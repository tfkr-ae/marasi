package marasi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"
	"time"

	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

func startRequestHold(t *testing.T, proxy *Proxy) (*http.Request, uuid.UUID, <-chan error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
	_, remove, err := martian.TestContext(req, nil, nil)
	if err != nil {
		t.Fatalf("applying martian context : %v", err)
	}
	t.Cleanup(remove)
	if err := SetupRequestModifier(proxy, req); err != nil {
		t.Fatalf("running SetupRequestModifier : %v", err)
	}
	reqID, ok := core.RequestIDFromContext(req.Context())
	if !ok {
		t.Fatalf("request id missing")
	}
	done := make(chan error, 1)
	go func() {
		done <- CheckpointRequestModifier(proxy, req)
	}()
	return req, reqID, done
}

func startResponseHold(t *testing.T, proxy *Proxy) (uuid.UUID, <-chan error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
	_, remove, err := martian.TestContext(req, nil, nil)
	if err != nil {
		t.Fatalf("applying martian context : %v", err)
	}
	t.Cleanup(remove)
	if err := SetupRequestModifier(proxy, req); err != nil {
		t.Fatalf("running SetupRequestModifier : %v", err)
	}
	reqID, ok := core.RequestIDFromContext(req.Context())
	if !ok {
		t.Fatalf("request id missing")
	}
	res := &http.Response{
		Header:  make(http.Header),
		Request: req,
	}
	done := make(chan error, 1)
	go func() {
		done <- CheckpointResponseModifier(proxy, res)
	}()
	return reqID, done
}

