package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// idSelection returns an id selection of the given pairs.
func idSelection(ids ...uuid.UUID) domain.Selection {
	return domain.Selection{IDs: ids}
}

// listedHighlights returns each listed pair's highlight, 0 for none.
func listedHighlights(t *testing.T, repo *Repository) map[uuid.UUID]int {
	t.Helper()
	page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 500})
	if err != nil {
		t.Fatalf("ListTraffic() error = %v", err)
	}
	got := make(map[uuid.UUID]int, len(page.Items))
	for _, item := range page.Items {
		got[item.ID] = 0
		if item.Highlight != nil {
			got[item.ID] = *item.Highlight
		}
	}
	return got
}

func TestTrafficRepo_SetHighlight(t *testing.T) {
	t.Run("should highlight the selected pairs and list them with their highlight", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		a := insertTraffic(t, repo, "GET", "a.example.com", "/a")
		b := insertTraffic(t, repo, "GET", "a.example.com", "/b")
		c := insertTraffic(t, repo, "GET", "a.example.com", "/c")

		count, err := repo.SetHighlight(idSelection(a, c, uuid.New()), 3)
		if err != nil {
			t.Fatalf("SetHighlight() error = %v", err)
		}
		if count != 2 {
			t.Errorf("SetHighlight() count = %d, want 2", count)
		}
		got := listedHighlights(t, repo)
		want := map[uuid.UUID]int{a: 3, b: 0, c: 3}
		for id, h := range want {
			if got[id] != h {
				t.Errorf("highlight of %s = %d, want %d", id, got[id], h)
			}
		}
	})
}

func TestTrafficRepo_SetHighlight_CountsOnlyChanges(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	a := insertTraffic(t, repo, "GET", "a.example.com", "/a")
	b := insertTraffic(t, repo, "GET", "a.example.com", "/b")
	c := insertTraffic(t, repo, "GET", "a.example.com", "/c")

	steps := []struct {
		name  string
		run   func() (int, error)
		count int
		want  map[uuid.UUID]int
	}{
		{"set 2 on a and b", func() (int, error) { return repo.SetHighlight(idSelection(a, b), 2) }, 2, map[uuid.UUID]int{a: 2, b: 2, c: 0}},
		{"set 2 again", func() (int, error) { return repo.SetHighlight(idSelection(a, b), 2) }, 0, map[uuid.UUID]int{a: 2, b: 2, c: 0}},
		{"set 8 on b and c", func() (int, error) { return repo.SetHighlight(idSelection(b, c), 8) }, 2, map[uuid.UUID]int{a: 2, b: 8, c: 8}},
		{"set 8 on a, b and c", func() (int, error) { return repo.SetHighlight(idSelection(a, b, c), 8) }, 1, map[uuid.UUID]int{a: 8, b: 8, c: 8}},
		{"clear a and b", func() (int, error) { return repo.ClearHighlight(idSelection(a, b)) }, 2, map[uuid.UUID]int{a: 0, b: 0, c: 8}},
		{"clear a, b and c", func() (int, error) { return repo.ClearHighlight(idSelection(a, b, c)) }, 1, map[uuid.UUID]int{a: 0, b: 0, c: 0}},
		{"clear again", func() (int, error) { return repo.ClearHighlight(idSelection(a, b, c)) }, 0, map[uuid.UUID]int{a: 0, b: 0, c: 0}},
	}
	for _, step := range steps {
		count, err := step.run()
		if err != nil {
			t.Fatalf("%s: error = %v", step.name, err)
		}
		if count != step.count {
			t.Errorf("%s: count = %d, want %d", step.name, count, step.count)
		}
		got := listedHighlights(t, repo)
		for id, h := range step.want {
			if got[id] != h {
				t.Errorf("%s: highlight of %s = %d, want %d", step.name, id, got[id], h)
			}
		}
	}
}

