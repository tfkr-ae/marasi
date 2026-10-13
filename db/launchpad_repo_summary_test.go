package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
	"modernc.org/sqlite"
)

func TestLaunchpadRepo_GetLaunchpadsEntrySummary(t *testing.T) {
	t.Run("should report no entries and no status for an empty launchpad", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id, err := repo.CreateLaunchpad("Empty", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		got, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []*domain.Launchpad{{ID: id, Name: "Empty", EntryCount: 0, LastStatusCode: nil}}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should report no status when the newest entry is in flight", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id := createTestLaunchpad(t, repo)
		older := testRequest(t, repo, nil)
		insertTestResponseWithStatusCode(t, repo, older, 200)
		newest := testRequest(t, repo, nil)
		linkTestRequests(t, repo, id, older, newest)

		assertLaunchpadSummary(t, launchpadByID(t, repo, id), 2, nil)
	})

	t.Run("should report the newest entry's status, not an older one's", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id := createTestLaunchpad(t, repo)
		older := testRequest(t, repo, nil)
		newest := testRequest(t, repo, nil)
		// Respond and link out of order so neither write order picks the newest.
		insertTestResponseWithStatusCode(t, repo, newest, 404)
		insertTestResponseWithStatusCode(t, repo, older, 200)
		linkTestRequests(t, repo, id, newest, older)

		assertLaunchpadSummary(t, launchpadByID(t, repo, id), 2, intPtr(404))
	})

	t.Run("should count 500 entries and report the newest entry's status", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		big := createTestLaunchpad(t, repo)
		other := createTestLaunchpad(t, repo)
		_ = createTestLaunchpad(t, repo)
		ids := make([]uuid.UUID, 500)
		for i := range ids {
			ids[i] = testRequest(t, repo, nil)
			insertTestResponseWithStatusCode(t, repo, ids[i], 200)
		}
		newest := testRequest(t, repo, nil)
		insertTestResponseWithStatusCode(t, repo, newest, 503)
		linkTestRequests(t, repo, big, ids[1:]...)
		linkTestRequests(t, repo, big, newest)
		linkTestRequests(t, repo, other, ids[0])

		assertLaunchpadSummary(t, launchpadByID(t, repo, big), 500, intPtr(503))
		assertLaunchpadSummary(t, launchpadByID(t, repo, other), 1, intPtr(200))
	})

	t.Run("should list every launchpad's summary with one SQL statement", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		for i := range 3 {
			id := createTestLaunchpad(t, repo)
			for range i + 1 {
				request := testRequest(t, repo, nil)
				insertTestResponseWithStatusCode(t, repo, request, 200)
				linkTestRequests(t, repo, id, request)
			}
		}

		counted, statements := countingRepo(t, repo)
		got, err := counted.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		counts := []int{}
		for _, launchpad := range got {
			counts = append(counts, launchpad.EntryCount)
		}
		if !reflect.DeepEqual(counts, []int{1, 2, 3}) {
			t.Fatalf("\nwanted:\n[1 2 3]\ngot:\n%v", counts)
		}
		if n := statements.Load(); n != 1 {
			t.Fatalf("\nwanted:\n1 statement\ngot:\n%d", n)
		}
	})
}

func TestLaunchpadRepo_GetLaunchpadEntrySummary(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	id := createTestLaunchpad(t, repo)
	request := testRequest(t, repo, nil)
	insertTestResponseWithStatusCode(t, repo, request, 302)
	linkTestRequests(t, repo, id, request)

	got, err := repo.GetLaunchpad(id)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	assertLaunchpadSummary(t, got, 1, intPtr(302))
}

func createTestLaunchpad(t *testing.T, repo *Repository) uuid.UUID {
	t.Helper()
	id, err := repo.CreateLaunchpad("Launchpad", "")
	if err != nil {
		t.Fatalf("creating launchpad: %v", err)
	}
	return id
}

func insertTestResponseWithStatusCode(t *testing.T, repo *Repository, reqID uuid.UUID, statusCode int) {
	t.Helper()
	resp := &domain.ProxyResponse{
		ID:          reqID,
		Status:      fmt.Sprintf("%d X", statusCode),
		StatusCode:  statusCode,
		Raw:         fmt.Appendf(nil, "HTTP/1.1 %d X\r\n\r\n", statusCode),
		Metadata:    map[string]any{},
		RespondedAt: time.Now(),
	}
	if err := repo.InsertResponse(resp); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
}

func linkTestRequests(t *testing.T, repo *Repository, launchpadID uuid.UUID, requestIDs ...uuid.UUID) {
	t.Helper()
	for _, requestID := range requestIDs {
		if err := repo.LinkRequestToLaunchpad(requestID, launchpadID); err != nil {
			t.Fatalf("linking request: %v", err)
		}
	}
}

func launchpadByID(t *testing.T, repo *Repository, id uuid.UUID) *domain.Launchpad {
	t.Helper()
	launchpads, err := repo.GetLaunchpads()
	if err != nil {
		t.Fatalf("getting launchpads: %v", err)
	}
	for _, launchpad := range launchpads {
		if launchpad.ID == id {
			return launchpad
		}
	}
	t.Fatalf("launchpad %s not listed", id)
	return nil
}

func assertLaunchpadSummary(t *testing.T, got *domain.Launchpad, wantCount int, wantStatus *int) {
	t.Helper()
	if got.EntryCount != wantCount || !reflect.DeepEqual(got.LastStatusCode, wantStatus) {
		t.Fatalf("\nwanted:\n%d entries, %s status\ngot:\n%d entries, %s status", wantCount, statusText(wantStatus), got.EntryCount, statusText(got.LastStatusCode))
	}
}

func intPtr(value int) *int { return &value }

func statusText(code *int) string {
	if code == nil {
		return "no"
	}
	return fmt.Sprintf("%d", *code)
}

// countingRepo opens a second connection to repo's database file through a
// connector that counts every statement sent to SQLite.
func countingRepo(t *testing.T, repo *Repository) (*Repository, *atomic.Int64) {
	t.Helper()
	var file string
	if err := repo.dbConn.Get(&file, `SELECT file FROM pragma_database_list WHERE name = 'main'`); err != nil {
		t.Fatalf("reading database path: %v", err)
	}
	statements := new(atomic.Int64)
	conn := sqlx.NewDb(sql.OpenDB(countingConnector{dsn: file, statements: statements}), "sqlite")
	t.Cleanup(func() { conn.Close() })
	return NewProxyRepo(conn), statements
}

type countingConnector struct {
	dsn        string
	statements *atomic.Int64
}

func (c countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return countingConn{Conn: conn, statements: c.statements}, nil
}

func (c countingConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type countingConn struct {
	driver.Conn
	statements *atomic.Int64
}

func (c countingConn) Prepare(query string) (driver.Stmt, error) {
	c.statements.Add(1)
	return c.Conn.Prepare(query)
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.statements.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.statements.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
