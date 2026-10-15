package db

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// queryPlan returns the EXPLAIN QUERY PLAN details of query, one per line.
func queryPlan(t *testing.T, repo *Repository, query string, args ...any) string {
	t.Helper()
	rows, err := repo.dbConn.Queryx(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}

// A project opened small and grown in one session must not keep the
// statistics it was opened with (ADR-0041). Opened with one host and many
// status codes, then grown on many hosts and one status code, a sorted page
// filtered to one sparse host and that status code must read the host
// index. With the statistics from the open it reads every pair with the
// status code.
func TestRepository_ReoptimizesAsTheProjectGrows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grow.marasi")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func() *Repository {
		conn, err := New(path, logger)
		if err != nil {
			t.Fatal(err)
		}
		return NewProxyRepo(conn)
	}

	repo := open()
	for i := 0; i < 50; i++ {
		insertTrafficResponse(t, repo, insertTraffic(t, repo, "GET", "common.example", "/"), 200+i)
	}
	repo.Close()
	repo = open()
	t.Cleanup(func() { repo.Close() })

	for i := 0; i < 3*optimizeEveryPairs; i++ {
		id := insertTraffic(t, repo, "GET", fmt.Sprintf("h%d.example", i%500), "/")
		insertTrafficResponse(t, repo, id, 200)
	}

	plan := queryPlan(t, repo, `SELECT id`+trafficSummarySource+
		` WHERE host = ? AND status_code = ? ORDER BY method ASC, id ASC LIMIT 51`, "h7.example", 200)
	if !strings.Contains(plan, "idx_request_host_id") {
		t.Errorf("a sorted page filtered to a sparse host reads\n%s\nwant it to use idx_request_host_id", plan)
	}
}
