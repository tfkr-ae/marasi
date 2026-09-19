package marasi

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// SetWebSocketIntercept enables or disables global WebSocket message interception.
func (proxy *Proxy) SetWebSocketIntercept(enabled bool) {
	proxy.webSocketIntercept.Store(enabled)
}

// GetWebSocketIntercept reports whether global WebSocket interception is enabled.
func (proxy *Proxy) GetWebSocketIntercept() bool {
	return proxy.webSocketIntercept.Load()
}

// GetPendingWebSocketInterceptions returns snapshots of paused WebSocket messages.
func (proxy *Proxy) GetPendingWebSocketInterceptions() []domain.WebSocketMessage {
	if proxy.WebSocketInterceptor == nil {
		return []domain.WebSocketMessage{}
	}
	return proxy.WebSocketInterceptor.Pending()
}

// ResolveWebSocketInterception applies a decision to a paused WebSocket message.
func (proxy *Proxy) ResolveWebSocketInterception(
	messageID uuid.UUID,
	decision marasiws.InterceptionDecision,
) error {
	if proxy.WebSocketInterceptor == nil {
		return errors.New("websocket interceptor not configured")
	}
	err := proxy.WebSocketInterceptor.Resolve(messageID, decision)
	if err == nil {
		proxy.forgetCheckpoint(messageID)
	}
	return err
}

// GetWebSocketConnection returns live connection state for an upgrade request ID.
func (proxy *Proxy) GetWebSocketConnection(requestID uuid.UUID) (domain.WebSocketConnection, bool) {
	if proxy.WebSocketRegistry == nil {
		return domain.WebSocketConnection{}, false
	}
	connection, exists := proxy.WebSocketRegistry.GetByRequestID(requestID)
	if !exists {
		return domain.WebSocketConnection{}, false
	}

	if proxy.WebSocketRepo != nil {
		record, err := proxy.WebSocketRepo.GetConnectionByRequestID(requestID)
		if err == nil && record != nil {
			return *record, true
		}
	}

	closeCode, closeReason := connection.CloseDetails()
	return domain.WebSocketConnection{
		ID:          connection.ID,
		RequestID:   connection.RequestID,
		State:       "open",
		CloseCode:   closeCode,
		CloseReason: closeReason,
	}, true
}

// GetWebSocketMessages returns persisted messages for an upgrade request ID.
func (proxy *Proxy) GetWebSocketMessages(requestID uuid.UUID) ([]*domain.WebSocketMessage, error) {
	if proxy.WebSocketRepo == nil {
		return nil, ErrWebSocketRepositoryNotSet
	}
	return proxy.WebSocketRepo.GetMessagesByRequestID(requestID)
}

// InjectWebSocketMessage injects a frame into a live connection.
// direction must be client or server.
func (proxy *Proxy) InjectWebSocketMessage(
	requestID uuid.UUID,
	direction string,
	opcode int,
	payload []byte,
) error {
	if proxy.WebSocketRegistry == nil {
		return fmt.Errorf("%w: %s", ErrWebSocketConnectionNotFound, requestID)
	}
	connection, exists := proxy.WebSocketRegistry.GetByRequestID(requestID)
	if !exists {
		return fmt.Errorf("%w: %s", ErrWebSocketConnectionNotFound, requestID)
	}

	switch direction {
	case marasiws.DirectionFromClient, marasiws.DirectionFromServer:
		return connection.Inject(direction, opcode, payload)
	default:
		return fmt.Errorf("%w: %q", ErrWebSocketDirection, direction)
	}
}

// CloseWebSocket closes a live connection identified by its upgrade request ID.
// A code of 0 is treated as normal closure 1000.
func (proxy *Proxy) CloseWebSocket(requestID uuid.UUID, code int, reason string) error {
	if proxy.WebSocketRegistry == nil {
		return fmt.Errorf("%w: %s", ErrWebSocketConnectionNotFound, requestID)
	}
	connection, exists := proxy.WebSocketRegistry.GetByRequestID(requestID)
	if !exists {
		return fmt.Errorf("%w: %s", ErrWebSocketConnectionNotFound, requestID)
	}
	if code == 0 {
		code = 1000
	}
	return connection.Close(code, reason)
}

// CloseWebSockets closes every live WebSocket connection.
func (proxy *Proxy) CloseWebSockets(code int, reason string) error {
	return proxy.closeWebSockets(code, reason, false)
}