func TestTrafficRepo_SetHighlight_QuerySelectionStopsAtAsOf(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	post1 := insertTraffic(t, repo, "POST", "a.example.com", "/1")
	get := insertTraffic(t, repo, "GET", "a.example.com", "/get")
	post2 := insertTraffic(t, repo, "POST", "a.example.com", "/2")
	late := insertTraffic(t, repo, "POST", "a.example.com", "/late")
	sel := domain.Selection{Query: &domain.SelectionQuery{Text: `method = "POST"`, AsOf: post2}}

	count, err := repo.SetHighlight(sel, 5)
	if err != nil {
		t.Fatalf("SetHighlight() error = %v", err)
	}
	if count != 2 {
		t.Errorf("SetHighlight() count = %d, want 2", count)
	}
	got := listedHighlights(t, repo)
	for id, h := range map[uuid.UUID]int{post1: 5, get: 0, post2: 5, late: 0} {
		if got[id] != h {
			t.Errorf("highlight of %s = %d, want %d", id, got[id], h)
		}
	}

	count, err = repo.ClearHighlight(sel)
	if err != nil {
		t.Fatalf("ClearHighlight() error = %v", err)
	}
	if count != 2 {
		t.Errorf("ClearHighlight() count = %d, want 2", count)
	}
}

// storedRowAndIndex returns the pair's request row, with its rowid, and every block
// of the search index, as text. Highlighting must leave both alone (ADR-0035).
func storedRowAndIndex(t *testing.T, repo *Repository, id uuid.UUID) string {
	t.Helper()
	row := map[string]any{}
	if err := repo.dbConn.QueryRowx(`SELECT rowid, * FROM request WHERE id = ?`, id).MapScan(row); err != nil {
		t.Fatalf("reading request row: %v", err)
	}
	var blocks []struct {
		ID    int64  `db:"id"`
		Block []byte `db:"block"`
	}
	if err := repo.dbConn.Select(&blocks, `SELECT id, block FROM traffic_index_data ORDER BY id`); err != nil {
		t.Fatalf("reading search index: %v", err)
	}
	return fmt.Sprintf("%v\n%v", row, blocks)
}

func TestTrafficRepo_SetHighlight_LeavesRequestAndIndexAlone(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{{name: "pair", request: "GET /a HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nbody text")}})
	id := ids["pair"]
	before := storedRowAndIndex(t, repo, id)

	for _, h := range []int{1, 7} {
		if _, err := repo.SetHighlight(idSelection(id), h); err != nil {
			t.Fatalf("SetHighlight(%d) error = %v", h, err)
		}
		if after := storedRowAndIndex(t, repo, id); after != before {
			t.Fatalf("after SetHighlight(%d):\nwanted:\n%s\ngot:\n%s", h, before, after)
		}
	}
	if _, err := repo.ClearHighlight(idSelection(id)); err != nil {
		t.Fatalf("ClearHighlight() error = %v", err)
	}
	if after := storedRowAndIndex(t, repo, id); after != before {
		t.Fatalf("after ClearHighlight():\nwanted:\n%s\ngot:\n%s", before, after)
	}
}

func TestTrafficRepo_DeletingAPairRemovesItsHighlight(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	kept := insertTraffic(t, repo, "GET", "a.example.com", "/kept")
	deleted := insertTraffic(t, repo, "GET", "a.example.com", "/deleted")
	if _, err := repo.SetHighlight(idSelection(kept, deleted), 4); err != nil {
		t.Fatalf("SetHighlight() error = %v", err)
	}

	// Nothing deletes pairs yet (ticket 18); a delete removes the request row.
	if _, err := repo.dbConn.Exec(`DELETE FROM request WHERE id = ?`, deleted); err != nil {
		t.Fatalf("deleting the pair: %v", err)
	}
	var highlighted []uuid.UUID
	if err := repo.dbConn.Select(&highlighted, `SELECT request_id FROM pair_highlight`); err != nil {
		t.Fatalf("reading highlights: %v", err)
	}
	if len(highlighted) != 1 || highlighted[0] != kept {
		t.Fatalf("highlighted pairs = %v, want only %s", highlighted, kept)
	}
}

