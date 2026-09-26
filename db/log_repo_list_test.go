package db

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestLogRepo_ListLogs(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	middle := uuid.MustParse("01938030-0000-7000-8000-000000000001")
	newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
	for _, item := range []*domain.Log{
		{ID: older, Timestamp: time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC), Level: "DEBUG", Message: "older"},
		{ID: middle, Timestamp: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), Level: "INFO", Message: "middle"},
		{ID: newer, Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Level: "FATAL", Message: "newer"},
	} {
		if err := repo.InsertLog(item); err != nil {
			t.Fatalf("inserting log: %v", err)
		}
	}

	items, nextCursor, err := repo.ListLogs(nil, 2)
	if err != nil {
		t.Fatalf("listing logs: %v", err)
	}
	if len(items) != 2 || items[0].ID != newer || items[1].ID != middle {
		t.Fatalf("wanted newest-first IDs %s, %s, got %v", newer, middle, logIDs(items))
	}
	if nextCursor == nil || *nextCursor != middle {
		t.Fatalf("wanted next cursor %s, got %v", middle, nextCursor)
	}

	items, nextCursor, err = repo.ListLogs(nextCursor, 2)
	if err != nil {
		t.Fatalf("listing older logs: %v", err)
	}
	if len(items) != 1 || items[0].ID != older || nextCursor != nil {
		t.Fatalf("wanted final page [%s] with no cursor, got %v and %v", older, logIDs(items), nextCursor)
	}
}

func TestLogRepo_ListLogsEmpty(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	items, nextCursor, err := repo.ListLogs(nil, 200)
	if err != nil {
		t.Fatalf("listing logs: %v", err)
	}
	if items == nil || len(items) != 0 || nextCursor != nil {
		t.Fatalf("wanted an empty page with nil cursor, got %v and %v", items, nextCursor)
	}
}

func logIDs(items []*domain.Log) []uuid.UUID {
	ids := make([]uuid.UUID, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}
