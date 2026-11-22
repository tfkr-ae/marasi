package db

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"

	"github.com/tfkr-ae/marasi/domain"
)

// insertStreamedPair stores a pair and records state for it, unless state is nil.
func insertStreamedPair(t *testing.T, repo *Repository, state *domain.StreamedState) uuid.UUID {
	t.Helper()
	time.Sleep(time.Millisecond)
	id := uuid.Must(uuid.NewV7())
	if err := repo.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: "https", Method: "POST", Host: "example.com", Path: "/svc/Method",
		Raw: []byte("POST /svc/Method HTTP/2\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
	}); err != nil {
		t.Fatalf("InsertRequest() error = %v", err)
	}
	if state != nil {
		if err := repo.RecordStreamedPair(&domain.StreamedPairRecord{PairID: id, State: *state}); err != nil {
			t.Fatalf("RecordStreamedPair() error = %v", err)
		}
	}
	return id
}

func framing(name string) *domain.StreamedDirectionState {
	return &domain.StreamedDirectionState{Framing: name}
}

func describeState(s *domain.StreamedState) string {
	if s == nil {
		return "<nil>"
	}
	describe := func(d *domain.StreamedDirectionState) string {
		if d == nil {
			return "-"
		}
		return d.Framing + "/" + d.Ending
	}
	return describe(s.Request) + " " + describe(s.Response)
}

func TestRecordStreamedPair(t *testing.T) {
	t.Parallel()

	t.Run("should keep a stored framing and ending a later record leaves out", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := insertStreamedPair(t, repo, &domain.StreamedState{Request: framing("grpc")})
		if err := repo.RecordStreamedPair(&domain.StreamedPairRecord{PairID: id, State: domain.StreamedState{Response: framing("grpc")}}); err != nil {
			t.Fatal(err)
		}
		row, err := repo.GetRequestResponseRow(id)
		if err != nil {
			t.Fatal(err)
		}
		if got := describeState(row.Streamed); got != "grpc/ grpc/" {
			t.Fatalf("the live state\nwanted:\ngrpc/ grpc/\ngot:\n%s", got)
		}
		ended := domain.StreamedState{
			Request:  &domain.StreamedDirectionState{Framing: "grpc", Ending: "complete"},
			Response: &domain.StreamedDirectionState{Framing: "grpc", Ending: "reset"},
		}
		if err := repo.RecordStreamedPair(&domain.StreamedPairRecord{PairID: id, State: ended}); err != nil {
			t.Fatal(err)
		}
		page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || describeState(page.Items[0].Streamed) != "grpc/complete grpc/reset" {
			t.Fatalf("the listed state\nwanted:\ngrpc/complete grpc/reset\ngot:\n%+v", page.Items)
		}
	})

	t.Run("should read a pair that is not streamed with no streamed state", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := insertStreamedPair(t, repo, nil)
		row, err := repo.GetRequestResponseRow(id)
		if err != nil || row.Streamed != nil {
			t.Fatalf("GetRequestResponseRow()\nwanted:\nno streamed state\ngot:\n%+v %v", row.Streamed, err)
		}
	})

	t.Run("should not fail for a pair that no longer exists", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		if err := repo.RecordStreamedPair(&domain.StreamedPairRecord{PairID: uuid.Must(uuid.NewV7()), State: domain.StreamedState{Request: framing("grpc")}}); err != nil {
			t.Fatalf("RecordStreamedPair() error = %v", err)
		}
	})
}

func TestStreamedPairQueryFields(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	plain := insertStreamedPair(t, repo, nil)
	call := insertStreamedPair(t, repo, &domain.StreamedState{Request: framing("grpc"), Response: framing("grpc")})
	events := insertStreamedPair(t, repo, &domain.StreamedState{Response: framing("sse")})
	web := insertStreamedPair(t, repo, &domain.StreamedState{Request: framing("grpc-web")})

	cases := []struct {
		query string
		want  []uuid.UUID
	}{
		{`streamed:*`, []uuid.UUID{call, events, web}},
		{`NOT streamed:*`, []uuid.UUID{plain}},
		{`request_framing = "grpc"`, []uuid.UUID{call}},
		{`request_framing = "grpc*"`, []uuid.UUID{call, web}},
		{`request_framing = "*"`, []uuid.UUID{call, web}},
		{`request_framing != "grpc"`, []uuid.UUID{plain, events, web}},
		{`response_framing = "sse"`, []uuid.UUID{events}},
		{`response_framing = "grpc" OR response_framing = "sse"`, []uuid.UUID{call, events}},
		{`streamed:* AND NOT response_framing = "*"`, []uuid.UUID{web}},
	}
	for _, c := range cases {
		t.Run("should match "+c.query, func(t *testing.T) {
			page, err := repo.ListTraffic(domain.TrafficQuery{Text: c.query, OrderBy: "requested_at", Limit: 10})
			if err != nil {
				t.Fatalf("ListTraffic() error = %v", err)
			}
			var got []uuid.UUID
			for _, item := range page.Items {
				got = append(got, item.ID)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", c.want, got)
			}
			count, err := repo.CountTrafficContext(context.Background(), domain.TrafficCountQuery{Text: c.query})
			if err != nil || count.Count != len(c.want) {
				t.Fatalf("CountTrafficContext()\nwanted:\n%d\ngot:\n%d %v", len(c.want), count.Count, err)
			}
		})
	}

	for _, query := range []string{`streamed = "grpc"`, `streamed:"grpc"`, `request_framing:"grpc"`, `request_framing > "a"`, `streamed.request:*`} {
		t.Run("should refuse "+query, func(t *testing.T) {
			var queryErr *domain.QueryError
			if err := ValidateTrafficQuery(query, nil, ""); !errors.As(err, &queryErr) {
				t.Fatalf("ValidateTrafficQuery()\nwanted:\na query error\ngot:\n%v", err)
			}
		})
	}
}

// streamedPairStateMigrationVersion is the migration that adds the streamed
// state columns.
const streamedPairStateMigrationVersion = 31

func TestStreamedPairStateMigration(t *testing.T) {
	t.Run("should backfill the streamed state of a pair stored before it from metadata.streamed", func(t *testing.T) {
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
			_, err = provider.UpTo(context.Background(), streamedPairStateMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", streamedPairStateMigrationVersion-1, err)
		}
		streamed, plain, malformed := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		for id, metadata := range map[uuid.UUID]string{
			streamed:  `{"streamed":{"request":{"framing":"grpc","messages":2,"ending":"complete"},"response":{"framing":"grpc","messages":1,"ending":"reset","reset_code":"INTERNAL_ERROR"}}}`,
			plain:     `{"foo":"bar"}`,
			malformed: `{not json`,
		} {
			if _, err := conn.Exec(`INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at, metadata)
				VALUES(?, 'https', 'POST', 'example.com', '/', '', ?, ?)`, id, time.Now(), metadata); err != nil {
				t.Fatalf("inserting: %v", err)
			}
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}

		repo := openProject(t, path)
		for id, want := range map[uuid.UUID]string{streamed: "grpc/complete grpc/reset", plain: "<nil>", malformed: "<nil>"} {
			var state dbStreamedState
			if err := repo.dbConn.Get(&state, `SELECT `+streamedStateColumns+` FROM request WHERE id = ?`, id); err != nil {
				t.Fatal(err)
			}
			if got := describeState(state.state()); got != want {
				t.Errorf("pair %s\nwanted:\n%s\ngot:\n%s", id, want, got)
			}
		}
	})
}
