package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// grpcFrame is payload in a gRPC envelope with flags 0.
func grpcFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

// streamCheckpointFixture is a proxy on a project database, with its
// built-in extensions and the service's handlers, in front of a gRPC origin
// over h2c that answers each message it receives with "reply" and records
// what it received.
type streamCheckpointFixture struct {
	control  *httptest.Server
	proxy    *marasi.Proxy
	origin   string
	received chan []byte
	client   *http.Client
}

func newStreamCheckpointFixture(t *testing.T, extensions ...*domain.Extension) *streamCheckpointFixture {
	t.Helper()
	received := make(chan []byte, 16)
	origin := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			var prefix [5]byte
			if _, err := io.ReadFull(r.Body, prefix[:]); err != nil {
				break
			}
			payload := make([]byte, binary.BigEndian.Uint32(prefix[1:]))
			if _, err := io.ReadFull(r.Body, payload); err != nil {
				break
			}
			received <- payload
			_, _ = w.Write(grpcFrame([]byte("reply")))
			w.(http.Flusher).Flush()
		}
		w.Header().Set("Grpc-Status", "0")
	}), &http2.Server{}))
	t.Cleanup(origin.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	connection, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
	if err != nil {
		t.Fatal(err)
	}
	repository := db.NewProxyRepo(connection)
	builtIn, err := repository.GetExtensions()
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := marasi.New(marasi.WithConfigDir(t.TempDir()), marasi.WithDefaultRepositories(repository))
	if err != nil {
		t.Fatal(err)
	}
	proxy.Scope = compass.NewScope(true)
	proxy.Waypoints = make(map[string]string)
	if err := proxy.WithOptions(
		marasi.WithExtensions(append(builtIn, extensions...)),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
	); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(proxy, func() {})
	server.heartbeatInterval = 50 * time.Millisecond
	if err := proxy.WithOptions(
		marasi.WithInterceptHandler(server.HandleIntercept),
		marasi.WithCheckpointAbandonedHandler(server.HandleCheckpointAbandoned),
	); err != nil {
		t.Fatal(err)
	}
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- proxy.Serve(listener) }()
	control := httptest.NewServer(server)
	t.Cleanup(func() {
		control.Close()
		_ = proxy.Close()
		<-served
	})

	proxyAddr := listener.Addr().String()
	client := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
		},
	}}
	return &streamCheckpointFixture{control: control, proxy: proxy, origin: origin.URL, received: received, client: client}
}

// call starts a gRPC call with body, and returns the response once its head
// arrived. contentLength declares the body's length when not -1.
func (f *streamCheckpointFixture) call(t *testing.T, body io.Reader, contentLength int64) <-chan *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.origin+"/test.Echo/PingPong", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/grpc")
	request.Header.Set("Te", "trailers")
	request.ContentLength = contentLength
	responses := make(chan *http.Response, 1)
	go func() {
		response, err := f.client.Do(request)
		if err != nil {
			t.Errorf("the call: %v", err)
			close(responses)
			return
		}
		responses <- response
	}()
	return responses
}

// post sends body to the control path and returns the status and body.
func (f *streamCheckpointFixture) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	response, err := http.Post(f.control.URL+path, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(data)
}

// streamItem is a stream item of GET /checkpoint.
type streamItem struct {
	ID         uuid.UUID      `json:"id"`
	Type       string         `json:"type"`
	Payload    []byte         `json:"payload"`
	Text       *string        `json:"text"`
	Direction  string         `json:"direction"`
	Framing    string         `json:"framing"`
	PairID     uuid.UUID      `json:"pair_id"`
	Flags      *int           `json:"flags"`
	Compressed bool           `json:"compressed"`
	Metadata   map[string]any `json:"metadata"`
}

