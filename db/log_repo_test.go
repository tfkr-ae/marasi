package db

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestLogRepo_GetLogs(t *testing.T) {
	t.Run("should return 0 logs if there are none", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 0
		got, err := repo.GetLogs()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, len(got))
		}
	})

	t.Run("should return the correct log count if there are entries in the DB", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 2
		fixedTime := time.Date(2025, 10, 20, 12, 0, 0, 0, time.UTC)
		reqID := testRequest(t, repo, nil)
		extID := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")

		logs := []*domain.Log{
			{
				ID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
				Timestamp: fixedTime,
				Level:     "INFO",
				Message:   "Log message 1",
				Context:   make(map[string]any),
			},
			{
				ID:          uuid.MustParse("00000000-0000-0000-0000-000000000002"),
				Timestamp:   fixedTime.Add(time.Second),
				Level:       "ERROR",
				Message:     "Log message 2",
				Context:     map[string]any{"key": "value"},
				RequestID:   &reqID,
				ExtensionID: &extID,
			},
		}

		for _, logEntry := range logs {
			err := repo.InsertLog(logEntry)
			if err != nil {
				t.Fatalf("inserting log: %v", err)
			}
		}

		got, err := repo.GetLogs()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != want {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, len(got))
		}

		if !reflect.DeepEqual(logs, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should insert a log with nil context", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		log := &domain.Log{
			ID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			Timestamp: time.Now(),
			Level:     "INFO",
			Message:   "Log message with nil context",
			Context:   nil,
		}

		err := repo.InsertLog(log)
		if err != nil {
			t.Fatalf("inserting log: %v", err)
		}

		got, err := repo.GetLogs()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", len(got))
		}

		if got[0].Context == nil {
			t.Fatalf("\nwanted:\nnon-nil empty map\ngot:\nnil")
		}

		if len(got[0].Context) != 0 {
			t.Fatalf("\nwanted:\nempty map\ngot:\nmap of len %d", len(got[0].Context))
		}
	})

	t.Run("should fail to insert log if the request ID doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		invalidReqID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

		log := &domain.Log{
			ID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			Timestamp: time.Now(),
			Level:     "INFO",
			Message:   "Log with invalid request ID",
			Context:   nil,
			RequestID: &invalidReqID,
		}

		err := repo.InsertLog(log)
		if err == nil {
			t.Fatalf("\nwanted:\non-nil\ngot:\n%v", err)
		}

		if !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Fatalf("\nwanted:\nerror containing 'FOREIGN KEY'\ngot:\n%v", err)
		}
	})

	t.Run("should fail to insert log if the extension ID doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		invalidExtID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

		log := &domain.Log{
			ID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			Timestamp:   time.Now(),
			Level:       "INFO",
			Message:     "Log with invalid extension ID",
			Context:     nil,
			ExtensionID: &invalidExtID,
		}

		err := repo.InsertLog(log)
		if err == nil {
			t.Fatalf("\nwanted:\non-nil\ngot:\n%v", err)
		}

		if !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Fatalf("\nwanted:\nerror containing 'FOREIGN KEY'\ngot:\n%v", err)
		}
	})

}