func TestCheckpointPending(t *testing.T) {
	t.Run("should resolve any pending id not only the head", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.SetIntercept(true)

		_, firstID, firstDone := startRequestHold(t, proxy)
		waitForCheckpoint(t, proxy, 1)
		_, secondID, secondDone := startRequestHold(t, proxy)
		waitForCheckpoint(t, proxy, 2)

		if err := proxy.DropCheckpoint(secondID); err != nil {
			t.Fatalf("dropping second checkpoint: %v", err)
		}
		if err := receiveHoldResult(t, secondDone); !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}
		items := proxy.CheckpointItems()
		if len(items) != 1 || items[0].ID != firstID {
			t.Fatalf("wanted remaining id %s\ngot: %v", firstID, items)
		}
		select {
		case err := <-firstDone:
			t.Fatalf("wanted first hold to remain blocked\ngot: %v", err)
		default:
		}
		if err := proxy.DropCheckpoint(firstID); err != nil {
			t.Fatalf("dropping first checkpoint: %v", err)
		}
		if err := receiveHoldResult(t, firstDone); !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}
	})

	t.Run("should list http and websocket items in hold order", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.SetIntercept(true)

		olderID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating older id: %v", err)
		}
		_, requestID, requestDone := startRequestHold(t, proxy)
		waitForCheckpoint(t, proxy, 1)

		messageID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating websocket id: %v", err)
		}
		message := &marasiws.Message{
			ID:           messageID,
			ConnectionID: uuid.New(),
			Frame:        marasiws.Frame{Opcode: marasiws.OpText, Payload: []byte("held")},
		}
		wsDone := make(chan error, 1)
		go func() {
			proxy.noteWebSocketHold(message)
			wsDone <- proxy.WebSocketInterceptor.Intercept(message, nil)
		}()
		waitForCheckpoint(t, proxy, 2)

		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		t.Cleanup(remove)
		if err := SetupRequestModifier(proxy, req); err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}
		*req = *core.ContextWithRequestID(req, olderID)
		res := &http.Response{Header: make(http.Header), Request: req}
		responseDone := make(chan error, 1)
		go func() {
			responseDone <- CheckpointResponseModifier(proxy, res)
		}()
		items := waitForCheckpoint(t, proxy, 3)
		responseID := olderID
		if items[0].ID != requestID || items[0].Type != domain.CheckpointTypeRequest {
			t.Fatalf("wanted first item request %s\ngot: %s %s", requestID, items[0].Type, items[0].ID)
		}
		if items[1].ID != message.ID || items[1].Type != domain.CheckpointTypeWebSocket {
			t.Fatalf("wanted second item websocket %s\ngot: %s %s", message.ID, items[1].Type, items[1].ID)
		}
		if items[2].ID != responseID || items[2].Type != domain.CheckpointTypeResponse {
			t.Fatalf("wanted third item response %s\ngot: %s %s", responseID, items[2].Type, items[2].ID)
		}

		proxy.DropAllCheckpoint()
		if err := receiveHoldResult(t, requestDone); !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted request drop: %v\ngot: %v", ErrDropped, err)
		}
		if err := receiveHoldResult(t, responseDone); !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted response drop: %v\ngot: %v", ErrDropped, err)
		}
		if err := receiveHoldResult(t, wsDone); err != nil {
			t.Fatalf("wanted websocket drop: nil\ngot: %v", err)
		}
		if proxy.HasPendingCheckpoint() {
			t.Fatalf("expected no pending checkpoint items")
		}
	})

	t.Run("should reject intercept_response on a response item", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.SetIntercept(true)
		responseID, done := startResponseHold(t, proxy)
		waitForCheckpoint(t, proxy, 1)

		err := proxy.ForwardCheckpoint(responseID, CheckpointForward{InterceptResponse: true})
		if !errors.Is(err, ErrInterceptResponseNotAllowed) {
			t.Fatalf("wanted: %v\ngot: %v", ErrInterceptResponseNotAllowed, err)
		}
		if !proxy.HasPendingCheckpoint() {
			t.Fatalf("wanted response to stay pending")
		}
		if err := proxy.DropCheckpoint(responseID); err != nil {
			t.Fatalf("dropping response: %v", err)
		}
		if err := receiveHoldResult(t, done); !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}
	})

	t.Run("should set intercept without toggling websocket intercept", func(t *testing.T) {
		proxy := newTestProxy(t)
		proxy.SetIntercept(true)
		proxy.SetIntercept(true)
		if !proxy.GetIntercept() {
			t.Fatalf("wanted http intercept: true")
		}
		if proxy.GetWebSocketIntercept() {
			t.Fatalf("wanted websocket intercept: false")
		}
		proxy.SetWebSocketIntercept(true)
		if !proxy.GetIntercept() || !proxy.GetWebSocketIntercept() {
			t.Fatalf("wanted both flags true")
		}
		proxy.SetIntercept(false)
		if proxy.GetIntercept() {
			t.Fatalf("wanted http intercept: false")
		}
		if !proxy.GetWebSocketIntercept() {
			t.Fatalf("wanted websocket intercept: true")
		}
	})

	t.Run("should hold the matching response after intercept_response forward", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		updateExtension(t, proxy, "checkpoint", `
			function interceptRequest(request)
				return true
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		t.Cleanup(remove)
		if err := SetupRequestModifier(proxy, req); err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}
		forwardOnHold(proxy, CheckpointForward{InterceptResponse: true})
		if err := CheckpointRequestModifier(proxy, req); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		res := &http.Response{Header: make(http.Header), Request: req}
		done := make(chan error, 1)
		go func() {
			done <- CheckpointResponseModifier(proxy, res)
		}()
		items := waitForCheckpoint(t, proxy, 1)
		if items[0].Type != domain.CheckpointTypeResponse {
			t.Fatalf("wanted: %s\ngot: %s", domain.CheckpointTypeResponse, items[0].Type)
		}
		reqID, ok := core.RequestIDFromContext(req.Context())
		if !ok || items[0].ID != reqID {
			t.Fatalf("wanted response id %s\ngot: %s", reqID, items[0].ID)
		}
		if err := proxy.DropCheckpoint(items[0].ID); err != nil {
			t.Fatalf("dropping response: %v", err)
		}
		if err := receiveHoldResult(t, done); !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}
	})

	t.Run("should apply websocket payload and opcode on forward", func(t *testing.T) {
		proxy := newTestProxy(t)
		messageID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating message id: %v", err)
		}
		message := &marasiws.Message{
			ID:           messageID,
			ConnectionID: uuid.New(),
			Frame:        marasiws.Frame{Opcode: marasiws.OpText, Payload: []byte("original")},
		}
		done := make(chan error, 1)
		go func() {
			proxy.noteWebSocketHold(message)
			done <- proxy.WebSocketInterceptor.Intercept(message, nil)
		}()
		waitForCheckpoint(t, proxy, 1)
		payload := []byte("edited")
		opcode := marasiws.OpBinary
		if err := proxy.ForwardCheckpoint(messageID, CheckpointForward{Payload: &payload, Opcode: &opcode}); err != nil {
			t.Fatalf("forwarding websocket: %v", err)
		}
		if err := receiveHoldResult(t, done); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if message.Frame.Opcode != marasiws.OpBinary {
			t.Fatalf("wanted opcode: %d\ngot: %d", marasiws.OpBinary, message.Frame.Opcode)
		}
		if string(message.Frame.Payload) != "edited" {
			t.Fatalf("wanted payload: %q\ngot: %q", "edited", message.Frame.Payload)
		}
	})

	t.Run("should not rebuild a dropped request", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.SetIntercept(true)
		req, reqID, done := startRequestHold(t, proxy)
		waitForCheckpoint(t, proxy, 1)
		if !proxy.HasPendingCheckpoint() {
			t.Fatalf("wanted http pending to count")
		}
		before, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request: %v", err)
		}
		if err := proxy.DropCheckpoint(reqID); err != nil {
			t.Fatalf("dropping request: %v", err)
		}
		if err := receiveHoldResult(t, done); !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}
		after, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request after drop: %v", err)
		}
		if string(after) != string(before) {
			t.Fatalf("wanted dump unchanged\ngot:\n%q", after)
		}
	})

	t.Run("should treat websocket pending as project busy", func(t *testing.T) {
		proxy := newTestProxy(t)
		messageID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating message id: %v", err)
		}
		message := &marasiws.Message{ID: messageID, ConnectionID: uuid.New()}
		done := make(chan error, 1)
		go func() {
			done <- proxy.WebSocketInterceptor.Intercept(message, nil)
		}()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && !proxy.HasPendingCheckpoint() {
			time.Sleep(time.Millisecond)
		}
		if !proxy.HasPendingCheckpoint() {
			t.Fatalf("wanted websocket pending to count")
		}
		if err := proxy.DropCheckpoint(message.ID); err != nil {
			t.Fatalf("dropping websocket: %v", err)
		}
		if err := receiveHoldResult(t, done); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
	})
}
