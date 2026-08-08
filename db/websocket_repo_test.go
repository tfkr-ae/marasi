package db

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestWebSocketRepo_InsertConnection(t *testing.T) {
	t.Run("should insert a connection", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})
		wantTime := time.Now().UTC().Truncate(time.Millisecond)

		want := &domain.WebSocketConnection{
			ID:        wantID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: wantTime,
		}

		err = repo.InsertConnection(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetConnection(wantID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should fail when creating a second connection with a duplicate request ID", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		firstID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		secondID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        firstID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        secondID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "ws",
			Host:      "other.app",
			Path:      "/",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Fatalf("\nwanted:\nerror containing 'UNIQUE constraint failed'\ngot:\n%v", err)
		}
	})
	t.Run("should fail when request ID does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		missingReqID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        wantID,
			RequestID: missingReqID,
			State:     "open",
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})

		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Fatalf("\nwanted:\nerror containing 'FOREIGN KEY'\ngot:\n%v", err)
		}
	})
}

func TestWebSocketRepo_UpdateConnection(t *testing.T) {
	t.Run("should update lifecyle fields", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		testReqUUID := testRequest(t, repo, map[string]any{})

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		startedAt := time.Now().UTC().Truncate(time.Millisecond)
		closedAt := time.Now().UTC().Truncate(time.Millisecond)

		want := &domain.WebSocketConnection{
			ID:          connID,
			RequestID:   testReqUUID,
			State:       "closed",
			Transport:   "wss",
			Host:        "marasi.app",
			Path:        "/ws",
			StartedAt:   startedAt,
			ClosedAt:    &closedAt,
			CloseCode:   1000,
			CloseReason: "bye",
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: startedAt,
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.UpdateConnection(&domain.WebSocketConnection{
			ID:          connID,
			State:       "closed",
			ClosedAt:    &closedAt,
			CloseCode:   1000,
			CloseReason: "bye",
			Host:        "should-not-change.example",
			Path:        "/nope",
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetConnection(connID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should fail if connection doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		missingID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		err = repo.UpdateConnection(&domain.WebSocketConnection{
			ID:    missingID,
			State: "closed",
		})
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "no websocket connection found") {
			t.Fatalf("\nwanted:\nerror containing 'no websocket connection found'\ngot:\n%v", err)
		}
	})
}

func TestWebSocketRepo_GetConnection(t *testing.T) {
	t.Run("should return connection by id", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})
		wantTime := time.Now().UTC().Truncate(time.Millisecond)

		want := &domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: wantTime,
		}

		err = repo.InsertConnection(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetConnection(connID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should error if connection does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		missingID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		_, err = repo.GetConnection(missingID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "getting websocket connection") {
			t.Fatalf("\nwanted:\nerror containing 'getting websocket connection'\ngot:\n%v", err)
		}
	})
}

func TestWebSocketRepo_GetConnectionByRequestID(t *testing.T) {
	t.Run("should return connection for upgrade request", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		testReqUUID := testRequest(t, repo, map[string]any{})
		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		wantTime := time.Now().UTC().Truncate(time.Millisecond)
		want := &domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: wantTime,
		}

		err = repo.InsertConnection(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetConnectionByRequestID(testReqUUID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should error if no connection exists for request", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		testReqUUID := testRequest(t, repo, map[string]any{})

		_, err := repo.GetConnectionByRequestID(testReqUUID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "getting websocket connection for request") {
			t.Fatalf("\nwanted:\nerror containing 'getting websocket connection for request'\ngot:\n%v", err)
		}
	})
}

