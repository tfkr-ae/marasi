package websocket

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func startTestInterception(
	interceptor *Interceptor,
	message *Message,
) (<-chan domain.WebSocketMessage, <-chan error) {
	notified := make(chan domain.WebSocketMessage, 1)
	result := make(chan error, 1)

	go func() {
		result <- interceptor.Intercept(message, func(snapshot domain.WebSocketMessage) {
			notified <- snapshot
		})
	}()

	return notified, result
}

func receiveInterceptedMessage(
	t *testing.T,
	notified <-chan domain.WebSocketMessage,
) domain.WebSocketMessage {
	t.Helper()

	select {
	case message := <-notified:
		return message
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for intercepted message")
		return domain.WebSocketMessage{}
	}
}

func receiveInterceptionResult(t *testing.T, result <-chan error) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for interception result")
		return nil
	}
}

func newInterceptionTestMessageID(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating message UUID: %v", err)
	}
	return id
}

func TestInterceptor_ResolveModifiesMessage(t *testing.T) {
	interceptor := NewInterceptor()
	message := &Message{
		ID:           newInterceptionTestMessageID(t),
		ConnectionID: uuid.New(),
		Frame: Frame{
			Fin:     true,
			Opcode:  OpText,
			Payload: []byte("original"),
		},
		Metadata: map[string]any{"source": "test"},
	}

	notified, result := startTestInterception(interceptor, message)
	snapshot := receiveInterceptedMessage(t, notified)
	if snapshot.ID != message.ID {
		t.Fatalf("wanted: %s\ngot: %s", message.ID, snapshot.ID)
	}
	if snapshot.Opcode != OpText {
		t.Fatalf("wanted: %d\ngot: %d", OpText, snapshot.Opcode)
	}
	if string(snapshot.Payload) != "original" {
		t.Fatalf("wanted: %q\ngot: %q", "original", snapshot.Payload)
	}

	pending := interceptor.Pending()
	if len(pending) != 1 {
		t.Fatalf("wanted: %d\ngot: %d", 1, len(pending))
	}
	pending[0].Payload[0] = 'X'
	if string(message.Frame.Payload) != "original" {
		t.Fatalf("wanted live payload: %q\ngot: %q", "original", message.Frame.Payload)
	}

	err := interceptor.Resolve(message.ID, InterceptionDecision{
		Resume:  true,
		Opcode:  OpBinary,
		Payload: []byte{},
	})
	if err != nil {
		t.Fatalf("resolving interception: %v", err)
	}
	if err := receiveInterceptionResult(t, result); err != nil {
		t.Fatalf("Intercept() returned unexpected error: %v", err)
	}

	if message.Dropped {
		t.Fatalf("wanted dropped: false\ngot: true")
	}
	if message.Frame.Opcode != OpBinary {
		t.Fatalf("wanted: %d\ngot: %d", OpBinary, message.Frame.Opcode)
	}
	if len(message.Frame.Payload) != 0 {
		t.Fatalf("wanted empty payload\ngot: %q", message.Frame.Payload)
	}
	if message.Metadata["intercepted"] != true {
		t.Fatalf("wanted intercepted metadata: true\ngot: %v", message.Metadata["intercepted"])
	}
	if message.Metadata["dropped"] != false {
		t.Fatalf("wanted dropped metadata: false\ngot: %v", message.Metadata["dropped"])
	}
	if len(interceptor.Pending()) != 0 {
		t.Fatalf("wanted pending count: %d\ngot: %d", 0, len(interceptor.Pending()))
	}
}