// held waits until n stream items are held, and returns them.
func (f *streamCheckpointFixture) held(t *testing.T, n int) []streamItem {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := http.Get(f.control.URL + "/checkpoint?kind=stream")
		if err != nil {
			t.Fatal(err)
		}
		var list struct {
			Items []streamItem `json:"items"`
		}
		err = json.NewDecoder(response.Body).Decode(&list)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Items) == n {
			return list.Items
		}
		if time.Now().After(deadline) {
			t.Fatalf("held stream items\nwanted:\n%d\ngot:\n%+v", n, list.Items)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// trailerItem is a trailer item of GET /checkpoint.
type trailerItem struct {
	ID            uuid.UUID      `json:"id"`
	Type          string         `json:"type"`
	Raw           []byte         `json:"raw"`
	Direction     string         `json:"direction"`
	Framing       string         `json:"framing"`
	PairID        uuid.UUID      `json:"pair_id"`
	Metadata      map[string]any `json:"metadata"`
	InterceptedAt time.Time      `json:"intercepted_at"`
}

// heldTrailers waits until n trailer items are held, and returns them.
func (f *streamCheckpointFixture) heldTrailers(t *testing.T, n int) []trailerItem {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := http.Get(f.control.URL + "/checkpoint?kind=trailer")
		if err != nil {
			t.Fatal(err)
		}
		var list struct {
			Items []trailerItem `json:"items"`
		}
		err = json.NewDecoder(response.Body).Decode(&list)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Items) == n {
			return list.Items
		}
		if time.Now().After(deadline) {
			t.Fatalf("held trailer items\nwanted:\n%d\ngot:\n%+v", n, list.Items)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// receive waits for the origin to receive a message.
func (f *streamCheckpointFixture) receive(t *testing.T) []byte {
	t.Helper()
	select {
	case payload := <-f.received:
		return payload
	case <-time.After(5 * time.Second):
		t.Fatal("the origin received no message")
		return nil
	}
}

func TestCheckpointStreamItems(t *testing.T) {
	// 1:VARINT 150, the protobuf of the protobuf encoding guide.
	protobuf := []byte{0x08, 0x96, 0x01}
	const protobufText = "1:VARINT 150\n"

	t.Run("should give the same text form in the Checkpoint item, Lua's message:text() and the stream message route", func(t *testing.T) {
		texter := &domain.Extension{ID: uuid.New(), Name: "texter", Enabled: true, LuaContent: `
			function processStreamMessage(message)
				message:set_metadata({ text = message:text() })
			end
		`}
		f := newStreamCheckpointFixture(t, texter)
		if status, body := f.post(t, "/checkpoint/stream-intercept", `{"stream_intercept":true}`); status != http.StatusOK {
			t.Fatalf("turning the switch on\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		body, sender := io.Pipe()
		defer sender.Close()
		responses := f.call(t, body, -1)
		go func() { _, _ = sender.Write(grpcFrame(protobuf)) }()

		item := f.held(t, 1)[0]
		lua, _ := item.Metadata["texter"].(map[string]any)
		if item.Text == nil || *item.Text != protobufText || lua["text"] != protobufText {
			t.Fatalf("the held item's text and Lua's\nwanted:\n%q twice\ngot:\n%v %v", protobufText, item.Text, lua["text"])
		}
		if item.Type != "stream" || item.Direction != "request" || item.Framing != "grpc" || item.Flags == nil || *item.Flags != 0 ||
			!bytes.Equal(item.Payload, protobuf) || item.PairID == uuid.Nil || item.Metadata["intercepted"] != true {
			t.Fatalf("the held item\nwanted:\na grpc request message with its payload, flags 0, pair and intercepted\ngot:\n%+v", item)
		}
		if status, body := f.post(t, "/checkpoint/"+item.ID.String()+"/forward", `{}`); status != http.StatusOK {
			t.Fatalf("forwarding\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		if got := f.receive(t); !bytes.Equal(got, protobuf) {
			t.Fatalf("the origin's message\nwanted:\n%x\ngot:\n%x", protobuf, got)
		}
		response := <-responses
		reply := f.held(t, 1)[0]
		if status, body := f.post(t, "/checkpoint/forward", fmt.Sprintf(`{"ids":["%s"]}`, reply.ID)); status != http.StatusOK {
			t.Fatalf("forwarding the reply in bulk\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		sender.Close()
		trailers := f.heldTrailers(t, 1)[0]
		if status, body := f.post(t, "/checkpoint/"+trailers.ID.String()+"/forward", `{}`); status != http.StatusOK {
			t.Fatalf("forwarding the trailers\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		if data, _ := io.ReadAll(response.Body); !bytes.Equal(data, grpcFrame([]byte("reply"))) {
			t.Fatalf("the client's response body\nwanted:\nthe reply\ngot:\n%q", data)
		}
		response.Body.Close()

		var stored struct {
			Text     *string        `json:"text"`
			Metadata map[string]any `json:"metadata"`
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := http.Get(f.control.URL + "/stream/" + item.PairID.String() + "/message/" + item.ID.String())
			if err != nil {
				t.Fatal(err)
			}
			status := got.StatusCode
			_ = json.NewDecoder(got.Body).Decode(&stored)
			got.Body.Close()
			if status == http.StatusOK {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the stored message\nwanted:\n200\ngot:\n%d", status)
			}
			time.Sleep(10 * time.Millisecond)
		}
		lua, _ = stored.Metadata["texter"].(map[string]any)
		if stored.Text == nil || *stored.Text != *item.Text || lua["text"] != *item.Text {
			t.Fatalf("the route's text and the stored Lua text\nwanted:\n%q, the Checkpoint item's\ngot:\n%v %v", *item.Text, stored.Text, lua["text"])
		}
	})

	t.Run("should forward an edit, drop, and publish the Checkpoint events with the pair as request_id", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		f.proxy.SetStreamIntercept(true)
		events, reader := connectEventStream(t, f.control.URL)
		defer events.Body.Close()
		body, sender := io.Pipe()
		defer sender.Close()
		f.call(t, body, -1)

		go func() { _, _ = sender.Write(grpcFrame([]byte("drop me"))) }()
		dropped := f.held(t, 1)[0]
		var heldEvent streamItem
		if err := json.Unmarshal([]byte(readWebSocketEventData(t, reader, "checkpoint.held")), &heldEvent); err != nil || heldEvent.ID != dropped.ID || heldEvent.Type != "stream" {
			t.Fatalf("checkpoint.held\nwanted:\nthe stream item\ngot:\n%+v %v", heldEvent, err)
		}
		if status, body := f.post(t, "/checkpoint/"+dropped.ID.String()+"/drop", ""); status != http.StatusOK {
			t.Fatalf("dropping\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		want := fmt.Sprintf(`{"id":"%s","type":"stream","request_id":"%s","reason":"dropped"}`, dropped.ID, dropped.PairID)
		if got := readWebSocketEventData(t, reader, "checkpoint.dropped"); got != want {
			t.Fatalf("checkpoint.dropped\nwanted:\n%s\ngot:\n%s", want, got)
		}

		go func() { _, _ = sender.Write(grpcFrame([]byte("edit me"))) }()
		edited := f.held(t, 1)[0]
		forward := fmt.Sprintf(`{"payload":"%s"}`, base64.StdEncoding.EncodeToString([]byte("edited, longer")))
		if status, body := f.post(t, "/checkpoint/"+edited.ID.String()+"/forward", forward); status != http.StatusOK {
			t.Fatalf("forwarding an edit\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		if got := f.receive(t); string(got) != "edited, longer" {
			t.Fatalf("the origin's message\nwanted:\n%q, the dropped one never sent\ngot:\n%q", "edited, longer", got)
		}
		want = fmt.Sprintf(`{"id":"%s","type":"stream","request_id":"%s","reason":"forwarded"}`, edited.ID, edited.PairID)
		if got := readWebSocketEventData(t, reader, "checkpoint.forwarded"); got != want {
			t.Fatalf("checkpoint.forwarded\nwanted:\n%s\ngot:\n%s", want, got)
		}
		reply := f.held(t, 1)[0]
		if status, body := f.post(t, "/checkpoint/drop", fmt.Sprintf(`{"ids":["%s"]}`, reply.ID)); status != http.StatusOK || body != `{"done":1,"gone":0,"failed":0}`+"\n" {
			t.Fatalf("dropping in bulk\nwanted:\n200, 1 done\ngot:\n%d %s", status, body)
		}
	})

	t.Run("should refuse with its reason a length-changing edit or a drop on a direction whose head declared a length", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		f.proxy.SetStreamIntercept(true)
		message := grpcFrame([]byte("abc"))
		f.call(t, bytes.NewReader(message), int64(len(message)))
		item := f.held(t, 1)[0]

		forward := fmt.Sprintf(`{"payload":"%s"}`, base64.StdEncoding.EncodeToString([]byte("abcdef")))
		status, body := f.post(t, "/checkpoint/"+item.ID.String()+"/forward", forward)
		var refusal struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal([]byte(body), &refusal)
		if status != http.StatusBadRequest || refusal.Error != "invalid_checkpoint_request" || !bytes.Contains([]byte(refusal.Message), []byte("declares a length")) {
			t.Fatalf("a length-changing edit\nwanted:\n400 with the reason\ngot:\n%d %s", status, body)
		}
		if status, body := f.post(t, "/checkpoint/"+item.ID.String()+"/drop", ""); status != http.StatusBadRequest || !bytes.Contains([]byte(body), []byte("declares a length")) {
			t.Fatalf("a drop\nwanted:\n400 with the reason\ngot:\n%d %s", status, body)
		}
		if status, body := f.post(t, "/checkpoint/drop", `{"ids":["`+item.ID.String()+`"]}`); body != `{"done":0,"gone":0,"failed":1}`+"\n" {
			t.Fatalf("a bulk drop\nwanted:\n1 failed\ngot:\n%d %s", status, body)
		}
		forward = fmt.Sprintf(`{"payload":"%s"}`, base64.StdEncoding.EncodeToString([]byte("xyz")))
		if status, body := f.post(t, "/checkpoint/"+item.ID.String()+"/forward", forward); status != http.StatusOK {
			t.Fatalf("an edit that keeps the length\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		if got := f.receive(t); string(got) != "xyz" {
			t.Fatalf("the origin's message\nwanted:\n%q\ngot:\n%q", "xyz", got)
		}
	})

	t.Run("should list a held trailer block under kind trailer, refuse an edit that does not parse, and send an edited grpc-status", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		f.proxy.SetStreamIntercept(true)
		responses := f.call(t, bytes.NewReader(nil), -1)
		response := <-responses
		item := f.heldTrailers(t, 1)[0]
		if item.Type != "trailer" || string(item.Raw) != "grpc-status: 0\r\n" || item.Direction != "response" || item.Framing != "grpc" ||
			item.PairID == uuid.Nil || item.InterceptedAt.IsZero() {
			t.Fatalf("the held trailer item\nwanted:\nthe response's grpc-status 0, grpc, its pair, a hold time\ngot:\n%+v %q", item, item.Raw)
		}
		if streams := f.held(t, 0); len(streams) != 0 {
			t.Fatalf("stream items\nwanted:\nnone\ngot:\n%+v", streams)
		}

		bad := fmt.Sprintf(`{"raw":"%s"}`, base64.StdEncoding.EncodeToString([]byte("grpc-status 7\r\n")))
		if status, body := f.post(t, "/checkpoint/"+item.ID.String()+"/forward", bad); status != http.StatusBadRequest || !bytes.Contains([]byte(body), []byte("invalid_checkpoint_request")) {
			t.Fatalf("an edit that does not parse\nwanted:\n400 invalid_checkpoint_request\ngot:\n%d %s", status, body)
		}
		if status, body := f.post(t, "/checkpoint/"+item.ID.String()+"/forward", `{"payload":"eA=="}`); status != http.StatusBadRequest {
			t.Fatalf("a payload edit\nwanted:\n400\ngot:\n%d %s", status, body)
		}
		edit := fmt.Sprintf(`{"raw":"%s"}`, base64.StdEncoding.EncodeToString([]byte("grpc-status: 7\r\ngrpc-message: denied\r\n")))
		if status, body := f.post(t, "/checkpoint/"+item.ID.String()+"/forward", edit); status != http.StatusOK {
			t.Fatalf("forwarding the edit\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		_, _ = io.ReadAll(response.Body)
		response.Body.Close()
		if status := response.Trailer.Get("Grpc-Status"); status != "7" || response.Trailer.Get("Grpc-Message") != "denied" {
			t.Fatalf("the client's trailers\nwanted:\ngrpc-status 7, grpc-message denied\ngot:\n%v", response.Trailer)
		}
	})
}
