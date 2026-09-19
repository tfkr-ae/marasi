package marasi

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

var (
	// ErrCheckpointNotFound is returned when a Checkpoint item is missing or already resolved.
	ErrCheckpointNotFound = errors.New("checkpoint item not found")
	// ErrCheckpointExists is returned when a Checkpoint id is already pending.
	ErrCheckpointExists = errors.New("checkpoint item already pending")
	// ErrInterceptResponseNotAllowed is returned when intercept_response is set on a non-request item.
	ErrInterceptResponseNotAllowed = errors.New("intercept_response is only valid for request items")
)

// CheckpointForward is the optional edit applied when forwarding a pending Checkpoint item.
type CheckpointForward struct {
	// Raw replaces the dumped HTTP request or response when non-nil.
	Raw *[]byte
	// InterceptResponse holds the matching response after a request forward.
	InterceptResponse bool
	// Payload replaces the WebSocket frame payload when non-nil.
	Payload *[]byte
	// Opcode replaces the WebSocket frame opcode when non-nil.
	Opcode *int
}

type pendingHTTP struct {
	item              domain.CheckpointItem
	req               *http.Request
	res               *http.Response
	done              chan struct{}
	dropped           bool
	interceptResponse bool
	rebuiltReq        *http.Request
	rebuiltRes        *http.Response
}

type httpHoldResult struct {
	dropped           bool
	interceptResponse bool
	rebuiltReq        *http.Request
	rebuiltRes        *http.Response
}

type checkpointState struct {
	mu     sync.Mutex
	http   map[uuid.UUID]*pendingHTTP
	order  []uuid.UUID
	wsConn map[uuid.UUID]uuid.UUID
}

func newCheckpointState() *checkpointState {
	return &checkpointState{
		http:   make(map[uuid.UUID]*pendingHTTP),
		wsConn: make(map[uuid.UUID]uuid.UUID),
	}
}

func cloneCheckpointItem(item domain.CheckpointItem) domain.CheckpointItem {
	item.Raw = bytes.Clone(item.Raw)
	item.Payload = bytes.Clone(item.Payload)
	return item
}

func checkpointItemFromWebSocket(message domain.WebSocketMessage) domain.CheckpointItem {
	return domain.CheckpointItem{
		ID:           message.ID,
		Type:         domain.CheckpointTypeWebSocket,
		Payload:      bytes.Clone(message.Payload),
		Opcode:       message.Opcode,
		Direction:    message.Direction,
		ConnectionID: message.ConnectionID,
		RequestID:    message.RequestID,
	}
}

func removeCheckpointID(order []uuid.UUID, id uuid.UUID) []uuid.UUID {
	return slices.DeleteFunc(order, func(existing uuid.UUID) bool {
		return existing == id
	})
}

// SetIntercept enables or disables global HTTP Checkpoint.
func (proxy *Proxy) SetIntercept(enabled bool) {
	proxy.httpIntercept.Store(enabled)
}

// GetIntercept reports whether global HTTP Checkpoint is enabled.
func (proxy *Proxy) GetIntercept() bool {
	return proxy.httpIntercept.Load()
}

// HasPendingCheckpoint reports whether any HTTP or WebSocket Checkpoint item is pending.
func (proxy *Proxy) HasPendingCheckpoint() bool {
	if proxy.httpPendingCount() != 0 {
		return true
	}
	return len(proxy.GetPendingWebSocketInterceptions()) != 0
}

func (proxy *Proxy) httpPendingCount() int {
	if proxy.checkpoint == nil {
		return 0
	}
	proxy.checkpoint.mu.Lock()
	defer proxy.checkpoint.mu.Unlock()
	return len(proxy.checkpoint.http)
}

// CheckpointItems returns pending HTTP and WebSocket items in hold order, oldest first.
func (proxy *Proxy) CheckpointItems() []domain.CheckpointItem {
	if proxy.checkpoint == nil {
		return []domain.CheckpointItem{}
	}

	wsByID := make(map[uuid.UUID]domain.CheckpointItem)
	for _, message := range proxy.GetPendingWebSocketInterceptions() {
		wsByID[message.ID] = checkpointItemFromWebSocket(message)
	}

	proxy.checkpoint.mu.Lock()
	defer proxy.checkpoint.mu.Unlock()

	items := make([]domain.CheckpointItem, 0, len(proxy.checkpoint.order))
	seen := make(map[uuid.UUID]struct{}, len(proxy.checkpoint.order))
	for _, id := range proxy.checkpoint.order {
		seen[id] = struct{}{}
		if pending, ok := proxy.checkpoint.http[id]; ok {
			items = append(items, cloneCheckpointItem(pending.item))
			continue
		}
		if item, ok := wsByID[id]; ok {
			items = append(items, item)
		}
	}
	for id, item := range wsByID {
		if _, ok := seen[id]; ok {
			continue
		}
		items = append(items, item)
	}
	return items
}