func TestInterceptor_ResolveDropsMessage(t *testing.T) {
	interceptor := NewInterceptor()
	message := &Message{
		ID:           newInterceptionTestMessageID(t),
		ConnectionID: uuid.New(),
		Frame: Frame{
			Fin:     true,
			Opcode:  OpText,
			Payload: []byte("drop me"),
		},
	}

	notified, result := startTestInterception(interceptor, message)
	receiveInterceptedMessage(t, notified)

	if err := interceptor.Resolve(message.ID, InterceptionDecision{Resume: false}); err != nil {
		t.Fatalf("resolving interception: %v", err)
	}
	if err := receiveInterceptionResult(t, result); err != nil {
		t.Fatalf("Intercept() returned unexpected error: %v", err)
	}
	if !message.Dropped {
		t.Fatalf("wanted dropped: true\ngot: false")
	}
	if message.Metadata["intercepted"] != true {
		t.Fatalf("wanted intercepted metadata: true\ngot: %v", message.Metadata["intercepted"])
	}
	if message.Metadata["dropped"] != true {
		t.Fatalf("wanted dropped metadata: true\ngot: %v", message.Metadata["dropped"])
	}
}

func TestInterceptor_ResolvesConnectionsIndependently(t *testing.T) {
	interceptor := NewInterceptor()
	first := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}
	second := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}

	firstNotified, firstResult := startTestInterception(interceptor, first)
	secondNotified, secondResult := startTestInterception(interceptor, second)
	receiveInterceptedMessage(t, firstNotified)
	receiveInterceptedMessage(t, secondNotified)

	if err := interceptor.Resolve(second.ID, InterceptionDecision{Resume: true, Opcode: OpText}); err != nil {
		t.Fatalf("resolving second interception: %v", err)
	}
	if err := receiveInterceptionResult(t, secondResult); err != nil {
		t.Fatalf("second Intercept() returned unexpected error: %v", err)
	}

	select {
	case err := <-firstResult:
		t.Fatalf("wanted first interception to remain blocked\ngot: %v", err)
	default:
	}

	if err := interceptor.Resolve(first.ID, InterceptionDecision{Resume: true, Opcode: OpBinary}); err != nil {
		t.Fatalf("resolving first interception: %v", err)
	}
	if err := receiveInterceptionResult(t, firstResult); err != nil {
		t.Fatalf("first Intercept() returned unexpected error: %v", err)
	}
}

func TestInterceptor_PendingReturnsUUIDV7Order(t *testing.T) {
	interceptor := NewInterceptor()
	first := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}
	second := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}
	third := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}

	thirdNotified, thirdResult := startTestInterception(interceptor, third)
	receiveInterceptedMessage(t, thirdNotified)
	firstNotified, firstResult := startTestInterception(interceptor, first)
	receiveInterceptedMessage(t, firstNotified)
	secondNotified, secondResult := startTestInterception(interceptor, second)
	receiveInterceptedMessage(t, secondNotified)

	pending := interceptor.Pending()
	if len(pending) != 3 {
		t.Fatalf("wanted: %d\ngot: %d", 3, len(pending))
	}
	if pending[0].ID != first.ID {
		t.Fatalf("wanted first ID: %s\ngot: %s", first.ID, pending[0].ID)
	}
	if pending[1].ID != second.ID {
		t.Fatalf("wanted second ID: %s\ngot: %s", second.ID, pending[1].ID)
	}
	if pending[2].ID != third.ID {
		t.Fatalf("wanted third ID: %s\ngot: %s", third.ID, pending[2].ID)
	}

	if err := interceptor.Resolve(first.ID, InterceptionDecision{Resume: false}); err != nil {
		t.Fatalf("resolving first interception: %v", err)
	}
	if err := interceptor.Resolve(second.ID, InterceptionDecision{Resume: false}); err != nil {
		t.Fatalf("resolving second interception: %v", err)
	}
	if err := interceptor.Resolve(third.ID, InterceptionDecision{Resume: false}); err != nil {
		t.Fatalf("resolving third interception: %v", err)
	}
	if err := receiveInterceptionResult(t, firstResult); err != nil {
		t.Fatalf("first Intercept() returned unexpected error: %v", err)
	}
	if err := receiveInterceptionResult(t, secondResult); err != nil {
		t.Fatalf("second Intercept() returned unexpected error: %v", err)
	}
	if err := receiveInterceptionResult(t, thirdResult); err != nil {
		t.Fatalf("third Intercept() returned unexpected error: %v", err)
	}
}

