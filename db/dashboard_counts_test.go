package db

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// dashboardCountsMigrationVersion is the migration that adds the maintained
// dashboard counts.
const dashboardCountsMigrationVersion = 23

// scannedCounts is what the dashboard counted before ticket 23, by scanning
// request: the oracle every maintained count must equal.
func scannedCounts(t *testing.T, q sqlx.Queryer) (pairs, intercepted int) {
	t.Helper()
	if err := sqlx.Get(q, &pairs, `SELECT COUNT(*) FROM request`); err != nil {
		t.Fatalf("counting pairs: %v", err)
	}
	if err := sqlx.Get(q, &intercepted, `SELECT COUNT(*) FROM request WHERE json_extract(metadata, '$.intercepted') = true`); err != nil {
		t.Fatalf("counting intercepted pairs: %v", err)
	}
	return pairs, intercepted
}

// assertDashboardCounts checks DashboardCounts, CountRows and
// CountIntercepted against the scan, and that the counts are complete.
func assertDashboardCounts(t *testing.T, repo *Repository, wantPairs, wantIntercepted int) {
	t.Helper()
	pairs, intercepted := scannedCounts(t, repo.dbConn)
	if pairs != wantPairs || intercepted != wantIntercepted {
		t.Fatalf("scan = %d pairs, %d intercepted; the test expects %d, %d", pairs, intercepted, wantPairs, wantIntercepted)
	}
	counts, err := repo.DashboardCounts()
	if err != nil {
		t.Fatalf("DashboardCounts() error = %v", err)
	}
	if counts.Pairs != wantPairs || counts.Intercepted != wantIntercepted {
		t.Errorf("DashboardCounts() = %d pairs, %d intercepted, want %d, %d", counts.Pairs, counts.Intercepted, wantPairs, wantIntercepted)
	}
	if !counts.Complete[domain.JobInterceptedCount] {
		t.Errorf("DashboardCounts().Complete = %v, want %s complete", counts.Complete, domain.JobInterceptedCount)
	}
	if got, err := repo.CountRows(); err != nil || got != wantPairs {
		t.Errorf("CountRows() = %d, %v, want %d", got, err, wantPairs)
	}
	if got, err := repo.CountIntercepted(); err != nil || got != wantIntercepted {
		t.Errorf("CountIntercepted() = %d, %v, want %d", got, err, wantIntercepted)
	}
}

func captureMetaPair(t *testing.T, repo *Repository, requestMeta, responseMeta map[string]any) uuid.UUID {
	t.Helper()
	id := testRequest(t, repo, requestMeta)
	if responseMeta == nil {
		responseMeta = map[string]any{}
	}
	err := repo.InsertResponse(&domain.ProxyResponse{
		ID: id, Status: "OK", StatusCode: 200, ContentType: "text/plain", Length: "0",
		Raw: []byte{}, Metadata: responseMeta,
	})
	if err != nil {
		t.Fatalf("InsertResponse() error = %v", err)
	}
	return id
}

