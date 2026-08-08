package websocket

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

var (
	// ErrInterceptionMessageNil is returned when intercepting a nil message.
	ErrInterceptionMessageNil = errors.New("interception message is nil")
	// ErrInterceptionExists is returned when a message is already pending interception.
	ErrInterceptionExists = errors.New("interception already pending")
	// ErrInterceptionNotFound is returned when resolving an unknown message ID.
	ErrInterceptionNotFound = errors.New("interception not found")
)

// InterceptionDecision is the user or app decision for a paused message.
type InterceptionDecision struct {
	// Resume indicates whether the message should be forwarded.
	Resume bool
	// Opcode is the final opcode when Resume is true.
	Opcode int
	// Payload is the final payload when Resume is true.
	Payload []byte
}

type pendingInterception struct {
	message *Message
	done    chan struct{}
}

// Interceptor pauses live messages until they are resolved or cancelled.
type Interceptor struct {
	mu      sync.RWMutex
	pending map[uuid.UUID]*pendingInterception
}

// NewInterceptor creates an empty interceptor.
func NewInterceptor() *Interceptor {
	return &Interceptor{
		pending: make(map[uuid.UUID]*pendingInterception),
	}
}

// Intercept registers message, notifies the caller with a snapshot, and blocks
// until the message is resolved or cancelled.
func (i *Interceptor) Intercept(message *Message, notify func(domain.WebSocketMessage)) error {
	if message == nil {
		return ErrInterceptionMessageNil
	}

	pending := &pendingInterception{
		message: message,
		done:    make(chan struct{}),
	}

	i.mu.Lock()
	if i.pending == nil {
		i.pending = make(map[uuid.UUID]*pendingInterception)
	}
	if _, exists := i.pending[message.ID]; exists {
		i.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrInterceptionExists, message.ID)
	}
	i.pending[message.ID] = pending
	snapshot := message.ToDomain()
	i.mu.Unlock()

	if notify != nil {
		notify(snapshot)
	}

	<-pending.done
	return nil
}

// Resolve applies decision to a pending message and unblocks its relay.
func (i *Interceptor) Resolve(messageID uuid.UUID, decision InterceptionDecision) error {
	i.mu.Lock()
	pending, exists := i.pending[messageID]
	if !exists {
		i.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrInterceptionNotFound, messageID)
	}

	if decision.Resume {
		pending.message.Dropped = false
		pending.message.Frame.Opcode = decision.Opcode
		pending.message.Frame.Payload = bytes.Clone(decision.Payload)
	} else {
		pending.message.Dropped = true
	}
	if pending.message.Metadata == nil {
		pending.message.Metadata = make(map[string]any)
	}
	pending.message.Metadata["intercepted"] = true
	pending.message.Metadata["dropped"] = pending.message.Dropped

	delete(i.pending, messageID)
	i.mu.Unlock()
	close(pending.done)

	return nil
}

// Pending returns domain snapshots of currently paused messages in UUIDv7 order.
func (i *Interceptor) Pending() []domain.WebSocketMessage {
	i.mu.RLock()
	defer i.mu.RUnlock()

	messages := make([]domain.WebSocketMessage, 0, len(i.pending))
	for _, pending := range i.pending {
		messages = append(messages, pending.message.ToDomain())
	}
	slices.SortFunc(messages, func(a, b domain.WebSocketMessage) int {
		return bytes.Compare(a.ID[:], b.ID[:])
	})

	return messages
}

// CancelConnection drops and unblocks all pending messages for a connection.
func (i *Interceptor) CancelConnection(connectionID uuid.UUID) {
	i.mu.Lock()

	var cancelled []*pendingInterception
	for messageID, pending := range i.pending {
		if pending.message.ConnectionID != connectionID {
			continue
		}

		pending.message.Dropped = true
		cancelled = append(cancelled, pending)
		delete(i.pending, messageID)
	}

	i.mu.Unlock()

	for _, pending := range cancelled {
		close(pending.done)
	}
}

// CancelAll drops and unblocks every pending interception.
func (i *Interceptor) CancelAll() {
	i.mu.Lock()

	cancelled := make([]*pendingInterception, 0, len(i.pending))
	for messageID, pending := range i.pending {
		pending.message.Dropped = true
		cancelled = append(cancelled, pending)
		delete(i.pending, messageID)
	}

	i.mu.Unlock()

	for _, pending := range cancelled {
		close(pending.done)
	}
}
