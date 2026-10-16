package db

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/tfkr-ae/marasi/domain"
)

// pairNumberMigrationVersion is the migration that adds pair numbers.
const pairNumberMigrationVersion = 22

// projectBeforePairNumbers returns the path of a project migrated to just
// before pair numbers, and a connection to it.
func projectBeforePairNumbers(t *testing.T) (string, *sqlx.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.db")
	conn, err := sqlx.Connect("sqlite", path)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	migrationsFS, err := fs.Sub(embedMigrations, "migrations")
	if err != nil {
		t.Fatalf("migrations fs: %v", err)
	}
	migrateMu.Lock()
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn.DB, migrationsFS)
	if err == nil {
		_, err = provider.UpTo(context.Background(), pairNumberMigrationVersion-1)
	}
	migrateMu.Unlock()
	if err != nil {
		t.Fatalf("migrating to %d: %v", pairNumberMigrationVersion-1, err)
	}
	return path, conn
}

// insertRawPair inserts a pair as a binary without pair numbers stores it.
func insertRawPair(t *testing.T, conn *sqlx.DB, id uuid.UUID) {
	t.Helper()
	_, err := conn.Exec(`INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at) VALUES(?, 'https', 'GET', 'example.com', '/', '', ?)`, id, time.Now())
	if err != nil {
		t.Fatalf("inserting: %v", err)
	}
}

// openProject opens the project at path with this binary.
func openProject(t *testing.T, path string) *Repository {
	t.Helper()
	dbConn, err := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("db.New() failed: %v", err)
	}
	return NewProxyRepo(dbConn)
}

// numberOf returns a pair's number as its single read reports it.
func numberOf(t *testing.T, repo *Repository, id uuid.UUID) int64 {
	t.Helper()
	row, err := repo.GetRequestResponseRow(id)
	if err != nil {
		t.Fatalf("GetRequestResponseRow(%s) error = %v", id, err)
	}
	return row.Request.Number
}

// ascendingIDs returns n UUIDv7 ids in ascending order.
func ascendingIDs(t *testing.T, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	for i := range ids {
		time.Sleep(time.Millisecond)
		ids[i] = uuid.Must(uuid.NewV7())
	}
	return ids
}

func TestPairNumberMigration(t *testing.T) {
	t.Run("should number existing pairs 1 to N in id order", func(t *testing.T) {
		path, conn := projectBeforePairNumbers(t)
		ids := ascendingIDs(t, 4)
		// Inserted out of id order, so rowid order is not id order.
		for _, i := range []int{2, 0, 3, 1} {
			insertRawPair(t, conn, ids[i])
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		repo := openProject(t, path)
		defer repo.Close()
		for i, id := range ids {
			if got := numberOf(t, repo, id); got != int64(i+1) {
				t.Errorf("number of pair %d = %d, want %d", i, got, i+1)
			}
		}
	})
}

// fileProject returns the path of a new project file opened with this binary.
func fileProject(t *testing.T) (string, *Repository) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.db")
	return path, openProject(t, path)
}

// newPair returns a request with a new UUIDv7 id and no number.
func newPair(t *testing.T) *domain.ProxyRequest {
	t.Helper()
	return &domain.ProxyRequest{
		ID:          uuid.Must(uuid.NewV7()),
		Scheme:      "https",
		Method:      "GET",
		Host:        "example.com",
		Path:        "/",
		Raw:         []byte("GET / HTTP/1.1\r\n\r\n"),
		Metadata:    map[string]any{},
		RequestedAt: time.Now(),
	}
}

