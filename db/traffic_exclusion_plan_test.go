package db

import (
	"strings"
	"testing"
)

// A count, and the sitemap's matching set, need only index columns for a
// query on one field, so the field's index is the whole read. A
// content-type exclusion must not change that plan: it filters the
// matches, not the project. As a plain condition on content_type it made
// SQLite walk every pair up to as_of in id order, seconds on a cold 100k
// project (ticket 25).
func TestTrafficMatch_ExclusionKeepsTheQueryIndex(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	// The statistics of ledgerbench's 100k project, where a plain
	// condition on content_type read every pair.
	if _, err := repo.dbConn.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.dbConn.Exec(`DELETE FROM sqlite_stat1 WHERE tbl = 'request'`); err != nil {
		t.Fatal(err)
	}
	for index, stat := range map[string]string{
		"idx_request_length_id":       "88000 401 1",
		"idx_request_status_code_id":  "88000 267 1",
		"idx_request_path_id":         "88000 2 1",
		"idx_request_method_id":       "88000 401 1",
		"idx_request_content_type_id": "88000 134 1",
		"idx_request_host_id":         "88000 51 1",
		"sqlite_autoindex_request_1":  "88000 1",
	} {
		if _, err := repo.dbConn.Exec(`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('request', ?, ?)`, index, stat); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.dbConn.Exec(`ANALYZE sqlite_schema`); err != nil {
		t.Fatal(err)
	}

	for text, index := range map[string]string{
		`status_code >= 500`:       "idx_request_status_code_id",
		`host = "app.example.com"`: "idx_request_host_id",
		`method = "POST"`:          "idx_request_method_id",
	} {
		match, err := newTrafficMatch(text, []string{"image/png", "font/*", "application/javascript"})
		if err != nil {
			t.Fatal(err)
		}
		match.add(`id <= ?`, "ffffffff-ffff-ffff-ffff-ffffffffffff")
		for name, query := range map[string]string{
			"count":          trafficCountSQL(match),
			"matched leaves": matchedLeavesSQL(match),
		} {
			args := append(append([]any{}, match.args...), 1)
			plan := queryPlan(t, repo, query, args[:strings.Count(query, "?")]...)
			var outer string
			for _, line := range strings.Split(plan, "\n") {
				if strings.Contains(line, " request ") || strings.HasSuffix(line, " request") {
					outer = line
					break
				}
			}
			if !strings.Contains(outer, index) {
				t.Errorf("the %s of %q with an exclusion reads\n%s\nwant it to read %s first", name, text, plan, index)
			}
		}
	}
}
