package db

import (
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestTrafficRepo_CountTraffic(t *testing.T) {
	t.Run("should count the pairs that match the query and survive the exclusion", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		insertTypedPair(t, repo, "application/json")
		insertTypedPair(t, repo, "image/png")
		insertTypedPair(t, repo, "application/json")
		other := insertTraffic(t, repo, "POST", "example.com", "/")
		insertTrafficResponse(t, repo, other, 200)

		got, err := repo.CountTraffic(domain.TrafficCountQuery{Text: `method = "GET"`, Exclusion: []string{"image/*"}})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Count != 2 || got.Capped {
			t.Fatalf("\nwanted:\ncount 2, not capped\ngot:\n%+v", got)
		}
	})

	t.Run("should count only pairs after the after bound and at or below as_of", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		var ids []uuid.UUID
		for range 6 {
			ids = append(ids, insertTypedPair(t, repo, "application/json"))
		}

		cases := []struct {
			name  string
			asOf  *uuid.UUID
			after *uuid.UUID
			want  int
		}{
			{"as_of only", &ids[3], nil, 4},
			{"after only", nil, &ids[3], 2},
			{"both", &ids[4], &ids[1], 3},
			{"after the newest pair", nil, &ids[5], 0},
		}
		for _, c := range cases {
			got, err := repo.CountTraffic(domain.TrafficCountQuery{AsOf: c.asOf, After: c.after})
			if err != nil {
				t.Fatalf("%s:\nwanted:\nnil\ngot:\n%v", c.name, err)
			}
			if got.Count != c.want {
				t.Fatalf("%s:\nwanted:\n%d\ngot:\n%d", c.name, c.want, got.Count)
			}
		}
	})

	t.Run("should count exactly up to 100,000 and report capped above it", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		seedBareRequests(t, repo, 100_000)
		got, err := repo.CountTraffic(domain.TrafficCountQuery{})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Count != 100_000 || got.Capped {
			t.Fatalf("\nwanted:\ncount 100000, not capped\ngot:\n%d, capped %v", got.Count, got.Capped)
		}

		insertTraffic(t, repo, "GET", "example.com", "/")
		got, err = repo.CountTraffic(domain.TrafficCountQuery{})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Count != 100_000 || !got.Capped {
			t.Fatalf("\nwanted:\ncount 100000, capped\ngot:\n%d, capped %v", got.Count, got.Capped)
		}
	})
}

// seedBareRequests inserts n in-flight requests in one statement, with ids
// older than any uuid.NewV7, for tests that need many rows quickly.
func seedBareRequests(t *testing.T, repo *Repository, n int) {
	t.Helper()
	_, err := repo.dbConn.Exec(`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		INSERT INTO request (id, scheme, method, host, path, request_raw, requested_at)
		SELECT printf('00000000-0000-7000-8000-%012x', i), 'https', 'GET', 'example.com', '/', '', '2026-01-01T00:00:00Z' FROM seq`, n)
	if err != nil {
		t.Fatalf("seeding requests: %v", err)
	}
}