func TestWebSocketRepo_InsertMessage(t *testing.T) {
	t.Run("should insert a message", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		msgID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})
		wantTime := time.Now().UTC().Truncate(time.Millisecond)

		want := &domain.WebSocketMessage{
			ID:           msgID,
			ConnectionID: connID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("hello"),
			IsBinary:     false,
			CreatedAt:    wantTime,
			Metadata:     map[string]any{"key": "value"},
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetMessage(msgID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should fail when connection ID does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		msgID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		missingConnID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		err = repo.InsertMessage(&domain.WebSocketMessage{
			ID:           msgID,
			ConnectionID: missingConnID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("hello"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		})
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Fatalf("\nwanted:\nerror containing 'FOREIGN KEY'\ngot:\n%v", err)
		}
	})

	t.Run("should fail when message ID already exists", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		msgID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})

		msg := &domain.WebSocketMessage{
			ID:           msgID,
			ConnectionID: connID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("first"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(msg)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(&domain.WebSocketMessage{
			ID:           msgID,
			ConnectionID: connID,
			Direction:    "server",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("dup"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		})
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Fatalf("\nwanted:\nerror containing 'UNIQUE constraint failed'\ngot:\n%v", err)
		}
	})
}

func TestWebSocketRepo_GetMessages(t *testing.T) {
	t.Run("should return messages ordered by id ascending", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		firstID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		secondID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})

		first := &domain.WebSocketMessage{
			ID:           firstID,
			ConnectionID: connID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("first"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		}
		second := &domain.WebSocketMessage{
			ID:           secondID,
			ConnectionID: connID,
			Direction:    "server",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("second"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(first)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(second)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetMessages(connID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []*domain.WebSocketMessage{first, second}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should return an empty slice when there are no messages", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetMessages(connID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(got))
		}
	})
}

func TestWebSocketRepo_GetMessage(t *testing.T) {
	t.Run("should return message by id", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		msgID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})
		wantTime := time.Now().UTC().Truncate(time.Millisecond)

		want := &domain.WebSocketMessage{
			ID:           msgID,
			ConnectionID: connID,
			Direction:    "server",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("hello"),
			IsBinary:     false,
			CreatedAt:    wantTime,
			Metadata:     map[string]any{"key": "value"},
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetMessage(msgID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should error if message does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		missingID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		_, err = repo.GetMessage(missingID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "getting websocket message") {
			t.Fatalf("\nwanted:\nerror containing 'getting websocket message'\ngot:\n%v", err)
		}
	})
}

func TestWebSocketRepo_CountMessages(t *testing.T) {
	t.Run("should return zero when there are no messages", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.CountMessages(connID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", got)
		}
	})

	t.Run("should count messages for a connection", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		firstID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		secondID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(&domain.WebSocketMessage{
			ID:           firstID,
			ConnectionID: connID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("a"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(&domain.WebSocketMessage{
			ID:           secondID,
			ConnectionID: connID,
			Direction:    "server",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("b"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.CountMessages(connID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got != 2 {
			t.Fatalf("\nwanted:\n2\ngot:\n%d", got)
		}
	})
}

func TestWebSocketRepo_GetMessagesByRequestID(t *testing.T) {
	t.Run("should return messages for upgrade request ordered by id ascending", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		firstID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		secondID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})
		otherReqUUID := testRequest(t, repo, map[string]any{})

		otherConnID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		otherMsgID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		first := &domain.WebSocketMessage{
			ID:           firstID,
			ConnectionID: connID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("one"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		}

		second := &domain.WebSocketMessage{
			ID:           secondID,
			ConnectionID: connID,
			Direction:    "server",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("two"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(first)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(second)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        otherConnID,
			RequestID: otherReqUUID,
			State:     "open",
			Transport: "ws",
			Host:      "other.app",
			Path:      "/",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.InsertMessage(&domain.WebSocketMessage{
			ID:           otherMsgID,
			ConnectionID: otherConnID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("other"),
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
			Metadata:     map[string]any{},
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetMessagesByRequestID(testReqUUID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []*domain.WebSocketMessage{first, second}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should return an empty slice when request has no connection", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		testReqUUID := testRequest(t, repo, map[string]any{})

		got, err := repo.GetMessagesByRequestID(testReqUUID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(got))
		}
	})

	t.Run("should return an empty slice when connection has no messages", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		connID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		testReqUUID := testRequest(t, repo, map[string]any{})

		err = repo.InsertConnection(&domain.WebSocketConnection{
			ID:        connID,
			RequestID: testReqUUID,
			State:     "open",
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetMessagesByRequestID(testReqUUID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(got))
		}
	})
}