func TestTrafficRepo_ReadsReturnHighlight(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	launchpadID, err := repo.CreateLaunchpad("lp", "")
	if err != nil {
		t.Fatalf("CreateLaunchpad() error = %v", err)
	}
	finding := &domain.Finding{ID: uuid.New(), Title: "f", Severity: "Low"}
	if err := repo.SaveFinding(finding); err != nil {
		t.Fatalf("SaveFinding() error = %v", err)
	}
	testCase := &domain.TestCase{ID: uuid.New(), Title: "tc"}
	if err := repo.SaveTestCase(testCase); err != nil {
		t.Fatalf("SaveTestCase() error = %v", err)
	}
	marked := insertTraffic(t, repo, "GET", "a.example.com", "/marked")
	plain := insertTraffic(t, repo, "GET", "a.example.com", "/plain")
	for _, id := range []uuid.UUID{marked, plain} {
		if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
			t.Fatalf("LinkRequestToLaunchpad() error = %v", err)
		}
		if err := repo.UpdateNote(id, "a note"); err != nil {
			t.Fatalf("UpdateNote() error = %v", err)
		}
		if err := repo.LinkRequestToFinding(finding.ID, id); err != nil {
			t.Fatalf("LinkRequestToFinding() error = %v", err)
		}
		if err := repo.LinkRequestToTestCase(testCase.ID, id); err != nil {
			t.Fatalf("LinkRequestToTestCase() error = %v", err)
		}
	}
	if _, err := repo.SetHighlight(idSelection(marked), 6); err != nil {
		t.Fatalf("SetHighlight() error = %v", err)
	}
	want := map[uuid.UUID]int{marked: 6, plain: 0}
	check := func(read string, items []*domain.RequestResponseSummary) {
		t.Helper()
		if len(items) != 2 {
			t.Fatalf("%s: %d items, want 2", read, len(items))
		}
		for _, item := range items {
			got := 0
			if item.Highlight != nil {
				got = *item.Highlight
			}
			if got != want[item.ID] {
				t.Errorf("%s: highlight of %s = %d, want %d", read, item.ID, got, want[item.ID])
			}
		}
	}

	page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListTraffic() error = %v", err)
	}
	check("ListTraffic", page.Items)
	launchpadItems, err := repo.GetLaunchpadRequests(launchpadID, "", nil)
	if err != nil {
		t.Fatalf("GetLaunchpadRequests() error = %v", err)
	}
	check("GetLaunchpadRequests", launchpadItems)
	notes, _, err := repo.ListNotes(nil, 10)
	if err != nil {
		t.Fatalf("ListNotes() error = %v", err)
	}
	check("ListNotes", notes)
	summary, err := repo.GetRequestResponseSummary()
	if err != nil {
		t.Fatalf("GetRequestResponseSummary() error = %v", err)
	}
	check("GetRequestResponseSummary", summary)
	findingItems, err := repo.GetFindingRequests(finding.ID)
	if err != nil {
		t.Fatalf("GetFindingRequests() error = %v", err)
	}
	check("GetFindingRequests", findingItems)
	testCaseItems, err := repo.GetTestCaseRequests(testCase.ID)
	if err != nil {
		t.Fatalf("GetTestCaseRequests() error = %v", err)
	}
	check("GetTestCaseRequests", testCaseItems)

	for id, h := range want {
		row, err := repo.GetRequestResponseRow(id)
		if err != nil {
			t.Fatalf("GetRequestResponseRow() error = %v", err)
		}
		got := 0
		if row.Highlight != nil {
			got = *row.Highlight
		}
		if got != h {
			t.Errorf("GetRequestResponseRow: highlight of %s = %d, want %d", id, got, h)
		}
	}
	rows, err := repo.GetRequestResponseRows([]uuid.UUID{marked, plain})
	if err != nil {
		t.Fatalf("GetRequestResponseRows() error = %v", err)
	}
	for _, row := range rows {
		got := 0
		if row.Highlight != nil {
			got = *row.Highlight
		}
		if got != want[row.Request.ID] {
			t.Errorf("GetRequestResponseRows: highlight of %s = %d, want %d", row.Request.ID, got, want[row.Request.ID])
		}
	}
}

func TestTrafficRepo_SetHighlight_Rejects(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	a := insertTraffic(t, repo, "GET", "a.example.com", "/a")

	for _, h := range []int{0, 9, -1} {
		if _, err := repo.SetHighlight(idSelection(a), h); !errors.Is(err, domain.ErrInvalidHighlight) {
			t.Errorf("SetHighlight(%d) error = %v, want ErrInvalidHighlight", h, err)
		}
	}
	bad := domain.Selection{Query: &domain.SelectionQuery{Text: `method =`, AsOf: a}}
	var queryErr *domain.QueryError
	if _, err := repo.SetHighlight(bad, 1); !errors.As(err, &queryErr) {
		t.Errorf("SetHighlight(invalid query) error = %v, want *QueryError", err)
	}
	if _, err := repo.ClearHighlight(bad); !errors.As(err, &queryErr) {
		t.Errorf("ClearHighlight(invalid query) error = %v, want *QueryError", err)
	}
	if got := listedHighlights(t, repo)[a]; got != 0 {
		t.Errorf("highlight after rejected calls = %d, want 0", got)
	}
}

