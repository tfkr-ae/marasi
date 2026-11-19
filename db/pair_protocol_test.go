package db

import (
	"context"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// insertProtocolPair stores a pair with the given protocols and a response.
func insertProtocolPair(t *testing.T, repo *Repository, client, origin string) uuid.UUID {
	t.Helper()
	time.Sleep(time.Millisecond)
	id := uuid.Must(uuid.NewV7())
	if err := repo.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: "https", Method: "GET", Host: "example.com", Path: "/",
		Raw: []byte("GET / HTTP/2\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
		ClientProtocol: client, OriginProtocol: origin,
	}); err != nil {
		t.Fatalf("InsertRequest() error = %v", err)
	}
	return id
}

func TestPairProtocols(t *testing.T) {
	t.Parallel()

	t.Run("should read back the protocols a pair was stored with", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		h2 := insertProtocolPair(t, repo, domain.ProtocolHTTP2, domain.ProtocolHTTP2)
		h1 := insertProtocolPair(t, repo, domain.ProtocolHTTP1, domain.ProtocolHTTP1)

		for id, want := range map[uuid.UUID]string{h2: "h2", h1: "http/1.1"} {
			row, err := repo.GetRequestResponseRow(id)
			if err != nil {
				t.Fatalf("GetRequestResponseRow() error = %v", err)
			}
			if row.Request.ClientProtocol != want || row.Request.OriginProtocol != want {
				t.Fatalf("\nwanted:\n%s %s\ngot:\n%s %s", want, want, row.Request.ClientProtocol, row.Request.OriginProtocol)
			}
		}
		page, err := repo.ListTraffic(domain.TrafficQuery{OrderBy: "requested_at", Limit: 10})
		if err != nil {
			t.Fatalf("ListTraffic() error = %v", err)
		}
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ClientProtocol+" "+item.OriginProtocol)
		}
		if want := []string{"h2 h2", "http/1.1 http/1.1"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should store http/1.1 for a request that names no protocol", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := testRequest(t, repo, nil)
		row, err := repo.GetRequestResponseRow(id)
		if err != nil {
			t.Fatalf("GetRequestResponseRow() error = %v", err)
		}
		if row.Request.ClientProtocol != "http/1.1" || row.Request.OriginProtocol != "http/1.1" {
			t.Fatalf("\nwanted:\nhttp/1.1 http/1.1\ngot:\n%s %s", row.Request.ClientProtocol, row.Request.OriginProtocol)
		}
	})

	t.Run("should take the origin protocol from the response when it names one", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		translated := insertProtocolPair(t, repo, domain.ProtocolHTTP2, domain.ProtocolHTTP2)
		unnamed := insertProtocolPair(t, repo, domain.ProtocolHTTP2, domain.ProtocolHTTP2)
		for id, origin := range map[uuid.UUID]string{translated: domain.ProtocolHTTP1, unnamed: ""} {
			if err := repo.InsertResponse(&domain.ProxyResponse{
				ID: id, Status: "200 OK", StatusCode: 200, Raw: []byte{}, Metadata: map[string]any{},
				RespondedAt: time.Now(), OriginProtocol: origin,
			}); err != nil {
				t.Fatalf("InsertResponse() error = %v", err)
			}
		}
		for id, want := range map[uuid.UUID]string{translated: "http/1.1", unnamed: "h2"} {
			row, err := repo.GetRequestResponseRow(id)
			if err != nil {
				t.Fatalf("GetRequestResponseRow() error = %v", err)
			}
			if row.Request.ClientProtocol != "h2" || row.Request.OriginProtocol != want {
				t.Fatalf("\nwanted:\nh2 %s\ngot:\n%s %s", want, row.Request.ClientProtocol, row.Request.OriginProtocol)
			}
		}
	})

	t.Run("should select pairs by protocol in a query", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		h2 := insertProtocolPair(t, repo, domain.ProtocolHTTP2, domain.ProtocolHTTP1)
		h1 := insertProtocolPair(t, repo, domain.ProtocolHTTP1, domain.ProtocolHTTP1)

		for query, want := range map[string][]uuid.UUID{
			`client_protocol = "h2"`:        {h2},
			`client_protocol = "http/1.1"`:  {h1},
			`client_protocol != "h2"`:       {h1},
			`origin_protocol = "http/1.1"`:  {h2, h1},
			`origin_protocol = "h2"`:        nil,
			`client_protocol = "http*"`:     {h1},
			`-client_protocol = "http/1.1"`: {h2},
		} {
			page, err := repo.ListTraffic(domain.TrafficQuery{Text: query, OrderBy: "requested_at", Limit: 10})
			if err != nil {
				t.Fatalf("ListTraffic(%s) error = %v", query, err)
			}
			var got []uuid.UUID
			for _, item := range page.Items {
				got = append(got, item.ID)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s\nwanted:\n%v\ngot:\n%v", query, want, got)
			}
		}
	})

	t.Run("should sort traffic by protocol", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		a := insertProtocolPair(t, repo, domain.ProtocolHTTP1, domain.ProtocolHTTP2)
		b := insertProtocolPair(t, repo, domain.ProtocolHTTP2, domain.ProtocolHTTP1)
		c := insertProtocolPair(t, repo, domain.ProtocolHTTP1, domain.ProtocolHTTP1)

		for orderBy, want := range map[string][]uuid.UUID{
			"client_protocol":      {b, a, c},
			"client_protocol desc": {c, a, b},
			"origin_protocol":      {a, b, c},
			"origin_protocol desc": {c, b, a},
		} {
			for _, limit := range []int{1, 10} {
				got := flatten(walkForward(t, repo, domain.TrafficQuery{OrderBy: orderBy, Limit: limit}))
				if !slices.Equal(got, want) {
					t.Fatalf("%s limit %d\nwanted:\n%v\ngot:\n%v", orderBy, limit, want, got)
				}
			}
		}
	})
}

// pairProtocolsMigrationVersion is the migration that adds the protocols.
const pairProtocolsMigrationVersion = 28

func TestPairProtocolsMigration(t *testing.T) {
	t.Run("should give a pair captured before the migration HTTP/1.1 on both legs", func(t *testing.T) {
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
			_, err = provider.UpTo(context.Background(), pairProtocolsMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", pairProtocolsMigrationVersion-1, err)
		}
		id := uuid.Must(uuid.NewV7())
		if _, err := conn.Exec(`INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at)
			VALUES(?, 'https', 'GET', 'example.com', '/', '', ?)`, id, time.Now()); err != nil {
			t.Fatalf("inserting: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}

		repo := openProject(t, path)
		row, err := repo.GetRequestResponseRow(id)
		if err != nil {
			t.Fatalf("GetRequestResponseRow() error = %v", err)
		}
		if row.Request.ClientProtocol != "http/1.1" || row.Request.OriginProtocol != "http/1.1" {
			t.Fatalf("\nwanted:\nhttp/1.1 http/1.1\ngot:\n%s %s", row.Request.ClientProtocol, row.Request.OriginProtocol)
		}
	})
}
