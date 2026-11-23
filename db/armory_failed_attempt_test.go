package db

import (
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

func TestArmoryRepo_FailedAttempts(t *testing.T) {
	t.Run("should keep a run's failed attempts, oldest first, after the project is reopened", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		writeNewProject(t, path)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		conn, err := New(path, logger)
		if err != nil {
			t.Fatalf("opening the project: %v", err)
		}
		repo := NewProxyRepo(conn)
		template := armoryTestTemplate(t, repo, "Template")
		run := armoryTestRun(t, repo, template)
		other := armoryTestRun(t, repo, template)
		createdAt := time.Now().UTC().Truncate(time.Millisecond)
		attempts := []*domain.ArmoryFailedAttempt{
			{ID: armoryTestUUID(t), RunID: run.ID, Payloads: []domain.ArmoryPayload{{Position: 0, Value: "abc"}}, Error: "message 1: line 7 column 10: bad", CreatedAt: createdAt},
			{ID: armoryTestUUID(t), RunID: run.ID, Payloads: []domain.ArmoryPayload{{Position: 0, Value: "x"}, {Position: 1, Value: "\"y\n"}}, Error: "message 2: bad", CreatedAt: createdAt},
			{ID: armoryTestUUID(t), RunID: other.ID, Payloads: []domain.ArmoryPayload{}, Error: "other run", CreatedAt: createdAt},
		}
		for _, attempt := range attempts {
			if err := repo.CreateArmoryFailedAttempt(attempt); err != nil {
				t.Fatalf("recording a failed attempt: %v", err)
			}
		}
		requestID := testRequest(t, repo, nil)
		if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: run.ID, RequestID: requestID}); err != nil {
			t.Fatalf("creating armory entry: %v", err)
		}
		repo.Close()

		conn, err = New(path, logger)
		if err != nil {
			t.Fatalf("reopening the project: %v", err)
		}
		repo = NewProxyRepo(conn)
		defer repo.Close()

		got, err := repo.GetArmoryFailedAttempts(run.ID)
		if err != nil {
			t.Fatalf("listing failed attempts: %v", err)
		}
		if !reflect.DeepEqual(got, attempts[:2]) {
			t.Fatalf("\nwanted:\n%+v\n%+v\ngot:\n%+v", *attempts[0], *attempts[1], got)
		}

		want := domain.ArmoryRunProgress{Entries: 1, FailedAttempts: 2}
		gotRun, err := repo.GetArmoryRun(run.ID)
		if err != nil || gotRun.Progress != want {
			t.Fatalf("the run's progress\nwanted:\n%+v\ngot:\n%+v (%v)", want, gotRun, err)
		}
		runs, err := repo.GetArmoryRuns(template.ID)
		if err != nil || len(runs) != 2 || runs[0].Progress != want || runs[1].Progress != (domain.ArmoryRunProgress{FailedAttempts: 1}) {
			t.Fatalf("the listed runs' progress\nwanted:\n%+v and {FailedAttempts:1}\ngot:\n%+v (%v)", want, runs, err)
		}
	})

	t.Run("should list no failed attempts for a run with none", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		run := armoryTestRun(t, repo, armoryTestTemplate(t, repo, "Template"))

		got, err := repo.GetArmoryFailedAttempts(run.ID)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("\nwanted:\nan empty list\ngot:\n%#v (%v)", got, err)
		}
	})

	t.Run("should delete a run's failed attempts with the run", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		run := armoryTestRun(t, repo, armoryTestTemplate(t, repo, "Template"))
		if err := repo.CreateArmoryFailedAttempt(&domain.ArmoryFailedAttempt{ID: armoryTestUUID(t), RunID: run.ID, Error: "bad", CreatedAt: time.Now()}); err != nil {
			t.Fatalf("recording a failed attempt: %v", err)
		}

		if err := repo.DeleteArmoryRun(run.ID); err != nil {
			t.Fatalf("deleting the run: %v", err)
		}
		if got, err := repo.GetArmoryFailedAttempts(run.ID); err != nil || len(got) != 0 {
			t.Fatalf("failed attempts left\nwanted:\nnone\ngot:\n%+v (%v)", got, err)
		}
	})

	t.Run("should refuse a failed attempt for a run that does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		err := repo.CreateArmoryFailedAttempt(&domain.ArmoryFailedAttempt{ID: armoryTestUUID(t), RunID: armoryTestUUID(t), Error: "bad", CreatedAt: time.Now()})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}
