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

func TestMoveLaunchpadEntriesToAnotherLaunchpad(t *testing.T) {
	t.Parallel()
	t.Run("should move the entries to their place in the target in their source order", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		source, s := movePad(t, repo, 5)
		target, d := movePad(t, repo, 3)

		moved, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{
			SourceID: source, TargetID: target, RequestIDs: []uuid.UUID{s[3], s[1], s[3]}, BeforeID: d[1],
		})
		if err != nil {
			t.Fatalf("MoveLaunchpadEntries() error = %v", err)
		}

		assertEntries(t, moved, []uuid.UUID{s[1], s[3]})
		assertEntries(t, entryIDs(t, repo, source, ""), []uuid.UUID{s[0], s[2], s[4]})
		assertEntries(t, entryIDs(t, repo, target, ""), []uuid.UUID{d[0], s[1], s[3], d[1], d[2]})
	})

	t.Run("should move the entries after the target's last entry without an entry to place them before", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		source, s := movePad(t, repo, 3)
		target, d := movePad(t, repo, 2)
		empty, _ := movePad(t, repo, 0)

		if _, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{SourceID: source, TargetID: target, RequestIDs: []uuid.UUID{s[2], s[0]}}); err != nil {
			t.Fatalf("MoveLaunchpadEntries() error = %v", err)
		}
		if _, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{SourceID: source, TargetID: empty, RequestIDs: []uuid.UUID{s[1]}}); err != nil {
			t.Fatalf("MoveLaunchpadEntries() to an empty launchpad error = %v", err)
		}

		assertEntries(t, entryIDs(t, repo, source, ""), []uuid.UUID{})
		assertEntries(t, entryIDs(t, repo, target, ""), []uuid.UUID{d[0], d[1], s[0], s[2]})
		assertEntries(t, entryIDs(t, repo, empty, ""), []uuid.UUID{s[1]})
	})

	t.Run("should leave the moved pairs' metadata unchanged", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		source, _ := movePad(t, repo, 0)
		target, _ := movePad(t, repo, 0)
		id := testRequest(t, repo, map[string]any{"launchpad_id": source.String(), "note": "kept"})
		if err := repo.LinkRequestToLaunchpad(id, source); err != nil {
			t.Fatalf("linking: %v", err)
		}

		if _, err := repo.MoveLaunchpadEntries(domain.LaunchpadMove{SourceID: source, TargetID: target, RequestIDs: []uuid.UUID{id}}); err != nil {
			t.Fatalf("MoveLaunchpadEntries() error = %v", err)
		}

		metadata, err := repo.GetMetadata(id)
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}
		if metadata["launchpad_id"] != source.String() || metadata["note"] != "kept" {
			t.Fatalf("\nwanted:\nlaunchpad_id %s and note kept\ngot:\n%v", source, metadata)
		}
	})

	t.Run("should refuse the whole move and change nothing", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		source, s := movePad(t, repo, 3)
		target, d := movePad(t, repo, 2)
		if err := repo.LinkRequestToLaunchpad(s[1], target); err != nil {
			t.Fatalf("linking %s to the target: %v", s[1], err)
		}
		d = append(d, s[1])
		unlinked := testRequest(t, repo, nil)

		for _, test := range []struct {
			name string
			move domain.LaunchpadMove
			want error
		}{
			{"a pair the target already holds", domain.LaunchpadMove{SourceID: source, TargetID: target, RequestIDs: []uuid.UUID{s[0], s[1]}}, domain.ErrLaunchpadAlreadyLinked},
			{"a pair that is not an entry of the source", domain.LaunchpadMove{SourceID: source, TargetID: target, RequestIDs: []uuid.UUID{s[0], unlinked}}, domain.ErrLaunchpadNotLinked},
			{"an anchor that is not an entry of the target", domain.LaunchpadMove{SourceID: source, TargetID: target, RequestIDs: []uuid.UUID{s[0]}, BeforeID: s[2]}, domain.ErrInvalidPlacement},
			{"a missing target", domain.LaunchpadMove{SourceID: source, TargetID: uuid.New(), RequestIDs: []uuid.UUID{s[0]}}, domain.ErrLaunchpadNotFound},
			{"a missing source", domain.LaunchpadMove{SourceID: uuid.New(), TargetID: target, RequestIDs: []uuid.UUID{s[0]}}, domain.ErrLaunchpadNotFound},
		} {
			if _, err := repo.MoveLaunchpadEntries(test.move); !errors.Is(err, test.want) {
				t.Fatalf("%s:\nwanted:\n%v\ngot:\n%v", test.name, test.want, err)
			}
		}

		assertEntries(t, entryIDs(t, repo, source, ""), s)
		assertEntries(t, entryIDs(t, repo, target, ""), d)
	})
}
