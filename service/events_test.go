package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestServiceEvents(t *testing.T) {
	t.Run("should connect and flush an event stream", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, _ := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()
	})

	t.Run("should accept explicit broad and missing accept headers", func(t *testing.T) {
		for _, accept := range []string{"text/event-stream", "*/*", ""} {
			server := newTestServer(nil, func() {})
			httpServer := httptest.NewServer(server)
			request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/events", nil)
			if err != nil {
				t.Fatalf("creating request: %v", err)
			}
			if accept != "" {
				request.Header.Set("Accept", accept)
			}

			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("connecting with Accept %q: %v", accept, err)
			}
			if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
				response.Body.Close()
				httpServer.Close()
				t.Fatalf("\nAccept %q wanted:\ntext/event-stream\ngot:\n%s", accept, got)
			}
			response.Body.Close()
			httpServer.Close()
		}
	})

	t.Run("should reject non-GET methods", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		for _, method := range []string{http.MethodHead, http.MethodPost} {
			request, err := http.NewRequest(method, httpServer.URL+"/events", nil)
			if err != nil {
				t.Fatalf("creating %s request: %v", method, err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("sending %s request: %v", method, err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("\n%s wanted:\n%d\ngot:\n%d", method, http.StatusMethodNotAllowed, response.StatusCode)
			}
		}
	})

	t.Run("should frame publications without an SSE id", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()

		server.events.publish("traffic.request", struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		}{ID: "0193802f-f0e7-73d9-a764-06d21e367809", Path: "/a?b=c"})

		if got := readEventFrame(t, reader); got != "event: traffic.request\ndata: {\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"path\":\"/a?b=c\"}\n\n" {
			t.Fatalf("\nwanted exact event frame without id field\ngot:\n%s", got)
		}
	})

	t.Run("should flush heartbeat comments without waiting fifteen seconds", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		server.heartbeatInterval = time.Millisecond
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()

		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat: %v", err)
		}
		blank, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat terminator: %v", err)
		}
		if got := line + blank; got != ": heartbeat\n\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", ": heartbeat\\n\\n", got)
		}
	})

	t.Run("should wait for an idle interval after a publication before heartbeating", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		server.heartbeatInterval = 80 * time.Millisecond
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()

		time.Sleep(50 * time.Millisecond)
		server.events.publish("traffic.request", map[string]string{"path": "/active"})
		if got := readEventFrame(t, reader); got != "event: traffic.request\ndata: {\"path\":\"/active\"}\n\n" {
			t.Fatalf("\nwanted publication before heartbeat\ngot:\n%s", got)
		}

		heartbeat := make(chan string, 1)
		go func() {
			line, _ := reader.ReadString('\n')
			blank, _ := reader.ReadString('\n')
			heartbeat <- line + blank
		}()
		select {
		case got := <-heartbeat:
			t.Fatalf("\nwanted:\nno heartbeat during the new idle interval\ngot:\n%s", got)
		case <-time.After(50 * time.Millisecond):
		}
		select {
		case got := <-heartbeat:
			if got != ": heartbeat\n\n" {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", ": heartbeat\\n\\n", got)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("heartbeat did not arrive after the idle interval")
		}
	})

	t.Run("should send publications in order to concurrent subscribers", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		firstResponse, first := connectEventStream(t, httpServer.URL)
		defer firstResponse.Body.Close()
		secondResponse, second := connectEventStream(t, httpServer.URL)
		defer secondResponse.Body.Close()

		server.events.publish("first", map[string]string{"value": "one"})
		server.events.publish("second", map[string]string{"value": "two"})

		wantFirst := "event: first\ndata: {\"value\":\"one\"}\n\n"
		wantSecond := "event: second\ndata: {\"value\":\"two\"}\n\n"
		for name, reader := range map[string]*bufio.Reader{"first subscriber": first, "second subscriber": second} {
			if got := readEventFrame(t, reader); got != wantFirst {
				t.Fatalf("\n%s wanted:\n%s\ngot:\n%s", name, wantFirst, got)
			}
			if got := readEventFrame(t, reader); got != wantSecond {
				t.Fatalf("\n%s wanted:\n%s\ngot:\n%s", name, wantSecond, got)
			}
		}
	})

	t.Run("should remove a subscriber when its request is cancelled", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, _ := connectEventStream(t, httpServer.URL)

		if err := response.Body.Close(); err != nil {
			t.Fatalf("closing event stream: %v", err)
		}
		waitForSubscriberCount(t, server.events, 0)
	})

	t.Run("should close streams without a final event during service shutdown", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()

		stopResponse, err := http.Post(httpServer.URL+"/service/stop", "", nil)
		if err != nil {
			t.Fatalf("stopping service: %v", err)
		}
		stopResponse.Body.Close()

		if _, err := reader.ReadString('\n'); err == nil {
			t.Fatal("\nwanted:\nclosed stream\ngot:\nmore stream data")
		}
	})

	t.Run("should publish request and response handler values on the event stream", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")

		if err := server.HandleRequest(domain.ProxyRequest{
			ID:          id,
			Scheme:      "https",
			Method:      "GET",
			Host:        "example.com",
			Path:        "/a?b=c",
			Raw:         []byte("raw request"),
			Metadata:    map[string]any{"foo": "bar", "prettified-request": "pretty request", "prettified-response": "pretty response"},
			RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		}); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := server.HandleResponse(domain.ProxyResponse{
			ID:          id,
			Status:      "200 OK",
			StatusCode:  200,
			ContentType: "application/json",
			Length:      "12",
			Raw:         []byte("raw response"),
			Metadata:    map[string]any{"foo": "bar", "prettified-request": "pretty request", "prettified-response": "pretty response"},
			RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
		}); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got := readEventFrame(t, reader); got != "event: traffic.request\ndata: {\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a?b=c\",\"metadata\":{\"foo\":\"bar\"},\"requested_at\":\"2026-01-02T03:04:05Z\"}\n\n" {
			t.Fatalf("\nwanted exact traffic.request frame\ngot:\n%s", got)
		}
		if got := readEventFrame(t, reader); got != "event: traffic.response\ndata: {\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"application/json\",\"length\":\"12\",\"metadata\":{\"foo\":\"bar\"},\"responded_at\":\"2026-01-02T03:04:06Z\"}\n\n" {
			t.Fatalf("\nwanted exact traffic.response frame\ngot:\n%s", got)
		}
	})

	t.Run("should return nil from handlers when a subscriber queue fills", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, _ := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()

		done := make(chan error, 1)
		go func() {
			for range eventQueueSize + 1 {
				if err := server.HandleRequest(domain.ProxyRequest{Path: "/overflow"}); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nhandlers to return while the subscriber is not reading\ngot:\nblocked publication")
		}
	})
}