func TestStatsRepo_DashboardCounts(t *testing.T) {
	t.Run("should count notes, launchpads, pairs and intercepted pairs, complete on a new project", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		noted := captureMetaPair(t, repo, map[string]any{"intercepted": true}, nil)
		captureMetaPair(t, repo, nil, map[string]any{"intercepted": true})
		captureMetaPair(t, repo, map[string]any{"intercepted": false}, nil)
		if err := repo.UpdateNote(noted, "a note"); err != nil {
			t.Fatalf("UpdateNote() error = %v", err)
		}
		if _, err := repo.CreateLaunchpad("lp", ""); err != nil {
			t.Fatalf("CreateLaunchpad() error = %v", err)
		}

		counts, err := repo.DashboardCounts()
		if err != nil {
			t.Fatalf("DashboardCounts() error = %v", err)
		}
		if counts.Notes != 1 || counts.Launchpads != 1 {
			t.Errorf("DashboardCounts() = %d notes, %d launchpads, want 1, 1", counts.Notes, counts.Launchpads)
		}
		assertDashboardCounts(t, repo, 3, 2)
	})

	t.Run("should follow metadata edits, bulk edits and deletes", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		a := captureMetaPair(t, repo, map[string]any{"intercepted": true}, nil)
		b := captureMetaPair(t, repo, nil, nil)
		c := captureMetaPair(t, repo, nil, nil)
		d := captureMetaPair(t, repo, map[string]any{"intercepted": true}, nil)
		assertDashboardCounts(t, repo, 4, 2)

		// A response that sets the key to false un-intercepts the pair.
		if err := repo.UpdateMetadata(map[string]any{"intercepted": false}, a); err != nil {
			t.Fatalf("UpdateMetadata() error = %v", err)
		}
		if err := repo.UpdateMetadata(map[string]any{"intercepted": true, "x": 1}, b); err != nil {
			t.Fatalf("UpdateMetadata() error = %v", err)
		}
		// Rewriting an intercepted pair's metadata keeps it counted once.
		if err := repo.UpdateMetadata(map[string]any{"intercepted": true, "y": 2}, b); err != nil {
			t.Fatalf("UpdateMetadata() error = %v", err)
		}
		assertDashboardCounts(t, repo, 4, 2)

		if _, err := repo.SetMetadataKey(idSelection(a, c), "intercepted", json.RawMessage(`true`)); err != nil {
			t.Fatalf("SetMetadataKey() error = %v", err)
		}
		assertDashboardCounts(t, repo, 4, 4)
		if _, err := repo.ClearMetadataKey(idSelection(a, b), "intercepted"); err != nil {
			t.Fatalf("ClearMetadataKey() error = %v", err)
		}
		assertDashboardCounts(t, repo, 4, 2)

		if _, err := repo.DeleteTraffic(idSelection(c, b)); err != nil {
			t.Fatalf("DeleteTraffic() error = %v", err)
		}
		assertDashboardCounts(t, repo, 2, 1)
		if _, err := repo.DeleteTraffic(idSelection(d)); err != nil {
			t.Fatalf("DeleteTraffic() error = %v", err)
		}
		assertDashboardCounts(t, repo, 1, 0)
	})

	t.Run("should count pairs an older binary captured and deleted, after the job checks the new ones", func(t *testing.T) {
		path, repo := fileProject(t)
		deleted := captureMetaPair(t, repo, map[string]any{"intercepted": true}, nil)
		captureMetaPair(t, repo, map[string]any{"intercepted": true}, nil)
		captureMetaPair(t, repo, nil, nil)
		if err := repo.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		// An older binary writes request rows directly, without pair
		// numbers or any of this binary's bookkeeping.
		conn, err := sqlx.Connect("sqlite", path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("connecting: %v", err)
		}
		for i, id := range ascendingIDs(t, 3) {
			insertRawPair(t, conn, id)
			if i < 2 {
				if _, err := conn.Exec(`UPDATE request SET metadata = '{"intercepted":true}' WHERE id = ?`, id); err != nil {
					t.Fatalf("marking: %v", err)
				}
			}
		}
		if _, err := conn.Exec(`DELETE FROM request WHERE id = ?`, deleted); err != nil {
			t.Fatalf("deleting: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		repo = openProject(t, path)
		defer repo.Close()
		counts, err := repo.DashboardCounts()
		if err != nil {
			t.Fatalf("DashboardCounts() error = %v", err)
		}
		if counts.Pairs != 5 || counts.Intercepted != 1 || counts.Complete[domain.JobInterceptedCount] {
			t.Errorf("DashboardCounts() before the job = %d pairs, %d intercepted, complete %v; want 5, 1, false",
				counts.Pairs, counts.Intercepted, counts.Complete[domain.JobInterceptedCount])
		}
		checked := runInterceptedJob(t, repo)
		if checked != 3 {
			t.Errorf("the job checked %d pairs, want only the 3 the older binary captured", checked)
		}
		assertDashboardCounts(t, repo, 5, 3)
	})
}

// runInterceptedJob runs the intercepted_count job to the end and returns how
// many pairs it checked.
func runInterceptedJob(t *testing.T, repo *Repository) int {
	t.Helper()
	total := 0
	for remaining := true; remaining; {
		n, more, err := repo.RunJobBatch(domain.JobInterceptedCount, 1<<20)
		if err != nil {
			t.Fatalf("RunJobBatch() error = %v", err)
		}
		total += n
		remaining = more
	}
	return total
}

func TestDashboardCountsMigration(t *testing.T) {
	t.Run("should count existing intercepted pairs with a background job, and report it until done", func(t *testing.T) {
		path, conn := projectBeforePairNumbers(t)
		ids := ascendingIDs(t, 7)
		for i, id := range ids {
			insertRawPair(t, conn, id)
			if i%2 == 0 {
				if _, err := conn.Exec(`UPDATE request SET metadata = '{"intercepted":true}' WHERE id = ?`, id); err != nil {
					t.Fatalf("marking: %v", err)
				}
			}
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		repo := openProject(t, path)
		defer repo.Close()
		counts, err := repo.DashboardCounts()
		if err != nil {
			t.Fatalf("DashboardCounts() error = %v", err)
		}
		if counts.Pairs != 7 {
			t.Errorf("DashboardCounts().Pairs = %d, want 7", counts.Pairs)
		}
		if counts.Complete[domain.JobInterceptedCount] {
			t.Errorf("DashboardCounts().Complete[%s] = true before the job ran", domain.JobInterceptedCount)
		}
		// The old method stays exact while the job runs.
		if got, err := repo.CountIntercepted(); err != nil || got != 4 {
			t.Errorf("CountIntercepted() = %d, %v, want 4", got, err)
		}

		// A pair is un-intercepted before the job reaches it, and another
		// is captured; the job must count neither twice nor wrongly.
		if err := repo.UpdateMetadata(map[string]any{}, ids[0]); err != nil {
			t.Fatalf("UpdateMetadata() error = %v", err)
		}
		captureMetaPair(t, repo, map[string]any{"intercepted": true}, nil)

		defer func(n int) { interceptedBatchPairs = n }(interceptedBatchPairs)
		interceptedBatchPairs = 3
		batches := 0
		for remaining := true; remaining; batches++ {
			_, remaining, err = repo.RunJobBatch(domain.JobInterceptedCount, 1<<20)
			if err != nil {
				t.Fatalf("RunJobBatch() error = %v", err)
			}
			if batches == 1 {
				// An edit to a pair the job has not reached yet.
				if err := repo.UpdateMetadata(map[string]any{"intercepted": true}, ids[1]); err != nil {
					t.Fatalf("UpdateMetadata() error = %v", err)
				}
			}
		}
		if batches < 2 {
			t.Fatalf("the job ran %d batches, want several", batches)
		}
		assertDashboardCounts(t, repo, 8, 5)

		// Reopening does not count again.
		if err := repo.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		reopened := openProject(t, path)
		defer reopened.Close()
		assertDashboardCounts(t, reopened, 8, 5)
	})
}
