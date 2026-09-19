package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

func TestCheckpointControlRoutes(t *testing.T) {
	t.Run("should list empty pending items with current flags", func(t *testing.T) {
		server, _ := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint", ""), http.StatusOK, `{"items":[],"intercept":false,"websocket_intercept":false}`+"\n")
		assertNoCheckpointEvent(t, subscriber)
	})

	t.Run("should list held http items in hold order with raw included", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		proxy.SetIntercept(true)
		firstReq, firstID, firstDone := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		secondReq, secondID, secondDone := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 2)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(firstID)
			_ = proxy.DropCheckpoint(secondID)
			drainCheckpointHold(t, firstDone)
			drainCheckpointHold(t, secondDone)
		})

		firstRaw := dumpCheckpointRequest(t, firstReq)
		secondRaw := dumpCheckpointRequest(t, secondReq)
		want := fmt.Sprintf(
			`{"items":[{"id":"%s","type":"request","raw":"%s"},{"id":"%s","type":"request","raw":"%s"}],"intercept":true,"websocket_intercept":false}`+"\n",
			firstID, firstRaw, secondID, secondRaw,
		)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint", ""), http.StatusOK, want)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint?extra=1", ""), http.StatusOK, want)
	})

	t.Run("should filter list by kind and reject an unknown kind", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		proxy.SetIntercept(true)
		req, reqID, reqDone := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		messageID, messageDone := startCheckpointWebSocketHold(t, proxy)
		waitForCheckpointItems(t, proxy, 2)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(reqID)
			_ = proxy.DropCheckpoint(messageID)
			drainCheckpointHold(t, reqDone)
			drainCheckpointHold(t, messageDone)
		})

		raw := dumpCheckpointRequest(t, req)
		httpBody := fmt.Sprintf(
			`{"items":[{"id":"%s","type":"request","raw":"%s"}],"intercept":true,"websocket_intercept":false}`+"\n",
			reqID, raw,
		)
		wsBody := fmt.Sprintf(
			`{"items":[{"id":"%s","type":"websocket","payload":"%s","opcode":1,"direction":"client","connection_id":"%s","request_id":"%s"}],"intercept":true,"websocket_intercept":false}`+"\n",
			messageID, base64.StdEncoding.EncodeToString([]byte("held")), uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809"), uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e36780a"),
		)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint?kind=http", ""), http.StatusOK, httpBody)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint?kind=websocket", ""), http.StatusOK, wsBody)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint?kind=other", ""), http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`+"\n")
	})

	t.Run("should get one item without flags", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		proxy.SetIntercept(true)
		req, reqID, done := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(reqID)
			drainCheckpointHold(t, done)
		})

		want := fmt.Sprintf(`{"id":"%s","type":"request","raw":"%s"}`+"\n", reqID, dumpCheckpointRequest(t, req))
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint/"+reqID.String(), ""), http.StatusOK, want)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint/not-a-uuid", ""), http.StatusBadRequest, `{"error":"bad_request"}`+"\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint/0193802f-f0e7-73d9-a764-06d21e367809", ""), http.StatusNotFound, `{"error":"not_found"}`+"\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint/intercept", ""), http.StatusBadRequest, `{"error":"bad_request"}`+"\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint/websocket-intercept", ""), http.StatusBadRequest, `{"error":"bad_request"}`+"\n")
	})

	t.Run("should forward originals and return the remaining list", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		_, reqID, done := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		drainCheckpointHeld(t, subscriber)

		response := requestControlAPI(server, http.MethodPost, "/checkpoint/"+reqID.String()+"/forward", `{}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":false}`+"\n")
		if err := receiveCheckpointHold(t, done); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		assertCheckpointEvent(t, subscriber, "checkpoint.forwarded", fmt.Sprintf(`{"id":"%s","type":"request"}`, reqID))
	})

	t.Run("should drop an item and ignore a second resolve", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		_, firstID, firstDone := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		drainCheckpointHeld(t, subscriber)
		secondReq, secondID, secondDone := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 2)
		drainCheckpointHeld(t, subscriber)

		wantRemaining := fmt.Sprintf(
			`{"items":[{"id":"%s","type":"request","raw":"%s"}],"intercept":true,"websocket_intercept":false}`+"\n",
			secondID, dumpCheckpointRequest(t, secondReq),
		)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+firstID.String()+"/drop", ""), http.StatusOK, wantRemaining)
		if err := receiveCheckpointHold(t, firstDone); !errors.Is(err, marasi.ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", marasi.ErrDropped, err)
		}
		assertCheckpointEvent(t, subscriber, "checkpoint.dropped", fmt.Sprintf(`{"id":"%s","type":"request"}`, firstID))
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+secondID.String()+"/drop", `{}`), http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":false}`+"\n")
		if err := receiveCheckpointHold(t, secondDone); !errors.Is(err, marasi.ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", marasi.ErrDropped, err)
		}
		assertCheckpointEvent(t, subscriber, "checkpoint.dropped", fmt.Sprintf(`{"id":"%s","type":"request"}`, secondID))
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+secondID.String()+"/drop", ""), http.StatusNotFound, `{"error":"not_found"}`+"\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+secondID.String()+"/forward", `{}`), http.StatusNotFound, `{"error":"not_found"}`+"\n")
		assertNoCheckpointEvent(t, subscriber)
	})

	t.Run("should reject invalid forward drop and flag bodies without events", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		_, reqID, done := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(reqID)
			drainCheckpointHold(t, done)
		})
		drainCheckpointHeld(t, subscriber)

		for _, body := range []string{
			`null`, `[]`, `{"raw":null}`, `{"raw":"@@@"}`, `{"payload":"YQ=="}`, `{"extra":true}`, `{} {}`,
		} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+reqID.String()+"/forward", body), http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`+"\n")
		}
		for _, body := range []string{`null`, `{"extra":true}`, `{"raw":"YQ=="}`, `{} {}`} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+reqID.String()+"/drop", body), http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`+"\n")
		}
		for _, body := range []string{``, `null`, `{}`, `{"intercept":null}`, `{"intercept":"true"}`, `{"intercept":true,"extra":true}`, `{"intercept":true} {}`} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/intercept", body), http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`+"\n")
		}
		assertNoCheckpointEvent(t, subscriber)
		if !proxy.HasPendingCheckpoint() {
			t.Fatal("invalid bodies resolved the pending item")
		}
	})

	t.Run("should accept intercept_response on a held request", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		_, reqID, done := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		drainCheckpointHeld(t, subscriber)

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+reqID.String()+"/forward", `{"intercept_response":true}`), http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":false}`+"\n")
		if err := receiveCheckpointHold(t, done); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		assertCheckpointEvent(t, subscriber, "checkpoint.forwarded", fmt.Sprintf(`{"id":"%s","type":"request"}`, reqID))
	})

	t.Run("should reject intercept_response on a response item", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		responseID, done := startCheckpointHTTPResponseHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(responseID)
			drainCheckpointHold(t, done)
		})
		drainCheckpointHeld(t, subscriber)

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+responseID.String()+"/forward", `{"intercept_response":true}`), http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`+"\n")
		assertNoCheckpointEvent(t, subscriber)
		if !proxy.HasPendingCheckpoint() {
			t.Fatal("wanted response to stay pending")
		}
	})

	t.Run("should set flags idempotently and emit updated only on change", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/intercept", `{"intercept":false}`), http.StatusOK, `{"items":[],"intercept":false,"websocket_intercept":false}`+"\n")
		assertNoCheckpointEvent(t, subscriber)

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/intercept", `{"intercept":true}`), http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":false}`+"\n")
		assertCheckpointEvent(t, subscriber, "checkpoint.updated", `{"intercept":true,"websocket_intercept":false}`)
		if !proxy.GetIntercept() || proxy.GetWebSocketIntercept() {
			t.Fatalf("wanted http intercept only, got http=%t websocket=%t", proxy.GetIntercept(), proxy.GetWebSocketIntercept())
		}

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/websocket-intercept", `{"websocket_intercept":true}`), http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":true}`+"\n")
		assertCheckpointEvent(t, subscriber, "checkpoint.updated", `{"intercept":true,"websocket_intercept":true}`)
		if !proxy.GetIntercept() || !proxy.GetWebSocketIntercept() {
			t.Fatal("wanted both flags true")
		}

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/websocket-intercept", `{"websocket_intercept":true}`), http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":true}`+"\n")
		assertNoCheckpointEvent(t, subscriber)
	})

	t.Run("should publish held as the get-by-id object", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)

		req, reqID, done := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(reqID)
			drainCheckpointHold(t, done)
		})

		want := fmt.Sprintf(`{"id":"%s","type":"request","raw":"%s"}`, reqID, dumpCheckpointRequest(t, req))
		waitCheckpointEvent(t, subscriber, "checkpoint.held", want)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint/"+reqID.String(), ""), http.StatusOK, want+"\n")
		assertNoCheckpointEvent(t, subscriber)
	})

	t.Run("should publish websocket held as the get-by-id object", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		messageID, done := startCheckpointWebSocketHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(messageID)
			drainCheckpointHold(t, done)
		})

		want := fmt.Sprintf(
			`{"id":"%s","type":"websocket","payload":"%s","opcode":1,"direction":"client","connection_id":"%s","request_id":"%s"}`,
			messageID, base64.StdEncoding.EncodeToString([]byte("held")),
			uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809"),
			uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e36780a"),
		)
		waitCheckpointEvent(t, subscriber, "checkpoint.held", want)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/checkpoint/"+messageID.String(), ""), http.StatusOK, want+"\n")
	})

	t.Run("should forward a websocket payload and opcode", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		messageID, done := startCheckpointWebSocketHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		drainCheckpointHeld(t, subscriber)

		payload := base64.StdEncoding.EncodeToString([]byte("edited"))
		body := fmt.Sprintf(`{"payload":"%s","opcode":2}`, payload)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+messageID.String()+"/forward", body), http.StatusOK, `{"items":[],"intercept":false,"websocket_intercept":false}`+"\n")
		if err := receiveCheckpointHold(t, done); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		assertCheckpointEvent(t, subscriber, "checkpoint.forwarded", fmt.Sprintf(`{"id":"%s","type":"websocket"}`, messageID))
	})

	t.Run("should reject intercept_response on a websocket item", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		messageID, done := startCheckpointWebSocketHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(messageID)
			drainCheckpointHold(t, done)
		})
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+messageID.String()+"/forward", `{"intercept_response":true}`), http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`+"\n")
	})

	t.Run("should rebuild http raw on forward and reject a rebuild failure", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		_, reqID, done := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		t.Cleanup(func() {
			_ = proxy.DropCheckpoint(reqID)
			drainCheckpointHold(t, done)
		})
		drainCheckpointHeld(t, subscriber)

		badRaw := base64.StdEncoding.EncodeToString([]byte("not an HTTP request"))
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+reqID.String()+"/forward", `{"raw":"`+badRaw+`"}`), http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`+"\n")
		assertNoCheckpointEvent(t, subscriber)
		if !proxy.HasPendingCheckpoint() {
			t.Fatal("rebuild failure resolved the pending item")
		}

		edited := base64.StdEncoding.EncodeToString([]byte("GET /edited HTTP/1.1\r\nHost: marasi.app\r\n\r\n"))
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/checkpoint/"+reqID.String()+"/forward", `{"raw":"`+edited+`"}`), http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":false}`+"\n")
		if err := receiveCheckpointHold(t, done); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		assertCheckpointEvent(t, subscriber, "checkpoint.forwarded", fmt.Sprintf(`{"id":"%s","type":"request"}`, reqID))
	})

	t.Run("should set intercept without waiting on the project write gate", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		current := canonicalProjectPath(t, filepath.Join(dir, "current.marasi"))
		if err := lifecycle.Open(context.Background(), current); err != nil {
			t.Fatalf("opening current project: %v", err)
		}
		server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "dev", "default", current)
		release, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting work: %v", err)
		}
		defer release()

		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			result <- requestControlAPI(server, http.MethodPost, "/checkpoint/intercept", `{"intercept":true}`)
		}()
		select {
		case response := <-result:
			assertControlAPIResponse(t, response, http.StatusOK, `{"items":[],"intercept":true,"websocket_intercept":false}`+"\n")
		case <-time.After(200 * time.Millisecond):
			t.Fatal("checkpoint flag post waited on the project write gate")
		}
	})
}

func TestCheckpointServiceStop(t *testing.T) {
	t.Run("should drop pending http and websocket items then emit dropped", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		_, reqID, reqDone := startCheckpointHTTPHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		drainCheckpointHeld(t, subscriber)
		messageID, messageDone := startCheckpointWebSocketHold(t, proxy)
		waitForCheckpointItems(t, proxy, 2)
		drainCheckpointHeld(t, subscriber)

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/service/stop", ""), http.StatusAccepted, `{"status":"shutdown_in_progress"}`+"\n")
		if err := receiveCheckpointHold(t, reqDone); !errors.Is(err, marasi.ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", marasi.ErrDropped, err)
		}
		if err := receiveCheckpointHold(t, messageDone); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		assertCheckpointEvent(t, subscriber, "checkpoint.dropped", fmt.Sprintf(`{"id":"%s","type":"request"}`, reqID))
		assertCheckpointEvent(t, subscriber, "checkpoint.dropped", fmt.Sprintf(`{"id":"%s","type":"websocket"}`, messageID))
		if proxy.HasPendingCheckpoint() {
			t.Fatal("wanted no pending checkpoint items after stop")
		}
	})

	t.Run("should not publish sse when websocket teardown cancels a hold", func(t *testing.T) {
		server, proxy := newCheckpointServer(t)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		_, done := startCheckpointWebSocketHold(t, proxy)
		waitForCheckpointItems(t, proxy, 1)
		drainCheckpointHeld(t, subscriber)

		proxy.WebSocketInterceptor.CancelConnection(uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809"))
		if err := receiveCheckpointHold(t, done); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		assertNoCheckpointEvent(t, subscriber)
	})
}

func TestCheckpointEventFrames(t *testing.T) {
	server, proxy := newCheckpointServer(t)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	stream, reader := connectEventStream(t, httpServer.URL)
	defer stream.Body.Close()

	proxy.SetIntercept(true)
	req, reqID, done := startCheckpointHTTPHold(t, proxy)
	waitForCheckpointItems(t, proxy, 1)
	wantHeld := fmt.Sprintf(
		"event: checkpoint.held\ndata: {\"id\":\"%s\",\"type\":\"request\",\"raw\":\"%s\"}\n\n",
		reqID, dumpCheckpointRequest(t, req),
	)
	if got := readEventFrame(t, reader); got != wantHeld {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantHeld, got)
	}

	response := sendCheckpointRequest(t, http.MethodPost, httpServer.URL+"/checkpoint/"+reqID.String()+"/forward", `{}`)
	response.Body.Close()
	wantForwarded := fmt.Sprintf("event: checkpoint.forwarded\ndata: {\"id\":\"%s\",\"type\":\"request\"}\n\n", reqID)
	if got := readEventFrame(t, reader); got != wantForwarded {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantForwarded, got)
	}
	if err := receiveCheckpointHold(t, done); err != nil {
		t.Fatalf("wanted: nil\ngot: %v", err)
	}

	_, dropID, dropDone := startCheckpointHTTPHold(t, proxy)
	waitForCheckpointItems(t, proxy, 1)
	if got := readEventFrame(t, reader); !strings.HasPrefix(got, "event: checkpoint.held\n") {
		t.Fatalf("\nwanted:\ncheckpoint.held frame\ngot:\n%s", got)
	}
	response = sendCheckpointRequest(t, http.MethodPost, httpServer.URL+"/checkpoint/"+dropID.String()+"/drop", "")
	response.Body.Close()
	wantDropped := fmt.Sprintf("event: checkpoint.dropped\ndata: {\"id\":\"%s\",\"type\":\"request\"}\n\n", dropID)
	if got := readEventFrame(t, reader); got != wantDropped {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantDropped, got)
	}
	if err := receiveCheckpointHold(t, dropDone); !errors.Is(err, marasi.ErrDropped) {
		t.Fatalf("wanted: %v\ngot: %v", marasi.ErrDropped, err)
	}

	response = sendCheckpointRequest(t, http.MethodPost, httpServer.URL+"/checkpoint/intercept", `{"intercept":false}`)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("reading intercept response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("setting intercept: %d %s", response.StatusCode, body)
	}
	wantUpdated := "event: checkpoint.updated\ndata: {\"intercept\":false,\"websocket_intercept\":false}\n\n"
	if got := readEventFrame(t, reader); got != wantUpdated {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantUpdated, got)
	}
}

func sendCheckpointRequest(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("creating checkpoint request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("sending checkpoint request: %v", err)
	}
	return response
}

func newCheckpointServer(t *testing.T) (*Server, *marasi.Proxy) {
	t.Helper()
	proxy, err := marasi.New()
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	ext := &domain.Extension{
		Name:    "checkpoint",
		ID:      uuid.MustParse("01937d13-9632-75b1-9e73-c5129b06fa8c"),
		Enabled: true,
		LuaContent: `
			function interceptRequest(request)
				return false
			end
			function interceptResponse(response)
				return false
			end
		`,
	}
	if err := proxy.WithOptions(marasi.WithExtension(ext)); err != nil {
		t.Fatalf("loading checkpoint: %v", err)
	}
	server := newTestServer(proxy, func() {})
	if err := proxy.WithOptions(
		marasi.WithInterceptHandler(server.HandleIntercept),
		marasi.WithWebSocketInterceptHandler(server.HandleWebSocketIntercept),
	); err != nil {
		t.Fatalf("installing checkpoint notify: %v", err)
	}
	return server, proxy
}

func startCheckpointHTTPHold(t *testing.T, proxy *marasi.Proxy) (*http.Request, uuid.UUID, <-chan error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
	_, remove, err := martian.TestContext(req, nil, nil)
	if err != nil {
		t.Fatalf("applying martian context: %v", err)
	}
	t.Cleanup(remove)
	if err := marasi.SetupRequestModifier(proxy, req); err != nil {
		t.Fatalf("running SetupRequestModifier: %v", err)
	}
	reqID, ok := core.RequestIDFromContext(req.Context())
	if !ok {
		t.Fatal("request id missing")
	}
	done := make(chan error, 1)
	go func() {
		done <- marasi.CheckpointRequestModifier(proxy, req)
	}()
	return req, reqID, done
}

func startCheckpointHTTPResponseHold(t *testing.T, proxy *marasi.Proxy) (uuid.UUID, <-chan error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
	_, remove, err := martian.TestContext(req, nil, nil)
	if err != nil {
		t.Fatalf("applying martian context: %v", err)
	}
	t.Cleanup(remove)
	if err := marasi.SetupRequestModifier(proxy, req); err != nil {
		t.Fatalf("running SetupRequestModifier: %v", err)
	}
	reqID, ok := core.RequestIDFromContext(req.Context())
	if !ok {
		t.Fatal("request id missing")
	}
	res := &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    req,
	}
	done := make(chan error, 1)
	go func() {
		done <- marasi.CheckpointResponseModifier(proxy, res)
	}()
	return reqID, done
}

func startCheckpointWebSocketHold(t *testing.T, proxy *marasi.Proxy) (uuid.UUID, <-chan error) {
	t.Helper()
	messageID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e36780b")
	message := &marasiws.Message{
		ID:           messageID,
		ConnectionID: uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809"),
		RequestID:    uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e36780a"),
		Direction:    marasiws.DirectionFromClient,
		Frame:        marasiws.Frame{Opcode: marasiws.OpText, Payload: []byte("held")},
	}
	done := make(chan error, 1)
	go func() {
		done <- proxy.WebSocketInterceptor.Intercept(message, func(snapshot domain.WebSocketMessage) {
			if proxy.OnWebSocketIntercept == nil {
				return
			}
			_ = proxy.OnWebSocketIntercept(snapshot)
		})
	}()
	return messageID, done
}

func waitForCheckpointItems(t *testing.T, proxy *marasi.Proxy, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(proxy.CheckpointItems()) == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d checkpoint items, got %d", n, len(proxy.CheckpointItems()))
}

func receiveCheckpointHold(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for checkpoint hold")
		return nil
	}
}

func drainCheckpointHold(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case <-result:
	case <-time.After(2 * time.Second):
	}
}

func dumpCheckpointRequest(t *testing.T, req *http.Request) string {
	t.Helper()
	raw, err := httputil.DumpRequest(req, true)
	if err != nil {
		t.Fatalf("dumping request: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func drainCheckpointHeld(t *testing.T, subscriber *eventSubscriber) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		if event.name != "checkpoint.held" {
			t.Fatalf("wanted held event, got %s %s", event.name, event.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for checkpoint.held")
	}
}

func waitCheckpointEvent(t *testing.T, subscriber *eventSubscriber, name, data string) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		if event.name != name || string(event.data) != data {
			t.Fatalf("\nwanted:\n%s %s\ngot:\n%s %s", name, data, event.name, event.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("\nwanted:\n%s event\ngot:\ntimeout", name)
	}
}

func assertCheckpointEvent(t *testing.T, subscriber *eventSubscriber, name, data string) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		if event.name != name || string(event.data) != data {
			t.Fatalf("\nwanted:\n%s %s\ngot:\n%s %s", name, data, event.name, event.data)
		}
	default:
		t.Fatalf("\nwanted:\n%s event\ngot:\nno event", name)
	}
}

func assertNoCheckpointEvent(t *testing.T, subscriber *eventSubscriber) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		t.Fatalf("\nwanted:\nno event\ngot:\n%s %s", event.name, event.data)
	default:
	}
}