func TestListenerEvents(t *testing.T) {
	t.Run("should not replay normal service startup", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting initial listener: %v", err)
		}
		server := NewServer(nil, lifecycle, func() {}, "dev", "default", "scratchpad")
		server.heartbeatInterval = time.Millisecond
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()

		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat after startup: %v", err)
		}
		blank, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat terminator: %v", err)
		}
		if line+blank != ": heartbeat\n\n" {
			t.Fatalf("\nwanted:\nno startup event\ngot:\n%s", line+blank)
		}
	})

	t.Run("should publish completed state changes in order and keep no-ops silent", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, func() {}, "dev", "default", "scratchpad")
		server.heartbeatInterval = 500 * time.Millisecond
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()

		startedResponse := requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		if startedResponse.Code != http.StatusOK {
			t.Fatalf("starting listener: %d %s", startedResponse.Code, startedResponse.Body.String())
		}
		var started ListenerStatus
		if err := json.Unmarshal(startedResponse.Body.Bytes(), &started); err != nil {
			t.Fatalf("decoding listener start: %v", err)
		}
		address := statusAddress(t, started)
		requestListener(t, server, http.MethodPost, "/listener/update", `{"address":"127.0.0.1"}`)
		requestListener(t, server, http.MethodPost, "/listener/start", "")
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("occupying listener endpoint: %v", err)
		}
		defer occupied.Close()
		requestListener(t, server, http.MethodPost, "/listener/update", fmt.Sprintf(`{"port":%d}`, occupied.Addr().(*net.TCPAddr).Port))
		updatedResponse := requestListener(t, server, http.MethodPost, "/listener/update", `{"port":0}`)
		var updated ListenerStatus
		if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updated); err != nil {
			t.Fatalf("decoding listener update: %v", err)
		}
		requestListener(t, server, http.MethodPost, "/listener/stop", "")
		requestListener(t, server, http.MethodPost, "/listener/stop", "")

		want := []string{
			fmt.Sprintf("event: listener.started\ndata: {\"status\":\"active\",\"proxy_listener\":%q}\n\n", address),
			fmt.Sprintf("event: listener.updated\ndata: {\"status\":\"active\",\"proxy_listener\":%q}\n\n", statusAddress(t, updated)),
			"event: listener.stopped\ndata: {\"status\":\"inactive\",\"proxy_listener\":null,\"reason\":\"requested\"}\n\n",
		}
		for _, expected := range want {
			if got := readEventFrame(t, reader); got != expected {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", expected, got)
			}
		}
		heartbeat, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat after listener events: %v", err)
		}
		blank, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat terminator: %v", err)
		}
		if heartbeat+blank != ": heartbeat\n\n" {
			t.Fatalf("\nwanted:\nno extra listener event\ngot:\n%s", heartbeat+blank)
		}
	})

	t.Run("should publish requested stop when cleanup fails", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, func() {}, "dev", "default", "scratchpad")
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()
		requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		readEventFrame(t, reader)
		proxy.cleanupErr = errors.New("flush failed")

		response := requestListener(t, server, http.MethodPost, "/listener/stop", "")
		assertListenerError(t, response, http.StatusInternalServerError, "listener_cleanup_failed")
		want := "event: listener.stopped\ndata: {\"status\":\"inactive\",\"proxy_listener\":null,\"reason\":\"requested\"}\n\n"
		if got := readEventFrame(t, reader); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should publish one updated event when replacement cleanup fails", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, func() {}, "dev", "default", "scratchpad")
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()
		requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		readEventFrame(t, reader)
		proxy.cleanupErr = errors.New("flush failed")

		response := requestListener(t, server, http.MethodPost, "/listener/update", `{"port":0}`)
		assertListenerError(t, response, http.StatusInternalServerError, "listener_cleanup_failed")
		status := lifecycle.Status()
		want := fmt.Sprintf("event: listener.updated\ndata: {\"status\":\"active\",\"proxy_listener\":%q}\n\n", statusAddress(t, status))
		if got := readEventFrame(t, reader); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should publish failed stop after an unexpected serve failure", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, func() {}, "dev", "default", "scratchpad")
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		readEventFrame(t, reader)
		served := <-proxy.served
		if err := served.Close(); err != nil {
			t.Fatalf("failing listener: %v", err)
		}

		want := "event: listener.stopped\ndata: {\"status\":\"inactive\",\"proxy_listener\":null,\"reason\":\"failed\"}\n\n"
		if got := readEventFrame(t, reader); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should preserve an unexpected failure that races with replacement", func(t *testing.T) {
		proxy := newListenerTestProxy()
		secondBind := make(chan struct{})
		releaseBind := make(chan struct{})
		bindCalls := 0
		proxy.bind = func(address, port string) (net.Listener, error) {
			bindCalls++
			if bindCalls == 2 {
				close(secondBind)
				<-releaseBind
			}
			return net.Listen("tcp", net.JoinHostPort(address, port))
		}
		firstServeEnded := make(chan struct{})
		var firstServe sync.Once
		proxy.serve = func(listener net.Listener) error {
			_, err := listener.Accept()
			firstServe.Do(func() { close(firstServeEnded) })
			return err
		}
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, func() {}, "dev", "default", "scratchpad")
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()
		requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		readEventFrame(t, reader)
		old := <-proxy.served
		updateDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			updateDone <- requestListener(t, server, http.MethodPost, "/listener/update", `{"port":0}`)
		}()
		<-secondBind
		if err := old.Close(); err != nil {
			t.Fatalf("failing old listener: %v", err)
		}
		<-firstServeEnded
		close(releaseBind)
		response := <-updateDone
		if response.Code != http.StatusOK {
			t.Fatalf("updating after serving failure: %d %s", response.Code, response.Body.String())
		}

		failed := "event: listener.stopped\ndata: {\"status\":\"inactive\",\"proxy_listener\":null,\"reason\":\"failed\"}\n\n"
		if got := readEventFrame(t, reader); got != failed {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", failed, got)
		}
		updated := lifecycle.Status()
		wantUpdated := fmt.Sprintf("event: listener.updated\ndata: {\"status\":\"active\",\"proxy_listener\":%q}\n\n", statusAddress(t, updated))
		if got := readEventFrame(t, reader); got != wantUpdated {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantUpdated, got)
		}
	})
}