// GetCheckpoint returns one pending Checkpoint item.
func (proxy *Proxy) GetCheckpoint(id uuid.UUID) (domain.CheckpointItem, bool) {
	if proxy.checkpoint != nil {
		proxy.checkpoint.mu.Lock()
		pending, ok := proxy.checkpoint.http[id]
		if ok {
			item := cloneCheckpointItem(pending.item)
			proxy.checkpoint.mu.Unlock()
			return item, true
		}
		proxy.checkpoint.mu.Unlock()
	}

	for _, message := range proxy.GetPendingWebSocketInterceptions() {
		if message.ID == id {
			return checkpointItemFromWebSocket(message), true
		}
	}
	return domain.CheckpointItem{}, false
}

// ForwardCheckpoint dequeues a pending item and forwards it with optional edits.
func (proxy *Proxy) ForwardCheckpoint(id uuid.UUID, fwd CheckpointForward) error {
	if fwd.InterceptResponse {
		item, ok := proxy.GetCheckpoint(id)
		if !ok {
			return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
		}
		if item.Type != domain.CheckpointTypeRequest {
			return ErrInterceptResponseNotAllowed
		}
	}

	if err := proxy.forwardHTTP(id, fwd); err == nil || !errors.Is(err, ErrCheckpointNotFound) {
		return err
	}
	return proxy.forwardWebSocket(id, fwd)
}

// DropCheckpoint dequeues a pending item without sending it.
func (proxy *Proxy) DropCheckpoint(id uuid.UUID) error {
	if err := proxy.dropHTTP(id); err == nil || !errors.Is(err, ErrCheckpointNotFound) {
		return err
	}
	return proxy.dropWebSocket(id)
}

// DropAllCheckpoint unblocks every pending HTTP and WebSocket item as drop.
func (proxy *Proxy) DropAllCheckpoint() {
	var httpPending []*pendingHTTP
	if proxy.checkpoint != nil {
		proxy.checkpoint.mu.Lock()
		httpPending = make([]*pendingHTTP, 0, len(proxy.checkpoint.http))
		for id, pending := range proxy.checkpoint.http {
			pending.dropped = true
			httpPending = append(httpPending, pending)
			delete(proxy.checkpoint.http, id)
		}
		proxy.checkpoint.order = nil
		clear(proxy.checkpoint.wsConn)
		proxy.checkpoint.mu.Unlock()
	}

	for _, pending := range httpPending {
		close(pending.done)
	}
	if proxy.WebSocketInterceptor != nil {
		proxy.WebSocketInterceptor.CancelAll()
	}
}

func (proxy *Proxy) holdHTTPRequest(id uuid.UUID, raw []byte, req *http.Request) (httpHoldResult, error) {
	return proxy.holdHTTP(domain.CheckpointItem{
		ID:   id,
		Type: domain.CheckpointTypeRequest,
		Raw:  bytes.Clone(raw),
	}, req, nil)
}

func (proxy *Proxy) holdHTTPResponse(id uuid.UUID, raw []byte, res *http.Response) (httpHoldResult, error) {
	return proxy.holdHTTP(domain.CheckpointItem{
		ID:   id,
		Type: domain.CheckpointTypeResponse,
		Raw:  bytes.Clone(raw),
	}, nil, res)
}

func (proxy *Proxy) holdHTTP(item domain.CheckpointItem, req *http.Request, res *http.Response) (httpHoldResult, error) {
	if proxy.checkpoint == nil {
		proxy.checkpoint = newCheckpointState()
	}

	pending := &pendingHTTP{
		item: item,
		req:  req,
		res:  res,
		done: make(chan struct{}),
	}

	proxy.checkpoint.mu.Lock()
	if _, exists := proxy.checkpoint.http[item.ID]; exists {
		proxy.checkpoint.mu.Unlock()
		return httpHoldResult{}, fmt.Errorf("%w: %s", ErrCheckpointExists, item.ID)
	}
	proxy.checkpoint.http[item.ID] = pending
	proxy.checkpoint.order = append(proxy.checkpoint.order, item.ID)
	notify := proxy.OnIntercept
	proxy.checkpoint.mu.Unlock()

	if notify != nil {
		_ = notify(cloneCheckpointItem(item))
	}

	<-pending.done
	return httpHoldResult{
		dropped:           pending.dropped,
		interceptResponse: pending.interceptResponse,
		rebuiltReq:        pending.rebuiltReq,
		rebuiltRes:        pending.rebuiltRes,
	}, nil
}

