package db

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// movePad creates a launchpad with n entries, linked in capture order.
func movePad(t *testing.T, repo *Repository, n int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	launchpadID, err := repo.CreateLaunchpad("Move", "")
	if err != nil {
		t.Fatalf("creating launchpad: %v", err)
	}
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = testRequest(t, repo, nil)
		if err := repo.LinkRequestToLaunchpad(ids[i], launchpadID); err != nil {
			t.Fatalf("linking %s: %v", ids[i], err)
		}
	}
	return launchpadID, ids
}

func TestMoveLaunchpadEntries(t *testing.T) {
	t.Parallel()
	t.Run("should place the moved entries before another entry", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pad, e := movePad(t, repo, 5)

		moved, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{
			SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[4], e[3]}, BeforeID: e[1],
		})
		if err != nil {
			t.Fatalf("MoveLaunchpadEntries() error = %v", err)
		}

		assertEntries(t, moved, []uuid.UUID{e[3], e[4]})
		assertEntries(t, entryIDs(t, repo, pad, ""), []uuid.UUID{e[0], e[3], e[4], e[1], e[2]})
	})

	t.Run("should place the moved entries at the top before the first entry", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pad, e := movePad(t, repo, 4)

		if _, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[2]}, BeforeID: e[0]}); err != nil {
			t.Fatalf("MoveLaunchpadEntries() error = %v", err)
		}

		assertEntries(t, entryIDs(t, repo, pad, ""), []uuid.UUID{e[2], e[0], e[1], e[3]})
	})

	t.Run("should place the moved entries at the end without an entry to place them before", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pad, e := movePad(t, repo, 4)

		if _, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[1], e[0]}}); err != nil {
			t.Fatalf("MoveLaunchpadEntries() error = %v", err)
		}

		assertEntries(t, entryIDs(t, repo, pad, ""), []uuid.UUID{e[2], e[3], e[0], e[1]})
	})

	t.Run("should keep the entry order of the moved entries and count a repeated id once", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pad, e := movePad(t, repo, 5)

		moved, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{
			SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[4], e[0], e[2], e[4], e[0]}, BeforeID: e[3],
		})
		if err != nil {
			t.Fatalf("MoveLaunchpadEntries() error = %v", err)
		}

		assertEntries(t, moved, []uuid.UUID{e[0], e[2], e[4]})
		assertEntries(t, entryIDs(t, repo, pad, ""), []uuid.UUID{e[1], e[0], e[2], e[4], e[3]})
	})

	t.Run("should keep working when entries are placed at the same spot again and again", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pad, e := movePad(t, repo, 3)

		for i := range 60 {
			moving, before := e[2], e[1]
			if i%2 == 1 {
				moving, before = e[1], e[2]
			}
			if _, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{moving}, BeforeID: before}); err != nil {
				t.Fatalf("move %d: MoveLaunchpadEntries() error = %v", i, err)
			}
		}

		assertEntries(t, entryIDs(t, repo, pad, ""), []uuid.UUID{e[0], e[1], e[2]})
	})

	t.Run("should refuse the whole move and change nothing", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pad, e := movePad(t, repo, 3)
		other, outside := movePad(t, repo, 1)
		unlinked := testRequest(t, repo, nil)

		for _, test := range []struct {
			name string
			move domain.LaunchpadMove
			want error
		}{
			{"a pair that is not an entry", domain.LaunchpadMove{SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[2], unlinked}, BeforeID: e[0]}, domain.ErrLaunchpadNotLinked},
			{"an entry of another launchpad", domain.LaunchpadMove{SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[2], outside[0]}}, domain.ErrLaunchpadNotLinked},
			{"an anchor that is not an entry", domain.LaunchpadMove{SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[2]}, BeforeID: outside[0]}, domain.ErrInvalidPlacement},
			{"an anchor that is moved", domain.LaunchpadMove{SourceID: pad, TargetID: pad, RequestIDs: []uuid.UUID{e[2], e[1]}, BeforeID: e[1]}, domain.ErrInvalidPlacement},
			{"a missing launchpad", domain.LaunchpadMove{SourceID: uuid.New(), TargetID: uuid.Nil, RequestIDs: []uuid.UUID{e[2]}}, domain.ErrLaunchpadNotFound},
		} {
			if test.move.TargetID == uuid.Nil {
				test.move.TargetID = test.move.SourceID
			}
			if _, err := repo.MoveLaunchpadEntries(test.move); !errors.Is(err, test.want) {
				t.Fatalf("%s:\nwanted:\n%v\ngot:\n%v", test.name, test.want, err)
			}
		}

		assertEntries(t, entryIDs(t, repo, pad, ""), e)
		assertEntries(t, entryIDs(t, repo, other, ""), outside)
	})
}
