package service

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/db"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

func TestWebSocketConnectionControlRoutesAndEvents(t *testing.T) {
	control, originURL, proxyAddress := newWebSocketControlFixture(t)
	stream, events := connectEventStream(t, control.URL)
	defer stream.Body.Close()

	connection, request := upgradeWebSocketThroughProxy(t, proxyAddress, originURL)
	defer connection.Close()
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		t.Fatalf("reading websocket upgrade response: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		response.Body.Close()
		t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusSwitchingProtocols, response.StatusCode)
	}

	openedData := readWebSocketEventData(t, events, "websocket.opened")
	var opened struct {
		ID        uuid.UUID `json:"id"`
		RequestID uuid.UUID `json:"request_id"`
		State     string    `json:"state"`
	}
	if err := json.Unmarshal([]byte(openedData), &opened); err != nil {
		t.Fatalf("decoding websocket.opened: %v", err)
	}
	if opened.ID == uuid.Nil || opened.RequestID == uuid.Nil || opened.State != "open" {
		t.Fatalf("\nwanted:\nconnection and request IDs with state open\ngot:\n%s", openedData)
	}

	connectionPath := "/websocket/" + opened.ID.String()
	openedBody := waitForWebSocketState(t, control.URL+connectionPath, "open")
	if got := string(openedBody); got != openedData+"\n" {
		t.Fatalf("\nwanted event and get to have the same connection JSON:\n%s\ngot:\n%s", openedData+"\n", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(openedBody, &fields); err != nil {
		t.Fatalf("decoding websocket connection: %v", err)
	}
	for _, name := range []string{"id", "request_id", "state", "transport", "host", "path", "started_at", "closed_at", "close_code", "close_reason"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("\nwanted:\n%s field\ngot:\n%s", name, openedBody)
		}
	}
	if _, ok := fields["connection_id"]; ok {
		t.Fatalf("\nwanted:\nno connection_id field\ngot:\n%s", openedBody)
	}
	if string(fields["closed_at"]) != "null" {
		t.Fatalf("\nwanted:\nclosed_at null\ngot:\n%s", fields["closed_at"])
	}
	var startedAt string
	if err := json.Unmarshal(fields["started_at"], &startedAt); err != nil {
		t.Fatalf("decoding started_at: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, startedAt); err != nil {
		t.Fatalf("started_at is not RFC3339: %s", startedAt)
	}

	assertWebSocketControlResponse(t, control.URL+"/traffic/"+opened.RequestID.String()+"/websocket", http.StatusOK, openedBody)
	assertWebSocketControlResponse(t, control.URL+"/websocket/"+opened.RequestID.String(), http.StatusNotFound, []byte("{\"error\":\"not_found\"}\n"))
	assertWebSocketControlResponse(t, control.URL+"/traffic/"+opened.ID.String()+"/websocket", http.StatusNotFound, []byte("{\"error\":\"not_found\"}\n"))
	assertWebSocketControlResponse(t, control.URL+"/websocket/not-a-uuid", http.StatusBadRequest, []byte("{\"error\":\"bad_request\"}\n"))
	assertWebSocketControlResponse(t, control.URL+"/traffic/not-a-uuid/websocket", http.StatusBadRequest, []byte("{\"error\":\"bad_request\"}\n"))
	unknownID := uuid.New()
	assertWebSocketControlResponse(t, control.URL+"/websocket/"+unknownID.String(), http.StatusNotFound, []byte("{\"error\":\"not_found\"}\n"))
	assertWebSocketControlResponse(t, control.URL+"/traffic/"+unknownID.String()+"/websocket", http.StatusNotFound, []byte("{\"error\":\"not_found\"}\n"))

	if err := marasiws.WriteFrame(connection, marasiws.Frame{
		Fin:     true,
		Opcode:  marasiws.OpClose,
		Payload: []byte{0x03, 0xe8},
	}, true); err != nil {
		t.Fatalf("sending websocket close frame: %v", err)
	}
	closedData := readWebSocketEventData(t, events, "websocket.closed")
	closedBody := waitForWebSocketState(t, control.URL+connectionPath, "closed")
	if got := string(closedBody); got != closedData+"\n" {
		t.Fatalf("\nwanted event and closed get to have the same connection JSON:\n%s\ngot:\n%s", closedData+"\n", got)
	}
	assertWebSocketControlResponse(t, control.URL+"/traffic/"+opened.RequestID.String()+"/websocket", http.StatusOK, closedBody)
}

func TestWebSocketConnectionList(t *testing.T) {
	control, originURL, proxyAddress := newWebSocketControlFixture(t)

	t.Run("should return an empty page", func(t *testing.T) {
		assertWebSocketControlResponse(t, control.URL+"/websocket", http.StatusOK, []byte("{\"items\":[],\"next_cursor\":null}\n"))
	})

	t.Run("should reject invalid paging and ignore unknown parameters", func(t *testing.T) {
		for _, query := range []string{"limit=0", "limit=501", "limit=abc", "limit=1.5", "limit=", "cursor=bad", "cursor="} {
			assertWebSocketControlResponse(t, control.URL+"/websocket?"+query, http.StatusBadRequest, []byte("{\"error\":\"bad_request\"}\n"))
		}
		assertWebSocketControlResponse(t, control.URL+"/websocket?state=closed", http.StatusOK, []byte("{\"items\":[],\"next_cursor\":null}\n"))
	})

	t.Run("should page saved open closed and error connections by id", func(t *testing.T) {
		stream, events := connectEventStream(t, control.URL)
		defer stream.Body.Close()
		var ids []uuid.UUID
		var bodies []string
		for i := range 3 {
			connection, request := upgradeWebSocketThroughProxy(t, proxyAddress, originURL)
			t.Cleanup(func() { connection.Close() })
			response, err := http.ReadResponse(bufio.NewReader(connection), request)
			if err != nil {
				t.Fatalf("\nwanted:\nwebsocket upgrade response\ngot:\n%v", err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("\nwanted:\n101 upgrade\ngot:\n%d", response.StatusCode)
			}
			var opened struct {
				ID uuid.UUID `json:"id"`
			}
			if err := json.Unmarshal([]byte(readWebSocketEventData(t, events, "websocket.opened")), &opened); err != nil {
				t.Fatalf("\nwanted:\ndecoded websocket.opened event\ngot:\n%v", err)
			}
			ids = append(ids, opened.ID)
			path := control.URL + "/websocket/" + opened.ID.String()
			waitForWebSocketState(t, path, "open")
			if i == 1 {
				if err := marasiws.WriteFrame(connection, marasiws.Frame{Fin: true, Opcode: marasiws.OpClose, Payload: []byte{0x03, 0xe8}}, true); err != nil {
					t.Fatalf("\nwanted:\nwebsocket close frame sent\ngot:\n%v", err)
				}
				bodies = append(bodies, strings.TrimSpace(string(waitForWebSocketState(t, path, "closed"))))
			} else if i == 2 {
				connection.Close()
				bodies = append(bodies, strings.TrimSpace(string(waitForWebSocketState(t, path, "error"))))
			} else {
				bodies = append(bodies, strings.TrimSpace(string(waitForWebSocketState(t, path, "open"))))
			}
		}
		if !(ids[0].String() < ids[1].String() && ids[1].String() < ids[2].String()) {
			t.Fatalf("\nwanted:\nascending connection IDs from sequential upgrades\ngot:\n%v", ids)
		}
		firstPage := fmt.Sprintf(`{"items":[%s,%s],"next_cursor":"%s"}`+"\n", bodies[2], bodies[1], ids[1])
		assertWebSocketControlResponse(t, control.URL+"/websocket?limit=2&state=open", http.StatusOK, []byte(firstPage))
		assertWebSocketControlResponse(t, control.URL+"/websocket?limit=2&cursor="+ids[1].String(), http.StatusOK, []byte(fmt.Sprintf(`{"items":[%s],"next_cursor":null}`+"\n", bodies[0])))
		assertWebSocketControlResponse(t, control.URL+"/websocket?cursor="+uuid.Nil.String(), http.StatusOK, []byte("{\"items\":[],\"next_cursor\":null}\n"))
		assertWebSocketControlResponse(t, control.URL+"/websocket", http.StatusOK, []byte(fmt.Sprintf(`{"items":[%s,%s,%s],"next_cursor":null}`+"\n", bodies[2], bodies[1], bodies[0])))
	})
}

func TestWebSocketMessageList(t *testing.T) {
	control, originURL, proxyAddress := newWebSocketControlFixture(t)
	stream, events := connectEventStream(t, control.URL)
	defer stream.Body.Close()
	connection, request := upgradeWebSocketThroughProxy(t, proxyAddress, originURL)
	t.Cleanup(func() { connection.Close() })
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		t.Fatalf("reading websocket upgrade response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("\nwanted:\n101 upgrade\ngot:\n%d", response.StatusCode)
	}
	var opened struct {
		ID        uuid.UUID `json:"id"`
		RequestID uuid.UUID `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(readWebSocketEventData(t, events, "websocket.opened")), &opened); err != nil {
		t.Fatalf("decoding websocket.opened: %v", err)
	}
	path := control.URL + "/websocket/" + opened.ID.String() + "/message"
	waitForWebSocketState(t, control.URL+"/websocket/"+opened.ID.String(), "open")
	assertWebSocketControlResponse(t, path, http.StatusOK, []byte("{\"items\":[],\"next_cursor\":null}\n"))
	assertWebSocketControlResponse(t, control.URL+"/websocket/"+uuid.New().String()+"/message", http.StatusNotFound, []byte("{\"error\":\"not_found\"}\n"))
	assertWebSocketControlResponse(t, control.URL+"/websocket/not-a-uuid/message", http.StatusBadRequest, []byte("{\"error\":\"bad_request\"}\n"))
	for _, query := range []string{"limit=0", "limit=501", "limit=abc", "limit=", "cursor=bad", "cursor="} {
		assertWebSocketControlResponse(t, path+"?"+query, http.StatusBadRequest, []byte("{\"error\":\"bad_request\"}\n"))
	}
	for _, rawQuery := range []string{"limit=%ZZ", "cursor=%ZZ"} {
		req, err := http.NewRequest(http.MethodGet, path, nil)
		if err != nil {
			t.Fatalf("creating message page request: %v", err)
		}
		req.URL.RawQuery = rawQuery
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("getting malformed message page: %v", err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatalf("reading malformed message page: %v", err)
		}
		if res.StatusCode != http.StatusBadRequest || string(body) != "{\"error\":\"bad_request\"}\n" {
			t.Fatalf("\nwanted:\n400 bad_request for %s\ngot:\n%d %s", rawQuery, res.StatusCode, body)
		}
	}
	assertWebSocketControlResponse(t, path+"/"+uuid.New().String(), http.StatusNotFound, []byte("404 page not found\n"))
	wrongMethod, err := http.Post(path, "application/json", nil)
	if err != nil {
		t.Fatalf("posting to message list: %v", err)
	}
	wrongMethod.Body.Close()
	if wrongMethod.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("\nwanted:\n405 for POST /message\ngot:\n%d", wrongMethod.StatusCode)
	}

	t.Run("should store an unreassembled fragment and an empty control frame", func(t *testing.T) {
		for _, frame := range []marasiws.Frame{
			{Fin: false, Opcode: marasiws.OpText, Payload: []byte("first")},
			{Fin: true, Opcode: marasiws.OpPing},
		} {
			if err := marasiws.WriteFrame(connection, frame, true); err != nil {
				t.Fatalf("writing websocket frame: %v", err)
			}
		}
		fragment := readWebSocketEventData(t, events, "websocket.message")
		ping := readWebSocketEventData(t, events, "websocket.message")
		var first, second struct {
			ID           uuid.UUID `json:"id"`
			ConnectionID uuid.UUID `json:"connection_id"`
			RequestID    uuid.UUID `json:"request_id"`
			Direction    string    `json:"direction"`
			Opcode       int       `json:"opcode"`
			Fin          bool      `json:"fin"`
			Payload      string    `json:"payload"`
			IsBinary     bool      `json:"is_binary"`
			CreatedAt    string    `json:"created_at"`
		}
		if err := json.Unmarshal([]byte(fragment), &first); err != nil {
			t.Fatalf("decoding fragment event: %v", err)
		}
		if err := json.Unmarshal([]byte(ping), &second); err != nil {
			t.Fatalf("decoding ping event: %v", err)
		}
		if first.ID == uuid.Nil || first.ConnectionID != opened.ID || first.RequestID != opened.RequestID || first.Direction != "client" || first.Opcode != 1 || first.Fin || first.Payload != "Zmlyc3Q=" || first.IsBinary || first.CreatedAt == "" || !strings.Contains(fragment, `"metadata":{}`) {
			t.Fatalf("\nwanted:\nclient text fragment with its own ID and base64 payload\ngot:\n%s", fragment)
		}
		if second.ID == uuid.Nil || second.ID == first.ID || second.ConnectionID != opened.ID || second.RequestID != opened.RequestID || second.Direction != "client" || second.Opcode != 9 || !second.Fin || second.Payload != "" || second.IsBinary || second.CreatedAt == "" || !strings.Contains(ping, `"metadata":{}`) {
			t.Fatalf("\nwanted:\nempty client ping with full message fields\ngot:\n%s", ping)
		}
		if _, err := time.Parse(time.RFC3339, first.CreatedAt); err != nil {
			t.Fatalf("invalid message timestamp %q: %v", first.CreatedAt, err)
		}

		// The event precedes the asynchronous database flush.
		var page []byte
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			res, err := http.Get(path + "?limit=1&ignored=yes")
			if err != nil {
				t.Fatalf("getting message page: %v", err)
			}
			page, err = io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatalf("reading message page: %v", err)
			}
			if string(page) == fmt.Sprintf(`{"items":[%s],"next_cursor":"%s"}`+"\n", ping, second.ID) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		want := fmt.Sprintf(`{"items":[%s],"next_cursor":"%s"}`+"\n", ping, second.ID)
		if string(page) != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, page)
		}
		assertWebSocketControlResponse(t, path+"?limit=1&cursor="+second.ID.String(), http.StatusOK, []byte(fmt.Sprintf(`{"items":[%s],"next_cursor":null}`+"\n", fragment)))
		assertWebSocketControlResponse(t, path+"?cursor="+uuid.Nil.String(), http.StatusOK, []byte("{\"items\":[],\"next_cursor\":null}\n"))
	})

	t.Run("should publish a dropped frame only after Checkpoint resolves it", func(t *testing.T) {
		if err := marasiws.WriteFrame(connection, marasiws.Frame{Fin: true, Opcode: marasiws.OpContinuation, Payload: []byte(" last")}, true); err != nil {
			t.Fatalf("finishing fragment: %v", err)
		}
		continuation := readWebSocketEventData(t, events, "websocket.message")
		var continued struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal([]byte(continuation), &continued); err != nil {
			t.Fatalf("decoding continuation: %v", err)
		}
		wantContinuation := fmt.Sprintf(`{"items":[%s],"next_cursor":"%s"}`+"\n", continuation, continued.ID)
		var lastPage []byte
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			res, err := http.Get(path + "?limit=1")
			if err != nil {
				t.Fatalf("getting continuation page: %v", err)
			}
			lastPage, err = io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatalf("reading continuation page: %v", err)
			}
			if string(lastPage) == wantContinuation {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if string(lastPage) != wantContinuation {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantContinuation, lastPage)
		}
		res, err := http.Post(control.URL+"/checkpoint/websocket-intercept", "application/json", strings.NewReader(`{"websocket_intercept":true}`))
		if err != nil {
			t.Fatalf("enabling websocket Checkpoint: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\n200 enabling Checkpoint\ngot:\n%d", res.StatusCode)
		}
		if err := marasiws.WriteFrame(connection, marasiws.Frame{Fin: true, Opcode: marasiws.OpText, Payload: []byte("drop me")}, true); err != nil {
			t.Fatalf("sending held frame: %v", err)
		}
		held := readWebSocketEventData(t, events, "checkpoint.held")
		var item struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal([]byte(held), &item); err != nil || item.ID == uuid.Nil {
			t.Fatalf("\nwanted:\ncheckpoint.held with a message ID\ngot:\n%s, %v", held, err)
		}
		// While held, the message has not been stored or published as websocket.message.
		assertWebSocketControlResponse(t, path+"?limit=1", http.StatusOK, []byte(wantContinuation))
		res, err = http.Post(control.URL+"/checkpoint/"+item.ID.String()+"/drop", "application/json", nil)
		if err != nil {
			t.Fatalf("dropping held frame: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\n200 dropping frame\ngot:\n%d", res.StatusCode)
		}
		dropped := readWebSocketEventData(t, events, "websocket.message")
		var message struct {
			ID       uuid.UUID      `json:"id"`
			Payload  string         `json:"payload"`
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(dropped), &message); err != nil {
			t.Fatalf("decoding dropped event: %v", err)
		}
		if message.ID != item.ID || message.Payload != "ZHJvcCBtZQ==" || message.Metadata["dropped"] != true {
			t.Fatalf("\nwanted:\nstored dropped frame with held ID and original payload\ngot:\n%s", dropped)
		}
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			res, err := http.Get(path + "?limit=1")
			if err != nil {
				t.Fatalf("reading dropped message: %v", err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatalf("reading dropped page: %v", err)
			}
			if string(body) == fmt.Sprintf(`{"items":[%s],"next_cursor":"%s"}`+"\n", dropped, item.ID) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("dropped websocket.message was not saved: %s", dropped)
	})
}

func newWebSocketControlFixture(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		connection, buffered, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer connection.Close()

		accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		if _, err := fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:])); err != nil {
			return
		}
		if err := buffered.Flush(); err != nil {
			return
		}
		for {
			frame, err := marasiws.ReadFrame(buffered)
			if err != nil {
				return
			}
			if frame.Opcode == marasiws.OpClose {
				_ = marasiws.WriteFrame(buffered, frame, false)
				_ = buffered.Flush()
				return
			}
		}
	}))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConnection, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
	if err != nil {
		t.Fatalf("creating database: %v", err)
	}
	repository := db.NewProxyRepo(dbConnection)
	defaultExtensions, err := repository.GetExtensions()
	if err != nil {
		_ = repository.Close()
		t.Fatalf("getting default extensions: %v", err)
	}
	proxy, err := marasi.New(
		marasi.WithConfigDir(t.TempDir()),
		marasi.WithDefaultRepositories(repository),
	)
	if err != nil {
		_ = repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	proxy.Scope = compass.NewScope(true)
	proxy.Waypoints = make(map[string]string)
	if err := proxy.WithOptions(
		marasi.WithExtensions(defaultExtensions),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
	); err != nil {
		_ = proxy.Close()
		t.Fatalf("configuring proxy pipeline: %v", err)
	}
	server := newTestServer(proxy, func() {})
	server.heartbeatInterval = 50 * time.Millisecond
	if err := proxy.WithOptions(
		marasi.WithRequestHandler(server.HandleRequest),
		marasi.WithResponseHandler(server.HandleResponse),
		marasi.WithWebSocketOpenHandler(server.HandleWebSocketOpen),
		marasi.WithWebSocketMessageHandler(server.HandleWebSocketMessage),
		marasi.WithWebSocketCloseHandler(server.HandleWebSocketClose),
		marasi.WithWebSocketInterceptHandler(server.HandleWebSocketIntercept),
	); err != nil {
		_ = proxy.Close()
		t.Fatalf("installing service handlers: %v", err)
	}
	proxyListener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		_ = proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- proxy.Serve(proxyListener) }()
	control := httptest.NewServer(server)
	t.Cleanup(func() {
		control.Close()
		if err := proxy.Close(); err != nil {
			t.Errorf("closing proxy: %v", err)
		}
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})

	return control, origin.URL, net.JoinHostPort(proxy.Addr, proxy.Port)
}

func upgradeWebSocketThroughProxy(t *testing.T, proxyAddress, originURL string) (net.Conn, *http.Request) {
	t.Helper()
	connection, err := net.Dial("tcp", proxyAddress)
	if err != nil {
		t.Fatalf("dialing proxy: %v", err)
	}
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		connection.Close()
		t.Fatalf("setting websocket deadline: %v", err)
	}
	parsedOrigin, err := url.Parse(originURL)
	if err != nil {
		connection.Close()
		t.Fatalf("parsing origin URL: %v", err)
	}
	target := originURL + "/socket?test=1"
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		connection.Close()
		t.Fatalf("creating websocket request: %v", err)
	}
	request.Header.Set("Host", parsedOrigin.Host)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Key", key)
	request.Header.Set("Sec-WebSocket-Version", "13")
	if _, err := fmt.Fprintf(connection, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", request.URL.String(), parsedOrigin.Host, key); err != nil {
		connection.Close()
		t.Fatalf("writing websocket request: %v", err)
	}
	return connection, request
}

func websocketEventData(t *testing.T, frame, name string) string {
	t.Helper()
	wantPrefix := "event: " + name + "\ndata: "
	if !strings.HasPrefix(frame, wantPrefix) || !strings.HasSuffix(frame, "\n\n") {
		t.Fatalf("\nwanted event frame:\n%s\ngot:\n%s", wantPrefix+"<json>\n\n", frame)
	}
	return strings.TrimSuffix(strings.TrimPrefix(frame, wantPrefix), "\n\n")
}

func readWebSocketEventData(t *testing.T, reader *bufio.Reader, name string) string {
	t.Helper()
	for range 20 {
		frame := readEventFrame(t, reader)
		if strings.HasPrefix(frame, "event: "+name+"\n") {
			return websocketEventData(t, frame, name)
		}
	}
	t.Fatalf("timed out waiting for %s event", name)
	return ""
}

func waitForWebSocketState(t *testing.T, endpoint, state string) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(endpoint)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				var current struct {
					State string `json:"state"`
				}
				if json.Unmarshal(body, &current) == nil && current.State == state {
					return body
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for websocket connection state %q at %s", state, endpoint)
	return nil
}

func assertWebSocketControlResponse(t *testing.T, endpoint string, status int, body []byte) {
	t.Helper()
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatalf("getting %s: %v", endpoint, err)
	}
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", endpoint, err)
	}
	if response.StatusCode != status || string(got) != string(body) {
		t.Fatalf("\nwanted status and body:\n%d %s\ngot:\n%d %s", status, body, response.StatusCode, got)
	}
}