func (proxy *Proxy) forwardHTTP(id uuid.UUID, fwd CheckpointForward) error {
	if proxy.checkpoint == nil {
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}

	proxy.checkpoint.mu.Lock()
	pending, ok := proxy.checkpoint.http[id]
	if !ok {
		proxy.checkpoint.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}

	raw := pending.item.Raw
	if fwd.Raw != nil {
		raw = *fwd.Raw
	}

	var err error
	switch pending.item.Type {
	case domain.CheckpointTypeRequest:
		pending.rebuiltReq, err = rawhttp.RebuildRequest(raw, pending.req)
		if err != nil {
			proxy.checkpoint.mu.Unlock()
			return fmt.Errorf("%w : %w", ErrRebuildRequest, err)
		}
	case domain.CheckpointTypeResponse:
		pending.rebuiltRes, err = rawhttp.RebuildResponse(raw, pending.res.Request)
		if err != nil {
			proxy.checkpoint.mu.Unlock()
			return fmt.Errorf("%w : %w", ErrRebuildResponse, err)
		}
	default:
		proxy.checkpoint.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}

	pending.dropped = false
	pending.interceptResponse = fwd.InterceptResponse
	delete(proxy.checkpoint.http, id)
	proxy.checkpoint.order = removeCheckpointID(proxy.checkpoint.order, id)
	proxy.checkpoint.mu.Unlock()
	close(pending.done)
	return nil
}

func (proxy *Proxy) dropHTTP(id uuid.UUID) error {
	if proxy.checkpoint == nil {
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}

	proxy.checkpoint.mu.Lock()
	pending, ok := proxy.checkpoint.http[id]
	if !ok {
		proxy.checkpoint.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	pending.dropped = true
	delete(proxy.checkpoint.http, id)
	proxy.checkpoint.order = removeCheckpointID(proxy.checkpoint.order, id)
	proxy.checkpoint.mu.Unlock()
	close(pending.done)
	return nil
}

func (proxy *Proxy) forwardWebSocket(id uuid.UUID, fwd CheckpointForward) error {
	item, ok := proxy.GetCheckpoint(id)
	if !ok || item.Type != domain.CheckpointTypeWebSocket {
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}

	decision := marasiws.InterceptionDecision{
		Resume:  true,
		Opcode:  item.Opcode,
		Payload: bytes.Clone(item.Payload),
	}
	if fwd.Opcode != nil {
		decision.Opcode = *fwd.Opcode
	}
	if fwd.Payload != nil {
		decision.Payload = bytes.Clone(*fwd.Payload)
	}
	return mapWebSocketResolveError(id, proxy.ResolveWebSocketInterception(id, decision))
}

func (proxy *Proxy) dropWebSocket(id uuid.UUID) error {
	item, ok := proxy.GetCheckpoint(id)
	if !ok || item.Type != domain.CheckpointTypeWebSocket {
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	return mapWebSocketResolveError(id, proxy.ResolveWebSocketInterception(id, marasiws.InterceptionDecision{Resume: false}))
}

func mapWebSocketResolveError(id uuid.UUID, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, marasiws.ErrInterceptionNotFound) {
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	return err
}

func (proxy *Proxy) noteWebSocketHold(message *marasiws.Message) {
	if proxy.checkpoint == nil {
		proxy.checkpoint = newCheckpointState()
	}
	proxy.checkpoint.mu.Lock()
	defer proxy.checkpoint.mu.Unlock()
	if _, exists := proxy.checkpoint.wsConn[message.ID]; exists {
		return
	}
	proxy.checkpoint.wsConn[message.ID] = message.ConnectionID
	proxy.checkpoint.order = append(proxy.checkpoint.order, message.ID)
}

func (proxy *Proxy) forgetCheckpoint(id uuid.UUID) {
	if proxy.checkpoint == nil {
		return
	}
	proxy.checkpoint.mu.Lock()
	defer proxy.checkpoint.mu.Unlock()
	delete(proxy.checkpoint.wsConn, id)
	proxy.checkpoint.order = removeCheckpointID(proxy.checkpoint.order, id)
}

func (proxy *Proxy) cancelWebSocketCheckpoint(connectionID uuid.UUID) {
	if proxy.WebSocketInterceptor != nil {
		proxy.WebSocketInterceptor.CancelConnection(connectionID)
	}
	if proxy.checkpoint == nil {
		return
	}
	proxy.checkpoint.mu.Lock()
	defer proxy.checkpoint.mu.Unlock()
	for messageID, heldConnectionID := range proxy.checkpoint.wsConn {
		if heldConnectionID != connectionID {
			continue
		}
		delete(proxy.checkpoint.wsConn, messageID)
		proxy.checkpoint.order = removeCheckpointID(proxy.checkpoint.order, messageID)
	}
}