// highlightQueryFixture is a project with pairs named by their highlight:
// none, one3, two3, h1 and h8, oldest first.
func highlightQueryFixture(t *testing.T) (*Repository, map[string]uuid.UUID) {
	t.Helper()
	repo, teardown := setupTestDB(t)
	t.Cleanup(teardown)
	ids := map[string]uuid.UUID{}
	for _, pair := range []struct {
		name string
		h    int
	}{{"none", 0}, {"one3", 3}, {"two3", 3}, {"h1", 1}, {"h8", 8}} {
		ids[pair.name] = insertTraffic(t, repo, "GET", "a.example.com", "/"+pair.name)
		if pair.h != 0 {
			if _, err := repo.SetHighlight(idSelection(ids[pair.name]), pair.h); err != nil {
				t.Fatalf("SetHighlight() error = %v", err)
			}
		}
	}
	return repo, ids
}

func TestTrafficRepo_ListTrafficHighlightQuery(t *testing.T) {
	repo, ids := highlightQueryFixture(t)
	names := func(items []*domain.RequestResponseSummary) string {
		var out []string
		for _, item := range items {
			for name, id := range ids {
				if id == item.ID {
					out = append(out, name)
				}
			}
		}
		return strings.Join(out, ",")
	}

	tests := []struct {
		query string
		want  string // names newest first
	}{
		{query: `highlight = 3`, want: "two3,one3"},
		{query: `highlight = 8`, want: "h8"},
		{query: `highlight = 5`, want: ""},
		{query: `highlight != 3`, want: "h8,h1,none"},
		{query: `highlight != 5`, want: "h8,h1,two3,one3,none"},
		{query: `highlight:*`, want: "h8,h1,two3,one3"},
		{query: `NOT highlight:*`, want: "none"},
		{query: `-highlight:*`, want: "none"},
		{query: `NOT highlight = 3`, want: "h8,h1,none"},
		{query: `highlight = 1 OR highlight = 8`, want: "h8,h1"},
		{query: `highlight:* AND path = "/one3"`, want: "one3"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			items, _, _, err := listPage(repo, nil, 200, test.query)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got := names(items); got != test.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", test.want, got)
			}
		})
	}
}

func TestTrafficRepo_ListTrafficHighlightQueryErrors(t *testing.T) {
	repo, _ := highlightQueryFixture(t)

	tests := []struct {
		query    string
		message  string
		position int
	}{
		{query: `highlight = 0`, message: `highlight needs a whole number from 1 to 8`, position: 13},
		{query: `highlight = 9`, message: `highlight needs a whole number from 1 to 8`, position: 13},
		{query: `highlight != -1`, message: `highlight needs a whole number from 1 to 8`, position: 14},
		{query: `highlight = "3"`, message: `highlight needs a whole number from 1 to 8`, position: 13},
		{query: `highlight = 2.5`, message: `highlight needs a whole number from 1 to 8`, position: 13},
		{query: `host = "a" AND highlight = red`, message: `highlight needs a whole number from 1 to 8`, position: 28},
		{query: `highlight > 3`, message: `operator > is not supported on highlight; use =, !=, or highlight:*`, position: 1},
		{query: `highlight <= 3`, message: `operator <= is not supported on highlight`, position: 1},
		{query: `highlight:3`, message: `operator : on highlight takes only *`, position: 1},
		{query: `highlight:"red"`, message: `operator : on highlight takes only *`, position: 1},
		{query: `highlight.colour = 3`, message: `field "highlight" has no member "colour"`, position: 1},
		{query: `highlight.colour:*`, message: `field "highlight" has no member "colour"`, position: 1},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			_, _, _, err := listPage(repo, nil, 200, test.query)
			assertQueryError(t, err, test.message, test.position)
			assertQueryError(t, ValidateTrafficQuery(test.query, nil), test.message, test.position)
		})
	}
}
