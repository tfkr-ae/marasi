package db

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// TestBulk_RefusesQuerySelectionsWhileTheirJobsBuild checks that a bulk
// operation refuses a query selection whose query depends on an unfinished
// background job, and changes nothing (ticket 26).
func TestBulk_RefusesQuerySelectionsWhileTheirJobsBuild(t *testing.T) {
	t.Parallel()

	// pairCount counts request rows directly, past every index.
	pairCount := func(t *testing.T, repo *Repository) int {
		t.Helper()
		var n int
		if err := repo.dbConn.Get(&n, `SELECT count(*) FROM request`); err != nil {
			t.Fatalf("counting pairs: %v", err)
		}
		return n
	}
	// notedPair captures a pair and gives it note.
	notedPair := func(t *testing.T, repo *Repository, path, note string) uuid.UUID {
		t.Helper()
		id := insertTraffic(t, repo, "GET", "a.example.com", path)
		if err := repo.UpdateNote(id, note); err != nil {
			t.Fatalf("UpdateNote() error = %v", err)
		}
		return id
	}

	t.Run("should not delete a pair a negated note query would wrongly select during an annotation index rebuild", func(t *testing.T) {
		t.Parallel()
		repo, teardown := setupTestDB(t)
		defer teardown()
		keep := notedPair(t, repo, "/keep", "retain")
		sel := domain.Selection{Query: &domain.SelectionQuery{Text: `NOT note:"retain"`, AsOf: keep}}
		if count, err := repo.CountTraffic(domain.TrafficCountQuery{Text: sel.Query.Text, AsOf: &keep}); err != nil || count.Count != 0 {
			t.Fatalf("CountTraffic() before the rebuild = %+v, %v; want 0", count, err)
		}
		if err := repo.RebuildJob(domain.JobAnnotationIndex); err != nil {
			t.Fatalf("RebuildJob() error = %v", err)
		}

		count, err := repo.DeleteTraffic(sel)
		var incomplete *domain.SelectionIncompleteError
		if !errors.As(err, &incomplete) || !errors.Is(err, domain.ErrSelectionIncomplete) {
			t.Fatalf("DeleteTraffic() = %d, %v; want a *domain.SelectionIncompleteError", count, err)
		}
		if want := []domain.JobName{domain.JobAnnotationIndex}; !slices.Equal(incomplete.Jobs, want) {
			t.Errorf("SelectionIncompleteError.Jobs = %v, want %v", incomplete.Jobs, want)
		}
		if got := pairCount(t, repo); got != 1 {
			t.Errorf("pairs after the refused delete = %d, want 1", got)
		}
	})

	t.Run("should refuse every bulk operation on a sitemap query while the sitemap builds, and change nothing", func(t *testing.T) {
		t.Parallel()
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := notedPair(t, repo, "/one", "first")
		launchpadID, err := repo.CreateLaunchpad("bulk", "")
		if err != nil {
			t.Fatalf("CreateLaunchpad() error = %v", err)
		}
		if err := repo.RebuildJob(domain.JobSitemap); err != nil {
			t.Fatalf("RebuildJob() error = %v", err)
		}
		sel := domain.Selection{Query: &domain.SelectionQuery{Text: `sitemap != "https://b.example.com"`, AsOf: id}}
		target := domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: launchpadID}
		operations := map[string]func() (int, error){
			"SetHighlight":     func() (int, error) { return repo.SetHighlight(sel, 3) },
			"LinkTraffic":      func() (int, error) { return repo.LinkTraffic(sel, target) },
			"UnlinkTraffic":    func() (int, error) { return repo.UnlinkTraffic(sel, target) },
			"SetMetadataKey":   func() (int, error) { return repo.SetMetadataKey(sel, "k", []byte(`1`)) },
			"ClearMetadataKey": func() (int, error) { return repo.ClearMetadataKey(sel, "k") },
			"AppendNote":       func() (int, error) { return repo.AppendNote(sel, "more") },
			"ClearNotes":       func() (int, error) { return repo.ClearNotes(sel) },
			"DeleteTraffic":    func() (int, error) { return repo.DeleteTraffic(sel) },
		}
		for name, operation := range operations {
			if count, err := operation(); !errors.Is(err, domain.ErrSelectionIncomplete) {
				t.Errorf("%s() = %d, %v; want domain.ErrSelectionIncomplete", name, count, err)
			}
		}
		if note, err := repo.GetNote(id); err != nil || note != "first" {
			t.Errorf("note after the refused operations = %q, %v; want first", note, err)
		}
		if got := pairCount(t, repo); got != 1 {
			t.Errorf("pairs after the refused operations = %d, want 1", got)
		}
	})

	t.Run("should run id selections and queries without text or sitemap terms while jobs build", func(t *testing.T) {
		t.Parallel()
		repo, teardown := setupTestDB(t)
		defer teardown()
		one := notedPair(t, repo, "/one", "first")
		two := notedPair(t, repo, "/two", "second")
		for _, job := range []domain.JobName{domain.JobAnnotationIndex, domain.JobTrafficIndex, domain.JobSitemap} {
			if err := repo.RebuildJob(job); err != nil {
				t.Fatalf("RebuildJob(%s) error = %v", job, err)
			}
		}
		if count, err := repo.SetHighlight(idSelection(one), 2); err != nil || count != 1 {
			t.Errorf("SetHighlight(ids) = %d, %v; want 1, nil", count, err)
		}
		sel := domain.Selection{Query: &domain.SelectionQuery{Text: `path = "/two"`, AsOf: two}}
		if count, err := repo.DeleteTraffic(sel); err != nil || count != 1 {
			t.Errorf("DeleteTraffic(path query) = %d, %v; want 1, nil", count, err)
		}
	})

	t.Run("should run a text query selection once its jobs complete", func(t *testing.T) {
		t.Parallel()
		repo, teardown := setupTestDB(t)
		defer teardown()
		keep := notedPair(t, repo, "/keep", "retain")
		drop := notedPair(t, repo, "/drop", "other")
		sel := domain.Selection{Query: &domain.SelectionQuery{Text: `NOT note:"retain"`, AsOf: drop}}
		if count, err := repo.DeleteTraffic(sel); err != nil || count != 1 {
			t.Fatalf("DeleteTraffic() = %d, %v; want 1, nil", count, err)
		}
		if _, err := repo.GetRequestResponseRow(keep); err != nil {
			t.Errorf("GetRequestResponseRow(kept pair) error = %v", err)
		}
	})
}
