package db

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

// insertManyPairs writes pairs pairs directly, so a long read is quick to
// set up.
func insertManyPairs(t *testing.T, repo *Repository, pairs int) {
	t.Helper()
	statement := `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ` + fmt.Sprint(pairs) + `)
		INSERT INTO request (id, scheme, method, host, path, request_raw, status_code, content_type, metadata, requested_at)
		SELECT printf('00000000-0000-7000-8000-%012d', i), 'https', 'GET', 'x.com', '/a', 'GET / HTTP/1.1', 200,
			'text/' || (i % 5000), '{}', '2026-01-01' FROM n`
	if _, err := repo.dbConn.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func TestTrafficRepo_ReadsStopWhenContextEnds(t *testing.T) {
	// Not parallel: it sets its deadline from how long a read takes, which
	// other tests would change.
	repo, teardown := setupTestDB(t)
	defer teardown()
	insertManyPairs(t, repo, 100_000)
	// Until the sitemap job completes, content types are grouped from the
	// pairs themselves.
	if _, err := repo.dbConn.Exec(`UPDATE background_job SET complete = 0 WHERE name = ?`, domain.JobSitemap); err != nil {
		t.Fatal(err)
	}
	asOf := newestID(t, repo)

	reads := []struct {
		name string
		read func(ctx context.Context) error
	}{
		{"list", func(ctx context.Context) error {
			// No pair matches, so the read scans every pair.
			_, err := repo.ListTrafficContext(ctx, domain.TrafficQuery{Text: `path = "*zzz*"`, Limit: 10, AsOf: &asOf})
			return err
		}},
		{"count", func(ctx context.Context) error {
			_, err := repo.CountTrafficContext(ctx, domain.TrafficCountQuery{Text: `path = "*zzz*"`, AsOf: &asOf})
			return err
		}},
		{"content types", func(ctx context.Context) error {
			_, err := repo.CountContentTypesContext(ctx)
			return err
		}},
	}
	for _, tt := range reads {
		t.Run("should stop a "+tt.name+" read promptly when the context ends and free the connection", func(t *testing.T) {
			// The shorter of two full reads, so a cold first read does not
			// make the deadline land after a warm read is done.
			full := time.Duration(math.MaxInt64)
			for range 2 {
				start := time.Now()
				if err := tt.read(context.Background()); err != nil {
					t.Fatal(err)
				}
				full = min(full, time.Since(start))
			}

			ctx, cancel := context.WithTimeout(context.Background(), full/10)
			defer cancel()
			// A read that ignores its context runs to the end and returns
			// no error; only one the deadline reached returns it. How long
			// the stop took is not checked: on a loaded machine it measures
			// the scheduler, not the read.
			if err := tt.read(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("\nwanted:\ncontext.DeadlineExceeded from a read given %v of the %v a full read takes\ngot:\n%v", full/10, full, err)
			}

			if _, err := repo.CountTraffic(domain.TrafficCountQuery{Text: `method = "GET"`}); err != nil {
				t.Fatalf("a read after the cancelled one failed: %v", err)
			}
		})
	}
}
