package db

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// newStreamMessage is a stored stream message of pair in direction.
func newStreamMessage(t *testing.T, pair uuid.UUID, direction, payload string) *domain.StreamMessage {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	flags := uint8(0)
	return &domain.StreamMessage{
		ID: id, PairID: pair, Direction: direction, Framing: "grpc", Flags: &flags,
		Payload: []byte(payload), CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		Metadata: map[string]any{},
	}
}

func TestStreamRepo(t *testing.T) {
	t.Parallel()

	t.Run("should store a stream message and read it back by its ID", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pair := testRequest(t, repo, nil)
		want := newStreamMessage(t, pair, domain.StreamDirectionResponse, "ping")
		want.Compressed = true
		want.Metadata = map[string]any{"too_large": true, "size_seen": float64(70)}
		if err := repo.InsertStreamMessage(want); err != nil {
			t.Fatalf("inserting\nwanted:\nnil\ngot:\n%v", err)
		}
		got, err := repo.GetStreamMessage(want.ID)
		if err != nil {
			t.Fatalf("getting\nwanted:\nnil\ngot:\n%v", err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should store a message whose framing has no flags byte with none", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		message := newStreamMessage(t, testRequest(t, repo, nil), domain.StreamDirectionResponse, "data: 1\n\n")
		message.Framing, message.Flags = "sse", nil
		if err := repo.InsertStreamMessage(message); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetStreamMessage(message.ID)
		if err != nil || got.Flags != nil {
			t.Fatalf("\nwanted:\nno flags\ngot:\n%+v, %v", got, err)
		}
	})

	t.Run("should report a message that is not stored as not found", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		if _, err := repo.GetStreamMessage(uuid.New()); !errors.Is(err, domain.ErrStreamMessageNotFound) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrStreamMessageNotFound, err)
		}
	})

	t.Run("should list a pair's messages in the order they arrived, both directions together, a page at a time", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		pair := testRequest(t, repo, nil)
		other := testRequest(t, repo, nil)
		var want []uuid.UUID
		for i, direction := range []string{domain.StreamDirectionRequest, domain.StreamDirectionResponse, domain.StreamDirectionRequest} {
			message := newStreamMessage(t, pair, direction, string(rune('a'+i)))
			want = append(want, message.ID)
			if err := repo.InsertStreamMessage(message); err != nil {
				t.Fatal(err)
			}
			if err := repo.InsertStreamMessage(newStreamMessage(t, other, direction, "other")); err != nil {
				t.Fatal(err)
			}
		}
		first, cursor, err := repo.ListStreamMessages(pair, nil, 2)
		if err != nil || len(first) != 2 || cursor == nil {
			t.Fatalf("first page\nwanted:\n2 messages and a cursor\ngot:\n%d, %v, %v", len(first), cursor, err)
		}
		second, end, err := repo.ListStreamMessages(pair, cursor, 2)
		if err != nil || len(second) != 1 || end != nil {
			t.Fatalf("second page\nwanted:\n1 message and no cursor\ngot:\n%d, %v, %v", len(second), end, err)
		}
		got := []uuid.UUID{first[0].ID, first[1].ID, second[0].ID}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("order\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
}
