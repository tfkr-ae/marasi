package db

import (
	"testing"
)

func TestStatsRepo_CountRows(t *testing.T) {
	t.Run("should return 0 when no requests exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 0
		got, err := repo.CountRows()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})

	t.Run("should return correct count when requests exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 2
		testRequest(t, repo, nil)
		testRequest(t, repo, nil)

		got, err := repo.CountRows()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})
}

func TestStatsRepo_CountNotes(t *testing.T) {
	t.Run("should return 0 if there are no notes", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		want := 0

		got, err := repo.CountNotes()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})

	t.Run("should return the correct note count when there are notes in the project", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 2
		reqID1 := testRequest(t, repo, nil)
		reqID2 := testRequest(t, repo, nil)
		reqID3 := testRequest(t, repo, nil)

		if err := repo.UpdateNote(reqID1, "Marasi Note 1"); err != nil {
			t.Fatalf("updating note for %s : %v", reqID1.String(), err)
		}

		if err := repo.UpdateNote(reqID2, "Marasi note 2"); err != nil {
			t.Fatalf("updating note for %s : %v", reqID2.String(), err)
		}

		if err := repo.UpdateNote(reqID3, ""); err != nil {
			t.Fatalf("updating note for %s : %v", reqID3.String(), err)
		}

		got, err := repo.CountNotes()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})
}

func TestStatsRepo_CountLaunchpads(t *testing.T) {
	t.Run("should return 0 when there are no launchpads", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 0
		got, err := repo.CountLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})

	t.Run("should return the correct launchpad count", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 2
		if _, err := repo.CreateLaunchpad("Test Marasi Launchpad 1", "Test Description"); err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		if _, err := repo.CreateLaunchpad("Test Marasi Launchpad 2", "Test Description 2"); err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		got, err := repo.CountLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})
}

func TestStatsRepo_CountIntercepted(t *testing.T) {
	t.Run("should return 0 when no intercepted requests exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 0
		testRequest(t, repo, nil)
		testRequest(t, repo, map[string]any{"intercepted": false})

		got, err := repo.CountIntercepted()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})

	t.Run("should return the correct intercepted count", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 2
		testRequest(t, repo, map[string]any{"intercepted": true})
		testRequest(t, repo, map[string]any{"intercepted": true, "other_data": "123"})
		testRequest(t, repo, nil)
		testRequest(t, repo, map[string]any{"intercepted": false})

		got, err := repo.CountIntercepted()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, got)
		}
	})
}
