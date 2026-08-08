package websocket

import (
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Registry tracks live WebSocket connections by connection ID and request ID.
type Registry struct {
	mu                 sync.RWMutex
	connections        map[uuid.UUID]*Connection
	requestConnections map[uuid.UUID]*Connection
}

// NewRegistry creates an empty connection registry.
func NewRegistry() *Registry {
	return &Registry{
		connections:        make(map[uuid.UUID]*Connection),
		requestConnections: make(map[uuid.UUID]*Connection),
	}
}

// Add registers a live connection.
// It rejects nil connections and duplicate connection or request IDs.
func (r *Registry) Add(connection *Connection) error {
	if connection == nil {
		return fmt.Errorf("websocket connection is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.connections[connection.ID]; exists {
		return fmt.Errorf(
			"websocket connection already registered: %s",
			connection.ID,
		)
	}
	if connection.RequestID != uuid.Nil {
		if _, exists := r.requestConnections[connection.RequestID]; exists {
			return fmt.Errorf(
				"websocket request already registered: %s",
				connection.RequestID,
			)
		}
	}

	r.connections[connection.ID] = connection
	if connection.RequestID != uuid.Nil {
		r.requestConnections[connection.RequestID] = connection
	}
	return nil
}

// Get returns a live connection by connection ID.
func (r *Registry) Get(id uuid.UUID) (*Connection, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	connection, exists := r.connections[id]
	return connection, exists
}

// GetByRequestID returns a live connection by its HTTP upgrade request ID.
func (r *Registry) GetByRequestID(requestID uuid.UUID) (*Connection, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	connection, exists := r.requestConnections[requestID]
	return connection, exists
}

// Remove unregisters a connection by connection ID.
func (r *Registry) Remove(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	connection, exists := r.connections[id]
	if !exists {
		return
	}
	delete(r.connections, id)
	if connection.RequestID != uuid.Nil {
		delete(r.requestConnections, connection.RequestID)
	}
}

// CloseAll closes and unregisters every live connection.
func (r *Registry) CloseAll(code int, reason string) error {
	if _, err := EncodeClosePayload(code, reason); err != nil {
		return fmt.Errorf("encoding websocket close payload: %w", err)
	}

	r.mu.Lock()
	connections := make([]*Connection, 0, len(r.connections))
	for _, connection := range r.connections {
		connections = append(connections, connection)
	}
	r.connections = make(map[uuid.UUID]*Connection)
	r.requestConnections = make(map[uuid.UUID]*Connection)
	r.mu.Unlock()

	closeErrors := make([]error, 0)
	for _, connection := range connections {
		if err := connection.Close(code, reason); err != nil && !errors.Is(err, ErrConnectionClosed) {
			closeErrors = append(
				closeErrors,
				fmt.Errorf("closing websocket connection %s: %w", connection.ID, err),
			)
		}
	}

	return errors.Join(closeErrors...)
}