func connectEventStream(t *testing.T, url string) (*http.Response, *bufio.Reader) {
	t.Helper()
	response, err := http.Get(url + "/events")
	if err != nil {
		t.Fatalf("connecting to events: %v", err)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		response.Body.Close()
		t.Fatalf("\nwanted:\ntext/event-stream\ngot:\n%s", got)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		response.Body.Close()
		t.Fatalf("reading connected comment: %v", err)
	}
	blank, err := reader.ReadString('\n')
	if err != nil {
		response.Body.Close()
		t.Fatalf("reading connected comment terminator: %v", err)
	}
	if got := line + blank; got != ": connected\n\n" {
		response.Body.Close()
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", ": connected\\n\\n", got)
	}
	return response, reader
}

func readEventFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var frame string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading event frame: %v", err)
		}
		frame += line
		if line == "\n" {
			return frame
		}
	}
}

func waitForSubscriberCount(t *testing.T, broadcaster *eventBroadcaster, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		broadcaster.mu.Lock()
		got := len(broadcaster.subscribers)
		broadcaster.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("subscriber count did not reach %d", want)
}

func TestEventBroadcaster(t *testing.T) {
	t.Run("should publish exact request and response events", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		subscriber := broadcaster.subscribe()
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")

		broadcaster.publishRequest(domain.ProxyRequest{
			ID:          id,
			Scheme:      "https",
			Method:      "GET",
			Host:        "example.com",
			Path:        "/a?b=c",
			Raw:         []byte("raw request"),
			Metadata:    map[string]any{"foo": "bar", "prettified-request": "pretty request", "prettified-response": "pretty response"},
			RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		})
		broadcaster.publishResponse(domain.ProxyResponse{
			ID:          id,
			Status:      "200 OK",
			StatusCode:  200,
			ContentType: "application/json",
			Length:      "12",
			Raw:         []byte("raw response"),
			Metadata:    map[string]any{"foo": "bar", "prettified-request": "pretty request", "prettified-response": "pretty response"},
			RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
		})

		requestEvent := <-subscriber.events
		if requestEvent.name != "traffic.request" {
			t.Fatalf("\nwanted:\ntraffic.request\ngot:\n%s", requestEvent.name)
		}
		wantRequest := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","scheme":"https","method":"GET","host":"example.com","path":"/a?b=c","metadata":{"foo":"bar"},"requested_at":"2026-01-02T03:04:05Z"}`
		if got := string(requestEvent.data); got != wantRequest {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantRequest, got)
		}

		responseEvent := <-subscriber.events
		if responseEvent.name != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.response\ngot:\n%s", responseEvent.name)
		}
		wantResponse := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","status":"200 OK","status_code":200,"content_type":"application/json","length":"12","metadata":{"foo":"bar"},"responded_at":"2026-01-02T03:04:06Z"}`
		if got := string(responseEvent.data); got != wantResponse {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantResponse, got)
		}
	})

	t.Run("should send one encoding to multiple subscribers in publication order", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		first := broadcaster.subscribe()
		second := broadcaster.subscribe()

		broadcaster.publishRequest(domain.ProxyRequest{Path: "/first"})
		broadcaster.publishResponse(domain.ProxyResponse{Status: "second"})

		firstRequest := <-first.events
		firstResponse := <-first.events
		secondRequest := <-second.events
		secondResponse := <-second.events
		if firstRequest.name != "traffic.request" || firstResponse.name != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.request then traffic.response\ngot:\n%s then %s", firstRequest.name, firstResponse.name)
		}
		if secondRequest.name != "traffic.request" || secondResponse.name != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.request then traffic.response\ngot:\n%s then %s", secondRequest.name, secondResponse.name)
		}
		if &firstRequest.data[0] != &secondRequest.data[0] {
			t.Fatal("\nwanted:\nshared encoded request bytes\ngot:\ndifferent byte slices")
		}
		if &firstResponse.data[0] != &secondResponse.data[0] {
			t.Fatal("\nwanted:\nshared encoded response bytes\ngot:\ndifferent byte slices")
		}
	})

	t.Run("should remove an unsubscribed subscriber", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		subscriber := broadcaster.subscribe()

		broadcaster.unsubscribe(subscriber)
		broadcaster.unsubscribe(subscriber)
		broadcaster.publishRequest(domain.ProxyRequest{})

		select {
		case <-subscriber.done:
		default:
			t.Fatal("\nwanted:\nsubscriber closed\ngot:\nsubscriber open")
		}
		if got := len(broadcaster.subscribers); got != 0 {
			t.Fatalf("\nwanted:\n0 subscribers\ngot:\n%d", got)
		}
		if got := len(subscriber.events); got != 0 {
			t.Fatalf("\nwanted:\n0 queued events\ngot:\n%d", got)
		}
		if _, open := <-subscriber.events; open {
			t.Fatal("\nwanted:\nsubscriber queue closed\ngot:\nsubscriber queue open")
		}
	})

	t.Run("should disconnect only a subscriber whose queue overflows", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		slow := broadcaster.subscribe()
		healthy := broadcaster.subscribe()

		for index := range eventQueueSize + 1 {
			broadcaster.publishRequest(domain.ProxyRequest{Path: fmt.Sprintf("/%d", index)})
			select {
			case <-healthy.events:
			default:
				t.Fatalf("\nwanted:\nhealthy subscriber event %d\ngot:\nno event", index)
			}
		}

		select {
		case <-slow.done:
		default:
			t.Fatal("\nwanted:\noverflowed subscriber closed\ngot:\nsubscriber open")
		}
		select {
		case <-healthy.done:
			t.Fatal("\nwanted:\nhealthy subscriber open\ngot:\nsubscriber closed")
		default:
		}
		if got := len(broadcaster.subscribers); got != 1 {
			t.Fatalf("\nwanted:\n1 subscriber\ngot:\n%d", got)
		}

		broadcaster.publishResponse(domain.ProxyResponse{Status: "still connected"})
		if got := (<-healthy.events).name; got != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.response\ngot:\n%s", got)
		}
	})

	t.Run("should support concurrent publication subscription and cancellation", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		start := make(chan struct{})
		finished := make(chan struct{}, 3)

		go func() {
			<-start
			for range 100 {
				broadcaster.publishRequest(domain.ProxyRequest{})
			}
			finished <- struct{}{}
		}()
		for range 2 {
			go func() {
				<-start
				for range 100 {
					subscriber := broadcaster.subscribe()
					broadcaster.unsubscribe(subscriber)
				}
				finished <- struct{}{}
			}()
		}

		close(start)
		for range 3 {
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("concurrent broadcaster operation timed out")
			}
		}
	})
}
