package marasi

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// negotiationResult is what one upgrade through the proxy produced.
type negotiationResult struct {
	// upstreamOffer is the Sec-WebSocket-Extensions the upstream received.
	upstreamOffer []string
	// response is the handshake response the client read, or nil when the
	// proxy closed the connection without one.
	response *http.Response
	// record is the stored WebSocket connection, or nil when none was stored.
	record *domain.WebSocketConnection
	// logs are the proxy logs written for the upgrade request.
	logs []domain.Log
	// stored is the raw handshake response Marasi stored, or empty when none
	// was stored.
	stored string
	// requestState is the websocket.state stored on the upgrade request, read
	// while the connection is open or after the proxy closed it without a
	// response.
	requestState any
}

// slowRequestInsertRepo delays every request insert, as a slow machine does,
// so writes that race the queued insert land first.
type slowRequestInsertRepo struct {
	domain.TrafficRepository
	delay time.Duration
}

func (repo slowRequestInsertRepo) InsertRequest(req *domain.ProxyRequest) error {
	time.Sleep(repo.delay)
	return repo.TrafficRepository.InsertRequest(req)
}

// negotiateThroughProxy sends one upgrade request with clientOffer through a
// proxy to an upstream that answers with upstreamAccept. workshopLua replaces
// the workshop extension when it is not empty. configure, when given, runs on
// the proxy before it serves.
func negotiateThroughProxy(t *testing.T, clientOffer []string, upstreamAccept []string, workshopLua string, configure ...func(*Proxy)) negotiationResult {
	t.Helper()

	upstreamOffers := make(chan []string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamOffers <- req.Header.Values("Sec-WebSocket-Extensions")
		connection, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()

		accept := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n",
			base64.StdEncoding.EncodeToString(accept[:]))
		for _, value := range upstreamAccept {
			fmt.Fprintf(buffered, "Sec-WebSocket-Extensions: %s\r\n", value)
		}
		fmt.Fprint(buffered, "\r\n")
		buffered.Flush()
		_, _ = io.Copy(io.Discard, buffered)
	}))
	defer origin.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConnection, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(dbConnection)
	extensions, err := repository.GetExtensions()
	if err != nil {
		repository.Close()
		t.Fatalf("getting default extensions: %v", err)
	}
	if workshopLua != "" {
		for _, extension := range extensions {
			if extension.Name == "workshop" {
				extension.LuaContent = workshopLua
				extension.Enabled = true
			}
		}
	}

	requestIDs := make(chan uuid.UUID, 1)
	storedResponses := make(chan string, 1)
	logs := make(chan domain.Log, 16)
	proxy, err := New(
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithExtensions(extensions),
		WithRequestHandler(func(req domain.ProxyRequest) error {
			requestIDs <- req.ID
			return nil
		}),
		WithResponseHandler(func(response domain.ProxyResponse) error {
			storedResponses <- string(response.Raw)
			return nil
		}),
		WithLogHandler(func(log domain.Log) error {
			logs <- log
			return nil
		}),
		WithBasePipeline(),
		WithDefaultModifierPipeline(),
	)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	for _, apply := range configure {
		apply(proxy)
	}
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- proxy.Serve(listener) }()
	defer func() {
		listener.Close()
		proxy.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	}()

	client, err := net.Dial("tcp", net.JoinHostPort(proxy.Addr, proxy.Port))
	if err != nil {
		t.Fatalf("dialing proxy: %v", err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("setting client deadline: %v", err)
	}

	var request strings.Builder
	fmt.Fprintf(&request, "GET %s/socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n",
		origin.URL, strings.TrimPrefix(origin.URL, "http://"))
	for _, value := range clientOffer {
		fmt.Fprintf(&request, "Sec-WebSocket-Extensions: %s\r\n", value)
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(client, request.String()); err != nil {
		t.Fatalf("writing upgrade request: %v", err)
	}

	var result negotiationResult
	select {
	case result.upstreamOffer = <-upstreamOffers:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the upstream to receive the upgrade")
	}

	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err == nil {
		result.response = response
	}

	var requestID uuid.UUID
	select {
	case requestID = <-requestIDs:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for request ID")
	}

	if result.response != nil {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && result.record == nil {
			if record, err := repository.GetConnectionByRequestID(requestID); err == nil {
				result.record = record
			}
			time.Sleep(10 * time.Millisecond)
		}
		if result.record == nil {
			t.Fatalf("timed out waiting for the stored WebSocket connection")
		}
		select {
		case result.stored = <-storedResponses:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for the stored handshake response")
		}
		stateDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(stateDeadline) {
			if metadata, err := repository.GetMetadata(requestID); err == nil {
				result.requestState = metadata["websocket.state"]
				if result.requestState != "pending" {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	} else {
		deadline := time.After(5 * time.Second)
	waitForLog:
		for {
			select {
			case log := <-logs:
				if log.RequestID != nil && *log.RequestID == requestID {
					result.logs = append(result.logs, log)
					break waitForLog
				}
			case <-deadline:
				break waitForLog
			}
		}
		stateDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(stateDeadline) {
			if metadata, err := repository.GetMetadata(requestID); err == nil {
				result.requestState = metadata["websocket.state"]
				if result.requestState != "pending" {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return result
}

func TestProxy_WebSocketDeflateNegotiation(t *testing.T) {
	t.Run("should mirror the upstream's accepted deflate and record removed extensions", func(t *testing.T) {
		result := negotiateThroughProxy(t,
			[]string{"x-webkit-deflate-frame, permessage-deflate; client_max_window_bits", "permessage-deflate"},
			[]string{"permessage-deflate; server_no_context_takeover; client_max_window_bits=10"},
			"",
		)

		wantOffer := []string{"permessage-deflate; client_max_window_bits, permessage-deflate"}
		if !reflect.DeepEqual(result.upstreamOffer, wantOffer) {
			t.Fatalf("\nwanted upstream offer:\n%q\ngot:\n%q", wantOffer, result.upstreamOffer)
		}
		if result.response == nil || result.response.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("\nwanted:\n101 response\ngot:\n%v", result.response)
		}
		if result.requestState != "open" {
			t.Fatalf("\nwanted:\nwebsocket.state open on the upgrade request while connected\ngot:\n%v", result.requestState)
		}
		wantAccept := []string{"permessage-deflate; server_no_context_takeover; client_max_window_bits=10"}
		if got := result.response.Header.Values("Sec-WebSocket-Extensions"); !reflect.DeepEqual(got, wantAccept) {
			t.Fatalf("\nwanted client accept:\n%q\ngot:\n%q", wantAccept, got)
		}
		wantDeflate := &domain.WebSocketDeflate{
			ServerNoContextTakeover: true,
			ServerMaxWindowBits:     15,
			ClientMaxWindowBits:     10,
		}
		if !reflect.DeepEqual(result.record.Deflate, wantDeflate) {
			t.Fatalf("\nwanted deflate:\n%+v\ngot:\n%+v", wantDeflate, result.record.Deflate)
		}
		if want := []string{"x-webkit-deflate-frame"}; !reflect.DeepEqual(result.record.RemovedExtensions, want) {
			t.Fatalf("\nwanted removed extensions:\n%q\ngot:\n%q", want, result.record.RemovedExtensions)
		}
	})

	t.Run("should open without deflate when the upstream declines", func(t *testing.T) {
		result := negotiateThroughProxy(t, []string{"permessage-deflate"}, nil, "")

		if result.response == nil || result.response.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("\nwanted:\n101 response\ngot:\n%v", result.response)
		}
		if got := result.response.Header.Values("Sec-WebSocket-Extensions"); len(got) != 0 {
			t.Fatalf("\nwanted client accept:\nnone\ngot:\n%q", got)
		}
		if result.record.Deflate != nil {
			t.Fatalf("\nwanted deflate:\nnil\ngot:\n%+v", result.record.Deflate)
		}
		if len(result.record.RemovedExtensions) != 0 {
			t.Fatalf("\nwanted removed extensions:\nnone\ngot:\n%q", result.record.RemovedExtensions)
		}
	})

	t.Run("should remove an unknown extension that a Lua extension added to the offer", func(t *testing.T) {
		result := negotiateThroughProxy(t, nil, nil, `
			function processRequest(request)
				request:headers():set("Sec-WebSocket-Extensions", "x-added")
			end
		`)

		if len(result.upstreamOffer) != 0 {
			t.Fatalf("\nwanted upstream offer:\nnone\ngot:\n%q", result.upstreamOffer)
		}
		if want := []string{"x-added"}; !reflect.DeepEqual(result.record.RemovedExtensions, want) {
			t.Fatalf("\nwanted removed extensions:\n%q\ngot:\n%q", want, result.record.RemovedExtensions)
		}
	})

	t.Run("should send the client the upstream's accept even after a response edit", func(t *testing.T) {
		result := negotiateThroughProxy(t,
			[]string{"permessage-deflate"},
			[]string{"permessage-deflate"},
			`
			function processResponse(response)
				response:headers():set("Sec-WebSocket-Extensions", "permessage-deflate; server_no_context_takeover")
			end
		`)

		if result.response == nil {
			t.Fatalf("\nwanted:\n101 response\ngot:\nnil")
		}
		if got, want := result.response.Header.Values("Sec-WebSocket-Extensions"), []string{"permessage-deflate"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted client accept:\n%q\ngot:\n%q", want, got)
		}
		stored, err := http.ReadResponse(bufio.NewReader(strings.NewReader(result.stored)), nil)
		if err != nil {
			t.Fatalf("parsing the stored response %q: %v", result.stored, err)
		}
		if got, want := stored.Header.Values("Sec-WebSocket-Extensions"), []string{"permessage-deflate"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted stored accept, as the client got it:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should mark the request dropped when an extension drops the upgrade response", func(t *testing.T) {
		result := negotiateThroughProxy(t,
			nil,
			nil,
			"function processResponse(res)\n\tres:drop()\nend\n",
		)

		if result.response != nil {
			t.Fatalf("\nwanted:\nno handshake response\ngot:\n%d", result.response.StatusCode)
		}
		if result.requestState != "dropped" {
			t.Fatalf("\nwanted:\nwebsocket.state dropped on the upgrade request\ngot:\n%v", result.requestState)
		}
	})

	t.Run("should fail the handshake with a proxy log when the upstream accepts what the proxy cannot honour", func(t *testing.T) {
		result := negotiateThroughProxy(t,
			[]string{"permessage-deflate"},
			[]string{"permessage-deflate; client_max_window_bits=10"},
			"",
		)

		if result.response != nil {
			t.Fatalf("\nwanted:\nno handshake response\ngot:\n%d", result.response.StatusCode)
		}
		if len(result.logs) != 1 {
			t.Fatalf("\nwanted:\none proxy log for the request\ngot:\n%+v", result.logs)
		}
		if result.logs[0].Level != "ERROR" || !strings.Contains(result.logs[0].Message, "client_max_window_bits") {
			t.Fatalf("\nwanted:\nERROR log naming client_max_window_bits\ngot:\n%s %q", result.logs[0].Level, result.logs[0].Message)
		}
		if result.requestState != "failed" {
			t.Fatalf("\nwanted:\nwebsocket.state failed on the upgrade request\ngot:\n%v", result.requestState)
		}
	})
	delayRequestInsert := func(proxy *Proxy) {
		proxy.TrafficRepo = slowRequestInsertRepo{TrafficRepository: proxy.TrafficRepo, delay: 200 * time.Millisecond}
	}

	t.Run("should keep the failed state when the request insert is slow", func(t *testing.T) {
		result := negotiateThroughProxy(t,
			[]string{"permessage-deflate"},
			[]string{"permessage-deflate; client_max_window_bits=10"},
			"",
			delayRequestInsert,
		)

		if result.response != nil {
			t.Fatalf("\nwanted:\nno handshake response\ngot:\n%d", result.response.StatusCode)
		}
		if result.requestState != "failed" {
			t.Fatalf("\nwanted:\nwebsocket.state failed on the upgrade request\ngot:\n%v", result.requestState)
		}
	})

	t.Run("should keep the dropped state when the request insert is slow", func(t *testing.T) {
		result := negotiateThroughProxy(t,
			nil,
			nil,
			"function processResponse(res)\n\tres:drop()\nend\n",
			delayRequestInsert,
		)

		if result.response != nil {
			t.Fatalf("\nwanted:\nno handshake response\ngot:\n%d", result.response.StatusCode)
		}
		if result.requestState != "dropped" {
			t.Fatalf("\nwanted:\nwebsocket.state dropped on the upgrade request\ngot:\n%v", result.requestState)
		}
	})

	// handshakeEnd is one call of the handshake ended handler, with the
	// websocket.state stored on the request when it ran.
	type handshakeEnd struct {
		state  string
		stored any
	}
	announceHandshakeEnd := func(ends chan<- handshakeEnd) func(*Proxy) {
		return func(proxy *Proxy) {
			proxy.TrafficRepo = slowRequestInsertRepo{TrafficRepository: proxy.TrafficRepo, delay: 200 * time.Millisecond}
			if err := proxy.WithOptions(WithWebSocketHandshakeEndedHandler(func(requestID uuid.UUID, state string) {
				var stored any
				if metadata, err := proxy.TrafficRepo.GetMetadata(requestID); err == nil {
					stored = metadata["websocket.state"]
				}
				ends <- handshakeEnd{state: state, stored: stored}
			})); err != nil {
				t.Fatalf("installing handshake ended handler: %v", err)
			}
		}
	}

	receiveHandshakeEnd := func(t *testing.T, ends <-chan handshakeEnd) handshakeEnd {
		t.Helper()
		select {
		case end := <-ends:
			return end
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for the handshake ended handler")
			return handshakeEnd{}
		}
	}

	t.Run("should announce a failed handshake once its state is stored", func(t *testing.T) {
		ends := make(chan handshakeEnd, 2)
		negotiateThroughProxy(t,
			[]string{"permessage-deflate"},
			[]string{"permessage-deflate; client_max_window_bits=10"},
			"",
			announceHandshakeEnd(ends),
		)

		want := handshakeEnd{state: "failed", stored: "failed"}
		if got := receiveHandshakeEnd(t, ends); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		select {
		case extra := <-ends:
			t.Fatalf("\nwanted:\none handshake ended call\ngot another:\n%+v", extra)
		default:
		}
	})

	t.Run("should announce a dropped handshake once its state is stored", func(t *testing.T) {
		ends := make(chan handshakeEnd, 2)
		negotiateThroughProxy(t,
			nil,
			nil,
			"function processResponse(res)\n\tres:drop()\nend\n",
			announceHandshakeEnd(ends),
		)

		want := handshakeEnd{state: "dropped", stored: "dropped"}
		if got := receiveHandshakeEnd(t, ends); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		select {
		case extra := <-ends:
			t.Fatalf("\nwanted:\none handshake ended call\ngot another:\n%+v", extra)
		default:
		}
	})

	t.Run("should not announce a handshake that opens", func(t *testing.T) {
		ends := make(chan handshakeEnd, 2)
		negotiateThroughProxy(t, nil, nil, "", announceHandshakeEnd(ends))

		select {
		case end := <-ends:
			t.Fatalf("\nwanted:\nno handshake ended call\ngot:\n%+v", end)
		default:
		}
	})
}

func TestFailWebSocketHandshake(t *testing.T) {
	t.Run("should return an error matching ErrWebSocketHandshakeFailed and ErrDropped", func(t *testing.T) {
		proxy := &Proxy{DBWriteChannel: make(chan any, 1)}
		req := httptest.NewRequest(http.MethodGet, "http://example.com/socket", nil)
		cause := errors.New("upstream accepted an extension no one offered")

		err := failWebSocketHandshake(proxy, &http.Response{Request: req}, cause)

		if !errors.Is(err, ErrWebSocketHandshakeFailed) || !errors.Is(err, ErrDropped) || !errors.Is(err, cause) {
			t.Fatalf("\nwanted:\nan error matching ErrWebSocketHandshakeFailed, ErrDropped and the cause\ngot:\n%v", err)
		}
	})
}