func TestInterceptor_RejectsDuplicateAndUnknownMessages(t *testing.T) {
	interceptor := NewInterceptor()
	message := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}

	notified, result := startTestInterception(interceptor, message)
	receiveInterceptedMessage(t, notified)

	err := interceptor.Intercept(message, nil)
	if !errors.Is(err, ErrInterceptionExists) {
		t.Fatalf("wanted: %v\ngot: %v", ErrInterceptionExists, err)
	}

	err = interceptor.Resolve(uuid.New(), InterceptionDecision{Resume: true})
	if !errors.Is(err, ErrInterceptionNotFound) {
		t.Fatalf("wanted: %v\ngot: %v", ErrInterceptionNotFound, err)
	}

	if err := interceptor.Resolve(message.ID, InterceptionDecision{Resume: false}); err != nil {
		t.Fatalf("resolving interception: %v", err)
	}
	if err := receiveInterceptionResult(t, result); err != nil {
		t.Fatalf("Intercept() returned unexpected error: %v", err)
	}
}

func TestInterceptor_CancelConnection(t *testing.T) {
	interceptor := NewInterceptor()
	connectionID := uuid.New()
	first := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: connectionID}
	second := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: connectionID}
	other := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}

	firstNotified, firstResult := startTestInterception(interceptor, first)
	secondNotified, secondResult := startTestInterception(interceptor, second)
	otherNotified, otherResult := startTestInterception(interceptor, other)
	receiveInterceptedMessage(t, firstNotified)
	receiveInterceptedMessage(t, secondNotified)
	receiveInterceptedMessage(t, otherNotified)

	interceptor.CancelConnection(connectionID)

	if err := receiveInterceptionResult(t, firstResult); err != nil {
		t.Fatalf("first Intercept() returned unexpected error: %v", err)
	}
	if err := receiveInterceptionResult(t, secondResult); err != nil {
		t.Fatalf("second Intercept() returned unexpected error: %v", err)
	}
	if !first.Dropped || !second.Dropped {
		t.Fatalf("wanted cancelled messages to be dropped")
	}

	pending := interceptor.Pending()
	if len(pending) != 1 {
		t.Fatalf("wanted: %d\ngot: %d", 1, len(pending))
	}
	if pending[0].ID != other.ID {
		t.Fatalf("wanted: %s\ngot: %s", other.ID, pending[0].ID)
	}

	if err := interceptor.Resolve(other.ID, InterceptionDecision{
		Resume:  true,
		Opcode:  OpText,
		Payload: bytes.Clone(other.Frame.Payload),
	}); err != nil {
		t.Fatalf("resolving other interception: %v", err)
	}
	if err := receiveInterceptionResult(t, otherResult); err != nil {
		t.Fatalf("other Intercept() returned unexpected error: %v", err)
	}
}

func TestInterceptor_CancelAll(t *testing.T) {
	interceptor := NewInterceptor()
	first := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}
	second := &Message{ID: newInterceptionTestMessageID(t), ConnectionID: uuid.New()}

	firstNotified, firstResult := startTestInterception(interceptor, first)
	secondNotified, secondResult := startTestInterception(interceptor, second)
	receiveInterceptedMessage(t, firstNotified)
	receiveInterceptedMessage(t, secondNotified)

	interceptor.CancelAll()

	if err := receiveInterceptionResult(t, firstResult); err != nil {
		t.Fatalf("first Intercept() returned unexpected error: %v", err)
	}
	if err := receiveInterceptionResult(t, secondResult); err != nil {
		t.Fatalf("second Intercept() returned unexpected error: %v", err)
	}
	if !first.Dropped || !second.Dropped {
		t.Fatalf("wanted cancelled messages to be dropped")
	}
	if len(interceptor.Pending()) != 0 {
		t.Fatalf("wanted pending count: %d\ngot: %d", 0, len(interceptor.Pending()))
	}
}