func TestTrafficRepo_PairNumbers(t *testing.T) {
	t.Run("should number pairs inserted without a number 1, 2, 3", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		for want := int64(1); want <= 3; want++ {
			req := newPair(t)
			if err := repo.InsertRequest(req); err != nil {
				t.Fatalf("InsertRequest() error = %v", err)
			}
			if req.Number != want {
				t.Errorf("InsertRequest() set Number = %d, want %d", req.Number, want)
			}
			if got := numberOf(t, repo, req.ID); got != want {
				t.Errorf("stored number = %d, want %d", got, want)
			}
		}
	})

	t.Run("should store the number the capture path took, and leave a gap for a failed insert", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		first := newPair(t)
		first.Number = repo.NextPairNumber()
		if err := repo.InsertRequest(first); err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}
		// A pair whose number was taken but whose insert fails: same id.
		failed := newPair(t)
		failed.ID = first.ID
		failed.Number = repo.NextPairNumber()
		if err := repo.InsertRequest(failed); err == nil {
			t.Fatalf("InsertRequest() of a duplicate id succeeded")
		}
		next := newPair(t)
		next.Number = repo.NextPairNumber()
		if err := repo.InsertRequest(next); err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}
		if got := numberOf(t, repo, first.ID); got != 1 {
			t.Errorf("first number = %d, want 1", got)
		}
		if got := numberOf(t, repo, next.ID); got != 3 {
			t.Errorf("number after the failed insert = %d, want 3", got)
		}
	})

	t.Run("should never reuse a deleted number, including after reopening", func(t *testing.T) {
		path, repo := fileProject(t)
		var ids []uuid.UUID
		for range 3 {
			ids = append(ids, insertTraffic(t, repo, "GET", "example.com", "/"))
		}
		// Delete the newest pair, so the highest number left is 2.
		if _, err := repo.DeleteTraffic(idSelection(ids[2])); err != nil {
			t.Fatalf("DeleteTraffic() error = %v", err)
		}
		if got := numberOf(t, repo, insertTraffic(t, repo, "GET", "example.com", "/")); got != 4 {
			t.Errorf("number after a delete = %d, want 4", got)
		}
		page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 10})
		if err != nil {
			t.Fatalf("ListTraffic() error = %v", err)
		}
		if _, err := repo.DeleteTraffic(idSelection(page.Items[0].ID)); err != nil {
			t.Fatalf("DeleteTraffic() error = %v", err)
		}
		if err := repo.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		repo = openProject(t, path)
		defer repo.Close()
		if got := repo.NextPairNumber(); got != 5 {
			t.Errorf("NextPairNumber() after reopening = %d, want 5", got)
		}
	})

	t.Run("should give 1000 concurrently captured pairs unique numbers", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		const pairs = 1000
		taken := make([]*domain.ProxyRequest, pairs)
		var wg sync.WaitGroup
		errs := make(chan error, pairs)
		for i := range pairs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := newPair(t)
				req.Number = repo.NextPairNumber()
				taken[i] = req
				errs <- repo.InsertRequest(req)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("InsertRequest() error = %v", err)
			}
		}
		seen := make(map[int64]bool, pairs)
		for _, req := range taken {
			if seen[req.Number] {
				t.Fatalf("number %d taken twice", req.Number)
			}
			seen[req.Number] = true
			if req.Number < 1 || req.Number > pairs {
				t.Errorf("number %d is outside 1..%d", req.Number, pairs)
			}
		}
		rows, err := repo.GetRequestResponseRows(idsOfRequests(taken))
		if err != nil {
			t.Fatalf("GetRequestResponseRows() error = %v", err)
		}
		for i, row := range rows {
			if row.Request.Number != taken[i].Number {
				t.Errorf("stored number of %s = %d, want %d", row.Request.ID, row.Request.Number, taken[i].Number)
			}
		}
	})

	t.Run("should keep numbers through VACUUM and a rebuild of request with foreign keys off", func(t *testing.T) {
		path, repo := fileProject(t)
		var ids []uuid.UUID
		for range 5 {
			ids = append(ids, insertTraffic(t, repo, "GET", "example.com", "/"))
		}
		if _, err := repo.DeleteTraffic(idSelection(ids[1])); err != nil {
			t.Fatalf("DeleteTraffic() error = %v", err)
		}
		if err := repo.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		conn, err := sqlx.Connect("sqlite", path)
		if err != nil {
			t.Fatalf("connecting: %v", err)
		}
		var schema string
		if err := conn.Get(&schema, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'request'`); err != nil {
			t.Fatalf("reading the request schema: %v", err)
		}
		rebuilt, ok := strings.CutPrefix(schema, "CREATE TABLE request")
		if !ok {
			t.Fatalf("unexpected request schema %q", schema)
		}
		for _, stmt := range []string{
			`PRAGMA foreign_keys = OFF`,
			// Triggers on other tables name request while it is missing.
			`PRAGMA legacy_alter_table = ON`,
			"CREATE TABLE request_rebuilt" + rebuilt,
			`INSERT INTO request_rebuilt SELECT * FROM request`,
			`DROP TABLE request`,
			`ALTER TABLE request_rebuilt RENAME TO request`,
			`VACUUM`,
		} {
			if _, err := conn.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		repo = openProject(t, path)
		defer repo.Close()
		got := map[uuid.UUID]int64{}
		for _, id := range []uuid.UUID{ids[0], ids[2], ids[3], ids[4]} {
			got[id] = numberOf(t, repo, id)
		}
		want := map[uuid.UUID]int64{ids[0]: 1, ids[2]: 3, ids[3]: 4, ids[4]: 5}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("numbers after VACUUM and rebuild = %v, want %v", got, want)
		}
	})
}

func idsOfRequests(reqs []*domain.ProxyRequest) []uuid.UUID {
	ids := make([]uuid.UUID, len(reqs))
	for i, req := range reqs {
		ids[i] = req.ID
	}
	return ids
}

func TestPairNumbers_OlderBinaryPairs(t *testing.T) {
	t.Run("should number pairs an older binary captured on the next open, after the highest number", func(t *testing.T) {
		path, repo := fileProject(t)
		first := insertTraffic(t, repo, "GET", "example.com", "/")
		deleted := insertTraffic(t, repo, "GET", "example.com", "/")
		if _, err := repo.DeleteTraffic(idSelection(deleted)); err != nil {
			t.Fatalf("DeleteTraffic() error = %v", err)
		}
		if err := repo.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		conn, err := sqlx.Connect("sqlite", path)
		if err != nil {
			t.Fatalf("connecting: %v", err)
		}
		older := ascendingIDs(t, 3)
		for _, i := range []int{1, 2, 0} {
			insertRawPair(t, conn, older[i])
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		repo = openProject(t, path)
		defer repo.Close()
		if got := numberOf(t, repo, first); got != 1 {
			t.Errorf("number of the pair this binary captured = %d, want 1", got)
		}
		for i, id := range older {
			if got, want := numberOf(t, repo, id), int64(i+3); got != want {
				t.Errorf("number of older-binary pair %d = %d, want %d", i, got, want)
			}
		}
		if got := numberOf(t, repo, insertTraffic(t, repo, "GET", "example.com", "/")); got != 6 {
			t.Errorf("number of the next capture = %d, want 6", got)
		}
	})
}

func TestTrafficRepo_ReadsReturnNumber(t *testing.T) {
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
	insertTraffic(t, repo, "GET", "a.example.com", "/unlinked")
	second := insertTraffic(t, repo, "GET", "a.example.com", "/second")
	third := insertTraffic(t, repo, "GET", "a.example.com", "/third")
	want := map[uuid.UUID]int64{second: 2, third: 3}
	for id := range want {
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
	check := func(read string, items []*domain.RequestResponseSummary) {
		t.Helper()
		if len(items) == 0 {
			t.Fatalf("%s: no items", read)
		}
		for _, item := range items {
			if w, ok := want[item.ID]; ok && item.Number != w {
				t.Errorf("%s: number of %s = %d, want %d", read, item.ID, item.Number, w)
			}
			if item.Number == 0 {
				t.Errorf("%s: %s has no number", read, item.ID)
			}
		}
	}

	page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListTraffic() error = %v", err)
	}
	check("ListTraffic", page.Items)
	launchpadItems, _, err := repo.GetLaunchpadRequests(launchpadID, "", nil, "")
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
}

// numberQueryFixture captures five pairs, numbered 1 to 5, and deletes the
// third. It returns the pairs' ids by number.
func numberQueryFixture(t *testing.T) (*Repository, map[int64]uuid.UUID) {
	t.Helper()
	repo, teardown := setupTestDB(t)
	t.Cleanup(teardown)
	ids := map[int64]uuid.UUID{}
	for n := int64(1); n <= 5; n++ {
		ids[n] = insertTraffic(t, repo, "GET", "a.example.com", fmt.Sprintf("/%d", n))
	}
	if _, err := repo.DeleteTraffic(idSelection(ids[3])); err != nil {
		t.Fatalf("DeleteTraffic() error = %v", err)
	}
	delete(ids, 3)
	return repo, ids
}

// listedNumbers returns the numbers of items, in order, comma-separated.
func listedNumbers(items []*domain.RequestResponseSummary) string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = fmt.Sprint(item.Number)
	}
	return strings.Join(out, ",")
}

func TestTrafficRepo_ListTrafficNumberQuery(t *testing.T) {
	repo, _ := numberQueryFixture(t)
	tests := []struct {
		query string
		want  string // numbers newest first
	}{
		{query: `number = 2`, want: "2"},
		{query: `number = 3`, want: ""},
		{query: `number = 99`, want: ""},
		{query: `number != 2`, want: "5,4,1"},
		{query: `number < 4`, want: "2,1"},
		{query: `number <= 4`, want: "4,2,1"},
		{query: `number > 2`, want: "5,4"},
		{query: `number >= 2`, want: "5,4,2"},
		{query: `number = -1`, want: ""},
		{query: `NOT number = 1`, want: "5,4,2"},
		{query: `number = 1 OR number = 5`, want: "5,1"},
		{query: `number > 1 AND path = "/4"`, want: "4"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			items, _, _, err := listPage(repo, nil, 200, test.query)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got := listedNumbers(items); got != test.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", test.want, got)
			}
		})
	}
}

func TestTrafficRepo_ListTrafficNumberQueryErrors(t *testing.T) {
	repo, _ := numberQueryFixture(t)
	tests := []struct {
		query    string
		message  string
		position int
	}{
		{query: `number = "3"`, message: `number needs a whole number`, position: 10},
		{query: `number = 2.5`, message: `number needs a whole number`, position: 10},
		{query: `number >= first`, message: `number needs a whole number`, position: 11},
		{query: `number:3`, message: `operator : is not supported on number`, position: 1},
		{query: `number:*`, message: `operator : is not supported on number`, position: 1},
		{query: `number.x = 3`, message: `field "number" has no member "x"`, position: 1},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			_, _, _, err := listPage(repo, nil, 200, test.query)
			assertQueryError(t, err, test.message, test.position)
			assertQueryError(t, ValidateTrafficQuery(test.query, nil, ""), test.message, test.position)
		})
	}
}

func TestTrafficRepo_ListTrafficOrderByNumber(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	// Numbers taken in one order and stored in another, as under concurrent
	// capture: number order differs from id order.
	reqs := make([]*domain.ProxyRequest, 5)
	for i := range reqs {
		reqs[i] = newPair(t)
		time.Sleep(time.Millisecond)
	}
	for i, n := range []int64{2, 1, 4, 3, 5} {
		reqs[i].Number = n
	}
	for _, req := range reqs {
		if err := repo.InsertRequest(req); err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}
	}
	for orderBy, want := range map[string]string{
		"number":      "1,2,3,4,5",
		"number desc": "5,4,3,2,1",
	} {
		t.Run(orderBy, func(t *testing.T) {
			var got []string
			cursor := ""
			for range 10 {
				page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 2, OrderBy: orderBy, Cursor: cursor})
				if err != nil {
					t.Fatalf("ListTraffic() error = %v", err)
				}
				got = append(got, listedNumbers(page.Items))
				if page.NextCursor == nil {
					break
				}
				cursor = *page.NextCursor
			}
			if joined := strings.Join(got, ","); joined != want {
				t.Fatalf("pages = %s, want %s", joined, want)
			}
			if err := ValidateTrafficQuery("", nil, orderBy); err != nil {
				t.Errorf("ValidateTrafficQuery(order_by %q) error = %v", orderBy, err)
			}
		})
	}
	t.Run("should walk backward from a cursor", func(t *testing.T) {
		got := walkBackward(t, repo, domain.TrafficQuery{Limit: 2, OrderBy: "number"})
		var flat []uuid.UUID
		for _, page := range got {
			flat = append(flat, page...)
		}
		// Display order reversed: numbers 5, 4, 3, 2, 1.
		want := []uuid.UUID{reqs[4].ID, reqs[2].ID, reqs[3].ID, reqs[0].ID, reqs[1].ID}
		if fmt.Sprint(flat) != fmt.Sprint(want) {
			t.Fatalf("backward walk = %v, want %v", flat, want)
		}
	})
}