// CloseWebSocketsAndFlush normally closes live WebSockets and flushes their persistence updates.
func (proxy *Proxy) CloseWebSocketsAndFlush() error {
	return proxy.closeWebSockets(marasiws.CloseNormalClosure, "", true)
}

func (proxy *Proxy) closeWebSockets(code int, reason string, flush bool) error {
	if proxy.WebSocketRegistry == nil {
		return nil
	}

	proxy.webSocketLifecycleMu.Lock()
	defer proxy.webSocketLifecycleMu.Unlock()

	closeErr := proxy.WebSocketRegistry.CloseAll(code, reason)
	proxy.webSocketSessions.Wait()
	if !flush {
		return closeErr
	}

	return errors.Join(closeErr, proxy.flushDBWrites())
}

// runWebSocketSession registers a connection, emits lifecycle events, and runs the relay.
func (proxy *Proxy) runWebSocketSession(connection *marasiws.Connection, record domain.WebSocketConnection) error {
	return proxy.runWebSocketSessionWithRelease(connection, record, nil)
}

func (proxy *Proxy) runWebSocketSessionWithRelease(connection *marasiws.Connection, record domain.WebSocketConnection, release func()) error {
	if connection.ID != record.ID {
		return fmt.Errorf(
			"websocket connection ID mismatch: %s != %s",
			connection.ID,
			record.ID,
		)
	}

	if connection.RequestID != record.RequestID {
		return fmt.Errorf(
			"websocket request ID mismatch: %s != %s",
			connection.RequestID,
			record.RequestID,
		)
	}

	proxy.webSocketLifecycleMu.RLock()
	if proxy.webSocketsClosing {
		proxy.webSocketLifecycleMu.RUnlock()
		_ = connection.Close(marasiws.CloseGoingAway, "Proxy shutting down")
		return nil
	}
	proxy.webSocketSessions.Add(1)
	if err := proxy.WebSocketRegistry.Add(connection); err != nil {
		proxy.webSocketSessions.Done()
		proxy.webSocketLifecycleMu.RUnlock()
		return fmt.Errorf("registering websocket connection: %w", err)
	}
	proxy.webSocketLifecycleMu.RUnlock()
	if release != nil {
		release()
	}
	defer func() {
		proxy.WebSocketRegistry.Remove(connection.ID)
		proxy.webSocketSessions.Done()
	}()

	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now()
	}
	record.State = "open"
	record.ClosedAt = nil

	openRecord := record
	proxy.DBWriteChannel <- &openRecord

	if proxy.OnWebSocketOpen != nil {
		_ = proxy.OnWebSocketOpen(openRecord)
	}

	runErr := connection.Run()

	closeCode, closeReason := connection.CloseDetails()
	closedAt := time.Now()

	closedRecord := record
	closedRecord.State = "closed"
	closedRecord.ClosedAt = &closedAt
	closedRecord.CloseCode = closeCode
	closedRecord.CloseReason = closeReason

	if runErr != nil && closedRecord.CloseCode == 0 {
		closedRecord.State = "error"
		closedRecord.CloseCode = 1006
	}

	proxy.DBWriteChannel <- &domain.WebSocketConnectionUpdate{
		Connection: closedRecord,
	}
	proxy.updateWebSocketRequestState(connection.RequestID, closedRecord.State)

	if proxy.OnWebSocketClose != nil {
		_ = proxy.OnWebSocketClose(closedRecord)
	}

	return runErr
}

// updateWebSocketRequestState merges websocket.state into the request metadata row.
func (proxy *Proxy) updateWebSocketRequestState(requestID uuid.UUID, state string) {
	if proxy.TrafficRepo == nil {
		return
	}

	metadata, err := proxy.TrafficRepo.GetMetadata(requestID)
	if err != nil {
		_ = proxy.WriteLog(
			"ERROR",
			fmt.Sprintf("getting metadata for websocket request %s : %s", requestID, err.Error()),
		)
		return
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["websocket.state"] = state

	if err := proxy.TrafficRepo.UpdateMetadata(metadata, requestID); err != nil {
		_ = proxy.WriteLog(
			"ERROR",
			fmt.Sprintf("updating websocket state metadata for %s : %s", requestID, err.Error()),
		)
	}
}
