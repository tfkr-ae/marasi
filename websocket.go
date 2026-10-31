package marasi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
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

// InjectWebSocketMessage injects a message into a live connection.
// direction must be client or server. With permessage-deflate the message is
// compressed by the leg's compressor unless options.NeverCompress is set.
func (proxy *Proxy) InjectWebSocketMessage(
	requestID uuid.UUID,
	direction string,
	opcode int,
	payload []byte,
	options marasiws.InjectOptions,
) (domain.WebSocketMessage, error) {
	if proxy.WebSocketRegistry == nil {
		return domain.WebSocketMessage{}, fmt.Errorf("%w: %s", ErrWebSocketConnectionNotFound, requestID)
	}
	connection, exists := proxy.WebSocketRegistry.GetByRequestID(requestID)
	if !exists {
		return domain.WebSocketMessage{}, fmt.Errorf("%w: %s", ErrWebSocketConnectionNotFound, requestID)
	}

	switch direction {
	case marasiws.DirectionFromClient, marasiws.DirectionFromServer:
		return connection.Inject(direction, opcode, payload, options)
	default:
		return domain.WebSocketMessage{}, fmt.Errorf("%w: %q", ErrWebSocketDirection, direction)
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

	connection.PublishClosedRecord(closedRecord)
	if proxy.OnWebSocketClose != nil {
		_ = proxy.OnWebSocketClose(closedRecord)
	}

	_ = proxy.updateWebSocketRequestState(connection.RequestID, closedRecord.State)
	proxy.DBWriteChannel <- &domain.WebSocketConnectionUpdate{
		Connection: closedRecord,
	}

	return runErr
}

// markDroppedWebSocketHandshake records websocket.state dropped on an upgrade
// request whose response a user or extension dropped. A handshake the proxy
// rejected keeps the failed state it already recorded.
func (proxy *Proxy) markDroppedWebSocketHandshake(res *http.Response, err error) {
	if errors.Is(err, ErrWebSocketHandshakeFailed) || !marasiws.IsUpgradeRequest(res.Request) {
		return
	}
	if requestID, ok := core.RequestIDFromContext(res.Request.Context()); ok {
		proxy.endWebSocketHandshake(requestID, "dropped")
	}
}

// endWebSocketHandshake stores websocket.state failed or dropped on an upgrade
// that will not open, then announces it, so a client reading the pair on the
// announcement sees the new state.
func (proxy *Proxy) endWebSocketHandshake(requestID uuid.UUID, state string) {
	if err := proxy.updateWebSocketRequestState(requestID, state); err != nil {
		return
	}
	if proxy.OnWebSocketHandshakeEnded != nil {
		proxy.OnWebSocketHandshakeEnded(requestID, state)
	}
}

// webSocketRequestState is a queued websocket.state change for an upgrade
// request. The DB writer applies it after the request insert queued before
// it, then closes done.
type webSocketRequestState struct {
	requestID uuid.UUID
	state     string
	err       error
	done      chan struct{}
}

// updateWebSocketRequestState merges websocket.state into the request metadata
// row. The row is inserted by the DB writer, so the change goes through the
// same queue and lands after it. It returns once the change is stored, with
// the error it has already written as a proxy log.
func (proxy *Proxy) updateWebSocketRequestState(requestID uuid.UUID, state string) error {
	if proxy.TrafficRepo == nil {
		return nil
	}

	var err error
	if proxy.dbWriterStarted.Load() && proxy.DBWriteChannel != nil {
		change := &webSocketRequestState{requestID: requestID, state: state, done: make(chan struct{})}
		proxy.DBWriteChannel <- change
		<-change.done
		err = change.err
	} else {
		err = proxy.applyWebSocketRequestState(requestID, state)
	}
	if err != nil {
		_ = proxy.WriteLog("ERROR", err.Error())
	}
	return err
}

// applyWebSocketRequestState writes websocket.state into the request metadata row.
func (proxy *Proxy) applyWebSocketRequestState(requestID uuid.UUID, state string) error {
	metadata, err := proxy.TrafficRepo.GetMetadata(requestID)
	if err != nil {
		return fmt.Errorf("getting metadata for websocket request %s : %w", requestID, err)
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["websocket.state"] = state

	if err := proxy.TrafficRepo.UpdateMetadata(metadata, requestID); err != nil {
		return fmt.Errorf("updating websocket state metadata for %s : %w", requestID, err)
	}
	return nil
}
