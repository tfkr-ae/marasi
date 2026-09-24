package marasi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

type sessionTestReadWriteCloser struct {
	bytes.Buffer
}

func (c *sessionTestReadWriteCloser) Close() error {
	return nil
}

type sessionTestErrorReader struct {
	err error
}

type errorCloser struct {
	err error
}

func (closer *errorCloser) Close() error { return closer.err }

type testRoundTripFunc func(*http.Request) (*http.Response, error)

type testResponseBody struct {
	*strings.Reader
	closed bool
}

type testArmoryTrafficRepository struct {
	domain.TrafficRepository
	inserted []*domain.ProxyRequest
	err      error
}

type testArmoryEntryRepository struct {
	domain.ArmoryRepository
	entries []*domain.ArmoryEntry
	err     error
}

type testProxyArmoryService struct {
	ArmoryService
	repository domain.ArmoryRepository
}

func (roundTrip testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return roundTrip(req)
}

func (body *testResponseBody) Close() error {
	body.closed = true
	return nil
}

func (repository *testArmoryTrafficRepository) InsertRequest(request *domain.ProxyRequest) error {
	if repository.err != nil {
		return repository.err
	}
	repository.inserted = append(repository.inserted, request)
	return nil
}

func (repository *testArmoryEntryRepository) CreateArmoryEntry(entry *domain.ArmoryEntry) error {
	if repository.err != nil {
		return repository.err
	}
	repository.entries = append(repository.entries, entry)
	return nil
}

func (service *testProxyArmoryService) Repo() domain.ArmoryRepository {
	return service.repository
}

func TestProxy_SendArmoryRequest(t *testing.T) {
	t.Run("should send a rendered request with armory correlation", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}
		responseBody := &testResponseBody{Reader: strings.NewReader("bad gateway")}
		var got *http.Request
		var gotBody string
		proxy := &Proxy{
			Client: &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				got = req
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				gotBody = string(body)
				return &http.Response{
					StatusCode: http.StatusBadGateway,
					Body:       responseBody,
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})},
		}
		raw := "POST /submit HTTP/1.1\r\nHost: example.com\r\nContent-Length: 100\r\n\r\npayload"

		err = proxy.SendArmoryRequest(context.Background(), raw, runID, false)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got == nil {
			t.Fatal("\nwanted:\nHTTP request\ngot:\nnil")
		}
		if got.URL.Scheme != "http" {
			t.Fatalf("\nwanted:\nhttp\ngot:\n%s", got.URL.Scheme)
		}
		if got.URL.Host != "example.com" {
			t.Fatalf("\nwanted:\nexample.com\ngot:\n%s", got.URL.Host)
		}
		if got.RequestURI != "" {
			t.Fatalf("\nwanted:\nempty request URI\ngot:\n%s", got.RequestURI)
		}
		if got.Header.Get(armoryRunIDHeader) != runID.String() {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", runID, got.Header.Get(armoryRunIDHeader))
		}
		if _, exists := got.Header["User-Agent"]; !exists {
			t.Fatal("\nwanted:\nuser-agent header\ngot:\nmissing header")
		}
		if got.ContentLength != int64(len("payload")) {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", len("payload"), got.ContentLength)
		}
		if gotBody != "payload" {
			t.Fatalf("\nwanted:\npayload\ngot:\n%s", gotBody)
		}
		if !responseBody.closed {
			t.Fatal("\nwanted:\nclosed response body\ngot:\nopen response body")
		}
		if responseBody.Len() != 0 {
			t.Fatalf("\nwanted:\ndrained response body\ngot:\n%d unread bytes", responseBody.Len())
		}
	})

	t.Run("should use HTTPS when configured", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}
		var got *http.Request
		proxy := &Proxy{
			Client: &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				got = req
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})},
		}

		err = proxy.SendArmoryRequest(context.Background(), "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", runID, true)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.URL.Scheme != "https" {
			t.Fatalf("\nwanted:\nhttps\ngot:\n%s", got.URL.Scheme)
		}
	})

	t.Run("should cancel an in-flight request", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}
		started := make(chan struct{})
		proxy := &Proxy{
			Client: &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				close(started)
				<-req.Context().Done()
				return nil, req.Context().Err()
			})},
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- proxy.SendArmoryRequest(ctx, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", runID, false)
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("\nwanted:\nrequest to start\ngot:\ntimeout")
		}
		cancel()

		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("\nwanted:\ncancelled request\ngot:\ntimeout")
		}
	})

	t.Run("should return an error for invalid input", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}

		tests := []struct {
			name  string
			ctx   context.Context
			raw   string
			runID uuid.UUID
			want  string
		}{
			{name: "missing context", raw: "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", runID: runID, want: "request context is required"},
			{name: "missing run ID", ctx: context.Background(), raw: "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", want: "armory run ID is required"},
			{name: "malformed request", ctx: context.Background(), raw: "malformed", runID: runID, want: "recalculating content length"},
			{name: "missing host", ctx: context.Background(), raw: "GET / HTTP/1.1\r\n\r\n", runID: runID, want: "host header not found or is empty"},
		}

		proxy := &Proxy{Client: &http.Client{}}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				err := proxy.SendArmoryRequest(test.ctx, test.raw, test.runID, false)
				if err == nil {
					t.Fatal("\nwanted:\nerror\ngot:\nnil")
				}
				if !strings.Contains(err.Error(), test.want) {
					t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%v", test.want, err)
				}
			})
		}
	})

	t.Run("should return a client transport error", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}
		proxy := &Proxy{
			Client: &http.Client{Transport: testRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("transport failed")
			})},
		}

		err = proxy.SendArmoryRequest(context.Background(), "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", runID, false)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "sending armory request") {
			t.Fatalf("\nwanted:\nerror containing 'sending armory request'\ngot:\n%v", err)
		}
	})
}

func TestProxy_WriteToDBArmoryEntry(t *testing.T) {
	t.Run("should link an inserted request to its armory run", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}
		requestID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating request uuid: %v", err)
		}
		trafficRepository := &testArmoryTrafficRepository{}
		armoryRepository := &testArmoryEntryRepository{}
		proxy := &Proxy{
			TrafficRepo:    trafficRepository,
			Armory:         &testProxyArmoryService{repository: armoryRepository},
			DBWriteChannel: make(chan any, 1),
		}
		request := &domain.ProxyRequest{ID: requestID, Metadata: map[string]any{"armory_run_id": runID}}
		proxy.DBWriteChannel <- request
		close(proxy.DBWriteChannel)

		proxy.WriteToDB()

		if len(trafficRepository.inserted) != 1 || trafficRepository.inserted[0] != request {
			t.Fatalf("\nwanted:\ninserted request\ngot:\n%v", trafficRepository.inserted)
		}
		want := []*domain.ArmoryEntry{{RunID: runID, RequestID: requestID}}
		if !reflect.DeepEqual(armoryRepository.entries, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, armoryRepository.entries)
		}
	})

	t.Run("should not link a request that could not be inserted", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}
		trafficRepository := &testArmoryTrafficRepository{err: errors.New("insert failed")}
		armoryRepository := &testArmoryEntryRepository{}
		proxy := &Proxy{
			TrafficRepo:    trafficRepository,
			Armory:         &testProxyArmoryService{repository: armoryRepository},
			DBWriteChannel: make(chan any, 1),
		}
		proxy.DBWriteChannel <- &domain.ProxyRequest{Metadata: map[string]any{"armory_run_id": runID}}
		close(proxy.DBWriteChannel)

		proxy.WriteToDB()

		if len(armoryRepository.entries) != 0 {
			t.Fatalf("\nwanted:\n0 armory entries\ngot:\n%d", len(armoryRepository.entries))
		}
	})

	t.Run("should log an armory linking failure and continue", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}
		trafficRepository := &testArmoryTrafficRepository{}
		armoryRepository := &testArmoryEntryRepository{err: errors.New("link failed")}
		var logs strings.Builder
		previousOutput := log.Writer()
		log.SetOutput(&logs)
		defer log.SetOutput(previousOutput)
		proxy := &Proxy{
			TrafficRepo:    trafficRepository,
			Armory:         &testProxyArmoryService{repository: armoryRepository},
			DBWriteChannel: make(chan any, 2),
		}
		proxy.DBWriteChannel <- &domain.ProxyRequest{Metadata: map[string]any{"armory_run_id": runID}}
		proxy.DBWriteChannel <- &domain.ProxyRequest{Metadata: make(map[string]any)}
		close(proxy.DBWriteChannel)

		proxy.WriteToDB()

		if !strings.Contains(logs.String(), "linking request to armory run: link failed") {
			t.Fatalf("\nwanted:\nlog containing 'linking request to armory run: link failed'\ngot:\n%s", logs.String())
		}
		if len(trafficRepository.inserted) != 2 {
			t.Fatalf("\nwanted:\n2 inserted requests\ngot:\n%d", len(trafficRepository.inserted))
		}
	})

	t.Run("should log missing armory dependencies", func(t *testing.T) {
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating run uuid: %v", err)
		}

		tests := []struct {
			name    string
			service ArmoryService
			want    string
		}{
			{name: "missing service", want: "armory service is not configured"},
			{name: "missing repository", service: &testProxyArmoryService{}, want: "armory repository is not configured"},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				var logs strings.Builder
				previousOutput := log.Writer()
				log.SetOutput(&logs)
				defer log.SetOutput(previousOutput)
				proxy := &Proxy{
					TrafficRepo:    &testArmoryTrafficRepository{},
					Armory:         test.service,
					DBWriteChannel: make(chan any, 1),
				}
				proxy.DBWriteChannel <- &domain.ProxyRequest{Metadata: map[string]any{"armory_run_id": runID}}
				close(proxy.DBWriteChannel)

				proxy.WriteToDB()

				if !strings.Contains(logs.String(), test.want) {
					t.Fatalf("\nwanted:\nlog containing %q\ngot:\n%s", test.want, logs.String())
				}
			})
		}
	})
}

func TestProxy_WriteToDBLog(t *testing.T) {
	tests := []struct {
		name         string
		withCallback bool
	}{
		{name: "should persist a log without a callback"},
		{name: "should deliver a log to a configured callback", withCallback: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			dbConnection, err := db.New(":memory:", logger)
			if err != nil {
				t.Fatalf("creating in-memory database: %v", err)
			}
			repository := db.NewProxyRepo(dbConnection)
			t.Cleanup(func() { repository.Close() })
			entry := &domain.Log{
				ID:        uuid.New(),
				Timestamp: time.Now(),
				Level:     "INFO",
				Message:   "listener started",
			}
			var received domain.Log
			proxy := &Proxy{
				LogRepo:        repository,
				DBWriteChannel: make(chan any, 1),
			}
			if test.withCallback {
				proxy.OnLog = func(log domain.Log) error {
					received = log
					return nil
				}
			}
			proxy.DBWriteChannel <- entry
			close(proxy.DBWriteChannel)

			proxy.WriteToDB()

			logs, err := repository.GetLogs()
			if err != nil {
				t.Fatalf("getting persisted logs: %v", err)
			}
			if len(logs) != 1 || logs[0].ID != entry.ID {
				t.Fatalf("\nwanted:\npersisted log %s\ngot:\n%v", entry.ID, logs)
			}
			if test.withCallback && received.ID != entry.ID {
				t.Fatalf("\nwanted:\ncallback log %s\ngot:\n%v", entry.ID, received)
			}
		})
	}
}

type websocketRepositoryStub struct {
	connection *domain.WebSocketConnection
	messages   []*domain.WebSocketMessage
}

type recordingWebSocketRepository struct {
	websocketRepositoryStub
	updates chan *domain.WebSocketConnection
}

func (r *websocketRepositoryStub) InsertConnection(*domain.WebSocketConnection) error { return nil }
func (r *websocketRepositoryStub) UpdateConnection(*domain.WebSocketConnection) error { return nil }
func (r *websocketRepositoryStub) GetConnection(uuid.UUID) (*domain.WebSocketConnection, error) {
	return r.connection, nil
}
func (r *websocketRepositoryStub) GetConnectionByRequestID(uuid.UUID) (*domain.WebSocketConnection, error) {
	return r.connection, nil
}
func (r *websocketRepositoryStub) ListConnections(*uuid.UUID, int) ([]*domain.WebSocketConnection, *uuid.UUID, error) {
	return nil, nil, nil
}
func (r *websocketRepositoryStub) ListMessages(uuid.UUID, *uuid.UUID, int) ([]*domain.WebSocketMessage, *uuid.UUID, error) {
	return nil, nil, nil
}
func (r *websocketRepositoryStub) InsertMessage(*domain.WebSocketMessage) error { return nil }
func (r *websocketRepositoryStub) GetMessage(uuid.UUID) (*domain.WebSocketMessage, error) {
	if len(r.messages) == 0 {
		return nil, nil
	}
	return r.messages[0], nil
}
func (r *websocketRepositoryStub) GetMessages(uuid.UUID) ([]*domain.WebSocketMessage, error) {
	return r.messages, nil
}
func (r *websocketRepositoryStub) CountMessages(uuid.UUID) (int, error) {
	return len(r.messages), nil
}
func (r *websocketRepositoryStub) GetMessagesByRequestID(uuid.UUID) ([]*domain.WebSocketMessage, error) {
	return r.messages, nil
}

func (r *recordingWebSocketRepository) UpdateConnection(connection *domain.WebSocketConnection) error {
	if r.updates != nil {
		r.updates <- connection
	}
	return nil
}

func (r *sessionTestErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func receiveSessionDBWrite(t *testing.T, writes <-chan any) any {
	t.Helper()

	select {
	case item := <-writes:
		return item
	case <-time.After(5 * time.Second):
		t.Fatalf("\nwanted DB write:\n%v\ngot:\n%v", "queued", "timeout")
		return nil
	}
}

func TestProxy_RunWebSocketSession(t *testing.T) {
	t.Run("clean lifecycle", func(t *testing.T) {
		connectionID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating connection UUID: %v", err)
		}
		requestID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating request UUID: %v", err)
		}

		clientConnection, clientPeer := net.Pipe()
		upstreamConnection, upstreamPeer := net.Pipe()
		defer clientPeer.Close()
		defer upstreamPeer.Close()

		deadline := time.Now().Add(5 * time.Second)
		if deadlineErr := clientPeer.SetDeadline(deadline); deadlineErr != nil {
			t.Fatalf("setting client peer deadline: %v", deadlineErr)
		}
		if deadlineErr := upstreamPeer.SetDeadline(deadline); deadlineErr != nil {
			t.Fatalf("setting upstream peer deadline: %v", deadlineErr)
		}

		openEvents := make(chan domain.WebSocketConnection, 2)
		closeEvents := make(chan domain.WebSocketConnection, 2)
		proxy := &Proxy{
			WebSocketRegistry: marasiws.NewRegistry(),
			DBWriteChannel:    make(chan any, 2),
			OnWebSocketOpen: func(connection domain.WebSocketConnection) error {
				openEvents <- connection
				return nil
			},
			OnWebSocketClose: func(connection domain.WebSocketConnection) error {
				closeEvents <- connection
				return nil
			},
		}
		connection := marasiws.NewConnection(
			connectionID,
			requestID,
			clientConnection,
			clientConnection,
			upstreamConnection,
			nil,
		)
		record := domain.WebSocketConnection{
			ID:        connectionID,
			RequestID: requestID,
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/socket",
		}
		runResult := make(chan error, 1)
		released := make(chan struct{})
		go func() {
			runResult <- proxy.runWebSocketSessionWithRelease(connection, record, func() { close(released) })
		}()
		select {
		case <-released:
			if _, exists := proxy.WebSocketRegistry.Get(connectionID); !exists {
				t.Fatal("\nwanted:\nWebSocket registered before HTTP admission release\ngot:\nrelease before registration")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for HTTP admission release")
		}

		openItem := receiveSessionDBWrite(t, proxy.DBWriteChannel)
		openRecord, ok := openItem.(*domain.WebSocketConnection)
		if !ok {
			t.Fatalf("\nwanted DB write type:\n%T\ngot:\n%T", (*domain.WebSocketConnection)(nil), openItem)
		}
		if openRecord.ID != connectionID {
			t.Fatalf("\nwanted connection ID:\n%s\ngot:\n%s", connectionID, openRecord.ID)
		}
		if openRecord.RequestID != requestID {
			t.Fatalf("\nwanted request ID:\n%s\ngot:\n%s", requestID, openRecord.RequestID)
		}
		if openRecord.State != "open" {
			t.Fatalf("\nwanted state:\n%q\ngot:\n%q", "open", openRecord.State)
		}
		if openRecord.StartedAt.IsZero() {
			t.Fatalf("\nwanted StartedAt:\n%v\ngot:\n%v", "initialized", openRecord.StartedAt)
		}
		if openRecord.ClosedAt != nil {
			t.Fatalf("\nwanted ClosedAt:\n%v\ngot:\n%v", nil, openRecord.ClosedAt)
		}
		select {
		case event := <-openEvents:
			if event.ID != connectionID {
				t.Fatalf("\nwanted open event connection ID:\n%s\ngot:\n%s", connectionID, event.ID)
			}
			if event.State != "open" {
				t.Fatalf("\nwanted open event state:\n%q\ngot:\n%q", "open", event.State)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for websocket open event")
		}
		select {
		case event := <-openEvents:
			t.Fatalf("\nwanted websocket open event count:\n%d\ngot extra event:\n%v", 1, event)
		default:
		}

		registered, exists := proxy.WebSocketRegistry.Get(connectionID)
		if !exists {
			t.Fatalf("\nwanted registered connection exists:\n%v\ngot:\n%v", true, exists)
		}
		if registered != connection {
			t.Fatalf("\nwanted registered connection:\n%p\ngot:\n%p", connection, registered)
		}

		closePayload, err := marasiws.EncodeClosePayload(1000, "Goodbye")
		if err != nil {
			t.Fatalf("EncodeClosePayload() returned unexpected error: %v", err)
		}
		if writeErr := marasiws.WriteFrame(clientPeer, marasiws.Frame{
			Fin:     true,
			Opcode:  marasiws.OpClose,
			Payload: closePayload,
		}, true); writeErr != nil {
			t.Fatalf("writing client close frame: %v", writeErr)
		}
		if _, readErr := marasiws.ReadFrame(upstreamPeer); readErr != nil {
			t.Fatalf("reading upstream close frame: %v", readErr)
		}

		select {
		case sessionErr := <-runResult:
			if sessionErr != nil {
				t.Fatalf("runWebSocketSession() returned unexpected error: %v", sessionErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("\nwanted session state:\n%q\ngot:\n%q", "returned", "blocked")
		}

		updateItem := receiveSessionDBWrite(t, proxy.DBWriteChannel)
		update, ok := updateItem.(*domain.WebSocketConnectionUpdate)
		if !ok {
			t.Fatalf("\nwanted DB write type:\n%T\ngot:\n%T", (*domain.WebSocketConnectionUpdate)(nil), updateItem)
		}
		if update.Connection.State != "closed" {
			t.Fatalf("\nwanted state:\n%q\ngot:\n%q", "closed", update.Connection.State)
		}
		if update.Connection.ClosedAt == nil {
			t.Fatalf("\nwanted ClosedAt:\n%v\ngot:\n%v", "initialized", update.Connection.ClosedAt)
		}
		if update.Connection.CloseCode != 1000 {
			t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", 1000, update.Connection.CloseCode)
		}
		if update.Connection.CloseReason != "Goodbye" {
			t.Fatalf("\nwanted close reason:\n%q\ngot:\n%q", "Goodbye", update.Connection.CloseReason)
		}
		select {
		case event := <-closeEvents:
			if event.ID != connectionID {
				t.Fatalf("\nwanted close event connection ID:\n%s\ngot:\n%s", connectionID, event.ID)
			}
			if event.State != "closed" {
				t.Fatalf("\nwanted close event state:\n%q\ngot:\n%q", "closed", event.State)
			}
			if event.CloseCode != 1000 {
				t.Fatalf("\nwanted close event code:\n%d\ngot:\n%d", 1000, event.CloseCode)
			}
			if event.CloseReason != "Goodbye" {
				t.Fatalf("\nwanted close event reason:\n%q\ngot:\n%q", "Goodbye", event.CloseReason)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for websocket close event")
		}
		select {
		case event := <-closeEvents:
			t.Fatalf("\nwanted websocket close event count:\n%d\ngot extra event:\n%v", 1, event)
		default:
		}
		if _, exists := proxy.WebSocketRegistry.Get(connectionID); exists {
			t.Fatalf("\nwanted registered connection exists:\n%v\ngot:\n%v", false, exists)
		}
	})

	t.Run("error lifecycle", func(t *testing.T) {
		connectionID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating connection UUID: %v", err)
		}
		requestID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating request UUID: %v", err)
		}
		readErr := errors.New("client read failed")

		clientConnection, clientPeer := net.Pipe()
		upstreamConnection, upstreamPeer := net.Pipe()
		defer clientPeer.Close()
		defer upstreamPeer.Close()

		proxy := &Proxy{
			WebSocketRegistry: marasiws.NewRegistry(),
			DBWriteChannel:    make(chan any, 2),
		}
		connection := marasiws.NewConnection(
			connectionID,
			requestID,
			&sessionTestErrorReader{err: readErr},
			clientConnection,
			upstreamConnection,
			nil,
		)
		record := domain.WebSocketConnection{
			ID:        connectionID,
			RequestID: requestID,
		}

		runResult := make(chan error, 1)
		go func() {
			runResult <- proxy.runWebSocketSession(connection, record)
		}()

		openItem := receiveSessionDBWrite(t, proxy.DBWriteChannel)
		if _, ok := openItem.(*domain.WebSocketConnection); !ok {
			t.Fatalf("\nwanted DB write type:\n%T\ngot:\n%T", (*domain.WebSocketConnection)(nil), openItem)
		}

		select {
		case sessionErr := <-runResult:
			if !errors.Is(sessionErr, readErr) {
				t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", readErr, sessionErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("\nwanted session state:\n%q\ngot:\n%q", "returned", "blocked")
		}

		updateItem := receiveSessionDBWrite(t, proxy.DBWriteChannel)
		update, ok := updateItem.(*domain.WebSocketConnectionUpdate)
		if !ok {
			t.Fatalf("\nwanted DB write type:\n%T\ngot:\n%T", (*domain.WebSocketConnectionUpdate)(nil), updateItem)
		}
		if update.Connection.State != "error" {
			t.Fatalf("\nwanted state:\n%q\ngot:\n%q", "error", update.Connection.State)
		}
		if update.Connection.ClosedAt == nil {
			t.Fatalf("\nwanted ClosedAt:\n%v\ngot:\n%v", "initialized", update.Connection.ClosedAt)
		}
		if update.Connection.CloseCode != 1006 {
			t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", 1006, update.Connection.CloseCode)
		}
		if _, exists := proxy.WebSocketRegistry.Get(connectionID); exists {
			t.Fatalf("\nwanted registered connection exists:\n%v\ngot:\n%v", false, exists)
		}
	})

	t.Run("connection ID mismatch", func(t *testing.T) {
		connectionID := uuid.New()
		requestID := uuid.New()
		stream := &sessionTestReadWriteCloser{}
		proxy := &Proxy{
			WebSocketRegistry: marasiws.NewRegistry(),
			DBWriteChannel:    make(chan any, 2),
		}
		connection := marasiws.NewConnection(
			connectionID,
			requestID,
			stream,
			stream,
			stream,
			nil,
		)

		err := proxy.runWebSocketSession(connection, domain.WebSocketConnection{
			ID:        uuid.New(),
			RequestID: requestID,
		})
		if err == nil {
			t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "connection ID mismatch", err)
		}
		if !strings.Contains(err.Error(), "connection ID mismatch") {
			t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "connection ID mismatch", err)
		}
		if got := len(proxy.DBWriteChannel); got != 0 {
			t.Fatalf("\nwanted DB write count:\n%d\ngot:\n%d", 0, got)
		}
		if _, exists := proxy.WebSocketRegistry.Get(connectionID); exists {
			t.Fatalf("\nwanted registered connection exists:\n%v\ngot:\n%v", false, exists)
		}
	})

	t.Run("request ID mismatch", func(t *testing.T) {
		connectionID := uuid.New()
		requestID := uuid.New()
		stream := &sessionTestReadWriteCloser{}
		proxy := &Proxy{
			WebSocketRegistry: marasiws.NewRegistry(),
			DBWriteChannel:    make(chan any, 2),
		}
		connection := marasiws.NewConnection(
			connectionID,
			requestID,
			stream,
			stream,
			stream,
			nil,
		)

		err := proxy.runWebSocketSession(connection, domain.WebSocketConnection{
			ID:        connectionID,
			RequestID: uuid.New(),
		})
		if err == nil {
			t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "request ID mismatch", err)
		}
		if !strings.Contains(err.Error(), "request ID mismatch") {
			t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "request ID mismatch", err)
		}
		if got := len(proxy.DBWriteChannel); got != 0 {
			t.Fatalf("\nwanted DB write count:\n%d\ngot:\n%d", 0, got)
		}
		if _, exists := proxy.WebSocketRegistry.Get(connectionID); exists {
			t.Fatalf("\nwanted registered connection exists:\n%v\ngot:\n%v", false, exists)
		}
	})
}

func TestProxy_CloseClosesWebSockets(t *testing.T) {
	client := &sessionTestReadWriteCloser{}
	upstream := &sessionTestReadWriteCloser{}
	connection := marasiws.NewConnection(
		uuid.New(),
		uuid.New(),
		client,
		client,
		upstream,
		nil,
	)
	registry := marasiws.NewRegistry()
	if err := registry.Add(connection); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}

	proxy := &Proxy{
		WebSocketRegistry: registry,
		martianProxy:      martian.NewProxy(),
	}
	proxy.Close()

	if _, exists := registry.Get(connection.ID); exists {
		t.Fatalf("wanted websocket connection to be closed")
	}
	frame, err := marasiws.ReadFrame(bytes.NewReader(client.Bytes()))
	if err != nil {
		t.Fatalf("reading close frame: %v", err)
	}
	code, reason, err := marasiws.ParseClosePayload(frame.Payload)
	if err != nil {
		t.Fatalf("parsing close payload: %v", err)
	}
	if code != marasiws.CloseNormalClosure {
		t.Fatalf("wanted close code:\n%d\ngot:\n%d", marasiws.CloseNormalClosure, code)
	}
	if reason != "" {
		t.Fatalf("wanted close reason:\n%q\ngot:\n%q", "", reason)
	}
}

func TestProxy_CloseReturnsDatabaseError(t *testing.T) {
	want := errors.New("database close failed")
	proxy := &Proxy{
		martianProxy: martian.NewProxy(),
		DBCloser:     &errorCloser{err: want},
	}

	err := proxy.Close()
	if !errors.Is(err, want) {
		t.Fatalf("wanted database close error:\n%v\ngot:\n%v", want, err)
	}
}

func TestProxy_CloseStopsServeCleanly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("creating listener: %v", err)
	}

	proxy := &Proxy{
		martianProxy:      martian.NewProxy(),
		DBWriteChannel:    make(chan any, 1),
		WebSocketRegistry: marasiws.NewRegistry(),
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- proxy.Serve(listener)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		proxy.listenerMu.Lock()
		serving := proxy.activeListener == listener
		proxy.listenerMu.Unlock()
		if serving {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy did not start serving")
		}
		time.Sleep(time.Millisecond)
	}

	proxy.Close()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("Serve() returned unexpected error during shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not stop during shutdown")
	}
	close(proxy.DBWriteChannel)
}

func TestProxy_ActiveListenerAddress(t *testing.T) {
	t.Run("should report no address before serving", func(t *testing.T) {
		proxy := &Proxy{}

		address, active := proxy.ActiveListenerAddress()

		if active || address != "" {
			t.Fatalf("\nwanted:\nno active listener\ngot:\n%q, %t", address, active)
		}
	})

	t.Run("should report the active listener's assigned address", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("creating listener: %v", err)
		}
		proxy := &Proxy{
			martianProxy:      martian.NewProxy(),
			DBWriteChannel:    make(chan any, 1),
			WebSocketRegistry: marasiws.NewRegistry(),
			Addr:              "wrong-address",
			Port:              "1",
		}
		serveResult := make(chan error, 1)
		go func() { serveResult <- proxy.Serve(listener) }()

		deadline := time.Now().Add(5 * time.Second)
		for {
			address, active := proxy.ActiveListenerAddress()
			if active {
				if address != listener.Addr().String() {
					t.Fatalf("\nwanted:\n%s\ngot:\n%s", listener.Addr(), address)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("proxy did not start serving")
			}
			time.Sleep(time.Millisecond)
		}

		if err := proxy.Close(); err != nil {
			t.Fatalf("closing proxy: %v", err)
		}
		if err := <-serveResult; err != nil {
			t.Fatalf("serving proxy: %v", err)
		}
		close(proxy.DBWriteChannel)

		address, active := proxy.ActiveListenerAddress()
		if active || address != "" {
			t.Fatalf("\nwanted:\nno active listener after close\ngot:\n%q, %t", address, active)
		}
	})
}

func TestProxy_GetListenerAcceptsNetListenAddresses(t *testing.T) {
	addresses := []string{"localhost", "127.0.0.1", "0.0.0.0"}
	if probe, err := net.Listen("tcp", net.JoinHostPort("::1", "0")); err == nil {
		probe.Close()
		addresses = append(addresses, "::1")
	}

	for _, address := range addresses {
		t.Run("should bind "+address, func(t *testing.T) {
			proxy, err := New()
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			listener, err := proxy.GetListener(address, "0")
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			defer listener.Close()
		})
	}
}

func TestProxy_CloseWebSocketsClosesActiveSession(t *testing.T) {
	clientConnection, clientPeer := net.Pipe()
	upstreamConnection, upstreamPeer := net.Pipe()
	t.Cleanup(func() {
		clientConnection.Close()
		clientPeer.Close()
		upstreamConnection.Close()
		upstreamPeer.Close()
	})
	if err := clientPeer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting client peer deadline: %v", err)
	}
	if err := upstreamPeer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting upstream peer deadline: %v", err)
	}

	proxy := &Proxy{
		WebSocketRegistry: marasiws.NewRegistry(),
		DBWriteChannel:    make(chan any, 4),
	}
	connection := marasiws.NewConnection(
		uuid.New(),
		uuid.New(),
		clientConnection,
		clientConnection,
		upstreamConnection,
		func(message *marasiws.Message) error {
			snapshot := message.ToDomain()
			proxy.DBWriteChannel <- &snapshot
			return nil
		},
	)
	runResult := make(chan error, 1)
	go func() {
		runResult <- proxy.runWebSocketSession(connection, domain.WebSocketConnection{
			ID:        connection.ID,
			RequestID: connection.RequestID,
		})
	}()

	openItem := receiveSessionDBWrite(t, proxy.DBWriteChannel)
	if _, ok := openItem.(*domain.WebSocketConnection); !ok {
		t.Fatalf("wanted websocket open write, got %T", openItem)
	}

	type frameResult struct {
		frame marasiws.Frame
		err   error
	}
	clientFrame := make(chan frameResult, 1)
	go func() {
		frame, err := marasiws.ReadFrame(clientPeer)
		clientFrame <- frameResult{frame: frame, err: err}
	}()
	upstreamFrame := make(chan frameResult, 1)
	go func() {
		frame, err := marasiws.ReadFrame(upstreamPeer)
		if err == nil {
			err = marasiws.WriteFrame(upstreamPeer, frame, false)
		}
		upstreamFrame <- frameResult{frame: frame, err: err}
	}()

	if err := proxy.CloseWebSockets(marasiws.CloseGoingAway, "listener stopped"); err != nil {
		t.Fatalf("CloseWebSockets() returned unexpected error: %v", err)
	}
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("runWebSocketSession() returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active websocket session did not stop")
	}

	for _, wantDirection := range []string{marasiws.DirectionFromClient, marasiws.DirectionFromServer} {
		item := receiveSessionDBWrite(t, proxy.DBWriteChannel)
		message, ok := item.(*domain.WebSocketMessage)
		if !ok {
			t.Fatalf("wanted websocket close message, got %T", item)
		}
		if message.Direction != wantDirection {
			t.Fatalf("\nwanted close direction:\n%q\ngot:\n%q", wantDirection, message.Direction)
		}
		if message.Opcode != marasiws.OpClose {
			t.Fatalf("\nwanted close opcode:\n%d\ngot:\n%d", marasiws.OpClose, message.Opcode)
		}
		code, reason, err := marasiws.ParseClosePayload(message.Payload)
		if err != nil {
			t.Fatalf("parsing persisted close payload: %v", err)
		}
		if code != marasiws.CloseGoingAway || reason != "listener stopped" {
			t.Fatalf("\nwanted close details:\n%d %q\ngot:\n%d %q", marasiws.CloseGoingAway, "listener stopped", code, reason)
		}
		generated, _ := message.Metadata["generated"].(bool)
		if generated != (wantDirection == marasiws.DirectionFromClient) {
			t.Fatalf(
				"\nwanted generated metadata:\n%v\ngot:\n%v",
				wantDirection == marasiws.DirectionFromClient,
				message.Metadata["generated"],
			)
		}
	}
	item := receiveSessionDBWrite(t, proxy.DBWriteChannel)
	if _, ok := item.(*domain.WebSocketConnectionUpdate); !ok {
		t.Fatalf("wanted websocket connection update, got %T", item)
	}

	for name, resultChannel := range map[string]<-chan frameResult{
		"client":   clientFrame,
		"upstream": upstreamFrame,
	} {
		select {
		case result := <-resultChannel:
			if result.err != nil {
				t.Fatalf("reading %s close frame: %v", name, result.err)
			}
			if result.frame.Opcode != marasiws.OpClose {
				t.Fatalf("wanted %s close opcode:\n%d\ngot:\n%d", name, marasiws.OpClose, result.frame.Opcode)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out reading %s close frame", name)
		}
	}

	if _, exists := proxy.WebSocketRegistry.Get(connection.ID); exists {
		t.Fatalf("wanted websocket connection to be unregistered")
	}
}

func TestProxy_ProjectSwitchDoesNotWriteWebSocketUpdateToNewRepository(t *testing.T) {
	clientConnection, clientPeer := net.Pipe()
	upstreamConnection, upstreamPeer := net.Pipe()
	t.Cleanup(func() {
		clientConnection.Close()
		clientPeer.Close()
		upstreamConnection.Close()
		upstreamPeer.Close()
	})
	if err := clientPeer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting client peer deadline: %v", err)
	}
	if err := upstreamPeer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting upstream peer deadline: %v", err)
	}
	clientFrame := make(chan error, 1)
	go func() {
		_, err := marasiws.ReadFrame(clientPeer)
		clientFrame <- err
	}()
	upstreamFrame := make(chan error, 1)
	go func() {
		_, err := marasiws.ReadFrame(upstreamPeer)
		upstreamFrame <- err
	}()

	oldRepository := &recordingWebSocketRepository{
		updates: make(chan *domain.WebSocketConnection, 1),
	}
	newRepository := &recordingWebSocketRepository{
		updates: make(chan *domain.WebSocketConnection, 1),
	}
	opened := make(chan struct{})
	proxy := &Proxy{
		WebSocketRegistry: marasiws.NewRegistry(),
		DBWriteChannel:    make(chan any, 4),
		WebSocketRepo:     oldRepository,
		OnWebSocketOpen: func(domain.WebSocketConnection) error {
			close(opened)
			return nil
		},
	}
	proxy.dbWriterStarted.Store(true)
	writerDone := make(chan struct{})
	go func() {
		proxy.WriteToDB()
		close(writerDone)
	}()
	connection := marasiws.NewConnection(
		uuid.New(),
		uuid.New(),
		clientConnection,
		clientConnection,
		upstreamConnection,
		nil,
	)
	runResult := make(chan error, 1)
	go func() {
		runResult <- proxy.runWebSocketSession(connection, domain.WebSocketConnection{
			ID:        connection.ID,
			RequestID: connection.RequestID,
		})
	}()

	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket session did not open")
	}

	if err := proxy.CloseWebSocketsAndFlush(); err != nil {
		t.Fatalf("CloseWebSockets() returned unexpected error: %v", err)
	}
	select {
	case update := <-oldRepository.updates:
		if update.ID != connection.ID {
			t.Fatalf("wanted old websocket update for %s, got %s", connection.ID, update.ID)
		}
	default:
		t.Fatal("old websocket update was not written to the old repository")
	}
	proxy.WebSocketRepo = newRepository

	close(proxy.DBWriteChannel)
	select {
	case <-writerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("database writer did not finish")
	}
	select {
	case <-runResult:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket session did not finish after project switch")
	}

	select {
	case update := <-newRepository.updates:
		t.Fatalf("old websocket update was written to the new repository: %v", update)
	default:
	}

	for name, result := range map[string]<-chan error{
		"client":   clientFrame,
		"upstream": upstreamFrame,
	} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("reading %s close frame: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out reading %s close frame", name)
		}
	}
}

func TestProxy_WebSocketControlAPIs(t *testing.T) {
	newLiveConnection := func(t *testing.T, process marasiws.ProcessFunc) (*Proxy, *marasiws.Connection, net.Conn, net.Conn) {
		t.Helper()

		connectionID := uuid.New()
		requestID := uuid.New()
		clientConnection, clientPeer := net.Pipe()
		upstreamConnection, upstreamPeer := net.Pipe()
		t.Cleanup(func() {
			clientConnection.Close()
			clientPeer.Close()
			upstreamConnection.Close()
			upstreamPeer.Close()
		})

		connection := marasiws.NewConnection(
			connectionID,
			requestID,
			clientConnection,
			clientConnection,
			upstreamConnection,
			process,
		)
		record := &domain.WebSocketConnection{
			ID:        connectionID,
			RequestID: requestID,
			State:     "open",
			Transport: "ws",
			Host:      "marasi.app",
			Path:      "/socket",
		}
		proxy := &Proxy{
			WebSocketRegistry: marasiws.NewRegistry(),
			WebSocketRepo: &websocketRepositoryStub{
				connection: record,
				messages: []*domain.WebSocketMessage{
					{ID: uuid.New(), ConnectionID: connectionID},
				},
			},
		}
		if err := proxy.WebSocketRegistry.Add(connection); err != nil {
			t.Fatalf("registering websocket connection: %v", err)
		}

		return proxy, connection, clientPeer, upstreamPeer
	}

	t.Run("get connection and message history by request ID", func(t *testing.T) {
		proxy, connection, _, _ := newLiveConnection(t, nil)

		record, exists := proxy.GetWebSocketConnection(connection.RequestID)
		if !exists {
			t.Fatalf("wanted live connection exists: true\ngot: false")
		}
		if record.ID != connection.ID {
			t.Fatalf("wanted: %s\ngot: %s", connection.ID, record.ID)
		}
		if record.Host != "marasi.app" {
			t.Fatalf("wanted: %q\ngot: %q", "marasi.app", record.Host)
		}

		messages, err := proxy.GetWebSocketMessages(connection.RequestID)
		if err != nil {
			t.Fatalf("getting websocket messages: %v", err)
		}
		if len(messages) != 1 {
			t.Fatalf("wanted: %d\ngot: %d", 1, len(messages))
		}
		if messages[0].ConnectionID != connection.ID {
			t.Fatalf("wanted: %s\ngot: %s", connection.ID, messages[0].ConnectionID)
		}
	})

	t.Run("inject message from client to upstream", func(t *testing.T) {
		var processed *domain.WebSocketMessage
		proxy, connection, _, upstreamPeer := newLiveConnection(t, func(message *marasiws.Message) error {
			if message.RequestID == uuid.Nil {
				t.Fatalf("wanted request ID to be set on injected message")
			}
			snapshot := message.ToDomain()
			processed = &snapshot
			return nil
		})
		result := make(chan error, 1)
		go func() {
			result <- proxy.InjectWebSocketMessage(
				connection.RequestID,
				marasiws.DirectionFromClient,
				marasiws.OpText,
				[]byte("to upstream"),
			)
		}()

		frame, err := marasiws.ReadFrame(upstreamPeer)
		if err != nil {
			t.Fatalf("reading injected upstream frame: %v", err)
		}
		if !frame.Masked {
			t.Fatalf("wanted masked: true\ngot: false")
		}
		if string(frame.Payload) != "to upstream" {
			t.Fatalf("wanted: %q\ngot: %q", "to upstream", frame.Payload)
		}
		if err := <-result; err != nil {
			t.Fatalf("injecting upstream message: %v", err)
		}
		if processed == nil {
			t.Fatalf("wanted processed injected message")
		}
		if processed.Metadata["injected"] != true {
			t.Fatalf("wanted injected metadata: true\ngot: %v", processed.Metadata["injected"])
		}
		if processed.ConnectionID != connection.ID {
			t.Fatalf("wanted connection ID: %s\ngot: %s", connection.ID, processed.ConnectionID)
		}
	})

	t.Run("inject message from server to client", func(t *testing.T) {
		proxy, connection, clientPeer, _ := newLiveConnection(t, nil)
		result := make(chan error, 1)
		go func() {
			result <- proxy.InjectWebSocketMessage(
				connection.RequestID,
				marasiws.DirectionFromServer,
				marasiws.OpBinary,
				[]byte("to client"),
			)
		}()

		frame, err := marasiws.ReadFrame(clientPeer)
		if err != nil {
			t.Fatalf("reading injected client frame: %v", err)
		}
		if frame.Masked {
			t.Fatalf("wanted masked: false\ngot: true")
		}
		if string(frame.Payload) != "to client" {
			t.Fatalf("wanted: %q\ngot: %q", "to client", frame.Payload)
		}
		if err := <-result; err != nil {
			t.Fatalf("injecting client message: %v", err)
		}
	})

	t.Run("close sends normal close frame to both peers", func(t *testing.T) {
		proxy, connection, clientPeer, upstreamPeer := newLiveConnection(t, nil)
		result := make(chan error, 1)
		go func() {
			result <- proxy.CloseWebSocket(connection.RequestID, 0, "Goodbye")
		}()

		clientFrame, err := marasiws.ReadFrame(clientPeer)
		if err != nil {
			t.Fatalf("reading client close frame: %v", err)
		}
		upstreamFrame, err := marasiws.ReadFrame(upstreamPeer)
		if err != nil {
			t.Fatalf("reading upstream close frame: %v", err)
		}
		if clientFrame.Opcode != marasiws.OpClose || upstreamFrame.Opcode != marasiws.OpClose {
			t.Fatalf("wanted close opcodes: %d\ngot: %d and %d", marasiws.OpClose, clientFrame.Opcode, upstreamFrame.Opcode)
		}
		code, reason, err := marasiws.ParseClosePayload(clientFrame.Payload)
		if err != nil {
			t.Fatalf("parsing close payload: %v", err)
		}
		if code != 1000 || reason != "Goodbye" {
			t.Fatalf("wanted: %d %q\ngot: %d %q", 1000, "Goodbye", code, reason)
		}
		if err := <-result; err != nil {
			t.Fatalf("closing websocket: %v", err)
		}
	})

	t.Run("missing connection and repository return errors", func(t *testing.T) {
		proxy := &Proxy{WebSocketRegistry: marasiws.NewRegistry()}
		requestID := uuid.New()

		if _, exists := proxy.GetWebSocketConnection(requestID); exists {
			t.Fatalf("wanted live connection exists: false\ngot: true")
		}
		if err := proxy.InjectWebSocketMessage(requestID, marasiws.DirectionFromClient, marasiws.OpText, nil); !errors.Is(err, ErrWebSocketConnectionNotFound) {
			t.Fatalf("wanted: %v\ngot: %v", ErrWebSocketConnectionNotFound, err)
		}
		if err := proxy.CloseWebSocket(requestID, 1000, ""); !errors.Is(err, ErrWebSocketConnectionNotFound) {
			t.Fatalf("wanted: %v\ngot: %v", ErrWebSocketConnectionNotFound, err)
		}
		if _, err := proxy.GetWebSocketMessages(requestID); !errors.Is(err, ErrWebSocketRepositoryNotSet) {
			t.Fatalf("wanted: %v\ngot: %v", ErrWebSocketRepositoryNotSet, err)
		}
	})

	t.Run("invalid injection direction returns an error", func(t *testing.T) {
		proxy, connection, _, _ := newLiveConnection(t, nil)

		err := proxy.InjectWebSocketMessage(connection.RequestID, "invalid", marasiws.OpText, nil)
		if !errors.Is(err, ErrWebSocketDirection) {
			t.Fatalf("wanted: %v\ngot: %v", ErrWebSocketDirection, err)
		}
	})
}

func TestProxy_WebSocketIntegration(t *testing.T) {
	t.Run("ws", func(t *testing.T) {
		testProxyWebSocketIntegration(t, false, false, false, false)
	})
	t.Run("wss", func(t *testing.T) {
		testProxyWebSocketIntegration(t, true, false, false, false)
	})
	t.Run("ws interception", func(t *testing.T) {
		testProxyWebSocketIntegration(t, false, true, true, false)
	})
	t.Run("ws interception without handler", func(t *testing.T) {
		testProxyWebSocketIntegration(t, false, true, false, false)
	})
	t.Run("ws checkpoint interception", func(t *testing.T) {
		testProxyWebSocketIntegration(t, false, false, true, true)
	})
}

func testProxyWebSocketIntegration(
	t *testing.T,
	secure bool,
	intercept bool,
	interceptHandler bool,
	checkpointIntercept bool,
) {
	t.Helper()

	originErrors := make(chan error, 1)
	originHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			originErrors <- fmt.Errorf("response writer does not support hijacking")
			return
		}

		connection, buffered, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			originErrors <- fmt.Errorf("hijacking origin connection: %w", hijackErr)
			return
		}
		defer connection.Close()

		key := req.Header.Get("Sec-WebSocket-Key")
		accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		if _, writeErr := fmt.Fprintf(
			buffered,
			"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(accept[:]),
		); writeErr != nil {
			originErrors <- fmt.Errorf("writing origin handshake: %w", writeErr)
			return
		}
		if flushErr := buffered.Flush(); flushErr != nil {
			originErrors <- fmt.Errorf("flushing origin handshake: %w", flushErr)
			return
		}

		for {
			frame, readErr := marasiws.ReadFrame(buffered)
			if readErr != nil {
				return
			}

			if frame.Opcode == marasiws.OpClose {
				return
			}

			if writeErr := marasiws.WriteFrame(buffered, frame, false); writeErr != nil {
				originErrors <- fmt.Errorf("echoing origin frame: %w", writeErr)
				return
			}
			if flushErr := buffered.Flush(); flushErr != nil {
				originErrors <- fmt.Errorf("flushing origin frame: %w", flushErr)
				return
			}
		}
	})
	var origin *httptest.Server
	if secure {
		origin = httptest.NewTLSServer(originHandler)
	} else {
		origin = httptest.NewServer(originHandler)
	}
	defer origin.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConnection, dbErr := db.New(":memory:", logger)
	if dbErr != nil {
		t.Fatalf("creating in-memory database: %v", dbErr)
	}
	repository := db.NewProxyRepo(dbConnection)
	defaultExtensions, extensionsErr := repository.GetExtensions()
	if extensionsErr != nil {
		repository.Close()
		t.Fatalf("getting default extensions: %v", extensionsErr)
	}
	if checkpointIntercept {
		for _, extension := range defaultExtensions {
			if extension.Name != "checkpoint" {
				continue
			}
			extension.LuaContent = `
				function interceptWebSocketMessage(message)
					return message:opcode() == 1
				end
			`
		}
	}

	requestIDs := make(chan uuid.UUID, 1)
	messageEvents := make(chan domain.WebSocketMessage, 4)
	interceptionEvents := make(chan domain.WebSocketMessage, 4)
	proxyOptions := []func(*Proxy) error{
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
	}
	if secure {
		proxyOptions = append(proxyOptions, WithTLS())
	}
	proxyOptions = append(proxyOptions,
		WithExtensions(defaultExtensions),
		WithRequestHandler(func(req domain.ProxyRequest) error {
			requestIDs <- req.ID
			return nil
		}),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithWebSocketMessageHandler(func(message domain.WebSocketMessage) error {
			messageEvents <- message
			return nil
		}),
		WithBasePipeline(),
		WithDefaultModifierPipeline(),
	)
	var proxy *Proxy
	if interceptHandler {
		proxyOptions = append(proxyOptions, WithWebSocketInterceptHandler(func(message domain.WebSocketMessage) error {
			interceptionEvents <- message
			return proxy.ResolveWebSocketInterception(message.ID, marasiws.InterceptionDecision{
				Resume:  true,
				Opcode:  message.Opcode,
				Payload: message.Payload,
			})
		}))
	}
	proxy, proxyErr := New(proxyOptions...)
	if proxyErr != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", proxyErr)
	}
	proxy.SetWebSocketIntercept(intercept)
	if intercept && !interceptHandler {
		stopForward := make(chan struct{})
		defer close(stopForward)
		go func() {
			for {
				select {
				case <-stopForward:
					return
				default:
					for _, message := range proxy.GetPendingWebSocketInterceptions() {
						_ = proxy.ForwardCheckpoint(message.ID, CheckpointForward{})
					}
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	proxyListener, listenerErr := proxy.GetListener("127.0.0.1", "0")
	if listenerErr != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", listenerErr)
	}
	serveResult := make(chan error, 1)
	if secure {
		roundTripper := newMarasiTransport(proxy.Cert)
		marasiTransport := roundTripper.(*marasiRoundTripper)
		upstreamTransport := marasiTransport.base.(*http.Transport)
		upstreamTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		proxy.martianProxy.SetRoundTripper(roundTripper)
		go proxy.WriteToDB()
		go func() {
			serveResult <- proxy.martianProxy.Serve(proxyListener)
		}()
	} else {
		go func() {
			serveResult <- proxy.Serve(proxyListener)
		}()
	}
	defer func() {
		proxyListener.Close()
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
		}
	}()

	proxyAddress := net.JoinHostPort(proxy.Addr, proxy.Port)
	client, dialErr := net.Dial("tcp", proxyAddress)
	if dialErr != nil {
		t.Fatalf("dialing proxy: %v", dialErr)
	}
	defer client.Close()
	if deadlineErr := client.SetDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
		t.Fatalf("setting client deadline: %v", deadlineErr)
	}

	originAddress := strings.TrimPrefix(strings.TrimPrefix(origin.URL, "http://"), "https://")
	requestTarget := origin.URL + "/socket"
	clientConnection := net.Conn(client)
	if secure {
		_, originPort, splitErr := net.SplitHostPort(originAddress)
		if splitErr != nil {
			t.Fatalf("splitting origin address: %v", splitErr)
		}
		originAddress = net.JoinHostPort("localhost", originPort)
		connectBuffer := bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client))
		if _, writeErr := fmt.Fprintf(
			connectBuffer,
			"CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n",
			originAddress,
			originAddress,
		); writeErr != nil {
			t.Fatalf("writing CONNECT request: %v", writeErr)
		}
		if flushErr := connectBuffer.Flush(); flushErr != nil {
			t.Fatalf("flushing CONNECT request: %v", flushErr)
		}
		connectResponse, responseErr := http.ReadResponse(connectBuffer.Reader, &http.Request{Method: http.MethodConnect})
		if responseErr != nil {
			t.Fatalf("reading CONNECT response: %v", responseErr)
		}
		if connectResponse.StatusCode != http.StatusOK {
			t.Fatalf("wanted: %d\ngot: %d", http.StatusOK, connectResponse.StatusCode)
		}

		rootCAs := x509.NewCertPool()
		rootCAs.AddCert(proxy.Cert)
		tlsClient := tls.Client(client, &tls.Config{
			RootCAs:    rootCAs,
			ServerName: "localhost",
		})
		if handshakeErr := tlsClient.Handshake(); handshakeErr != nil {
			t.Fatalf("performing TLS handshake with proxy: %v", handshakeErr)
		}
		clientConnection = tlsClient
		requestTarget = "/socket"
	}

	clientBuffer := bufio.NewReadWriter(bufio.NewReader(clientConnection), bufio.NewWriter(clientConnection))
	if _, writeErr := fmt.Fprintf(
		clientBuffer,
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n",
		requestTarget,
		originAddress,
	); writeErr != nil {
		t.Fatalf("writing upgrade request: %v", writeErr)
	}
	if flushErr := clientBuffer.Flush(); flushErr != nil {
		t.Fatalf("flushing upgrade request: %v", flushErr)
	}

	statusLine, statusErr := clientBuffer.ReadString('\n')
	if statusErr != nil {
		t.Fatalf("reading upgrade status: %v", statusErr)
	}
	if statusLine != "HTTP/1.1 101 Switching Protocols\r\n" {
		t.Fatalf("wanted: %q\ngot: %q", "HTTP/1.1 101 Switching Protocols\r\n", statusLine)
	}
	for {
		headerLine, headerErr := clientBuffer.ReadString('\n')
		if headerErr != nil {
			t.Fatalf("reading upgrade header: %v", headerErr)
		}
		if headerLine == "\r\n" {
			break
		}
	}

	textFrame := marasiws.Frame{
		Fin:     true,
		Opcode:  marasiws.OpText,
		Payload: []byte("Hello from Marasi"),
	}
	if writeErr := marasiws.WriteFrame(clientBuffer, textFrame, true); writeErr != nil {
		t.Fatalf("writing text frame: %v", writeErr)
	}
	if flushErr := clientBuffer.Flush(); flushErr != nil {
		t.Fatalf("flushing text frame: %v", flushErr)
	}

	echo, echoErr := marasiws.ReadFrame(clientBuffer)
	if echoErr != nil {
		t.Fatalf("reading echoed frame: %v", echoErr)
	}
	if echo.Opcode != marasiws.OpText {
		t.Fatalf("wanted: %d\ngot: %d", marasiws.OpText, echo.Opcode)
	}
	if string(echo.Payload) != "Hello from Marasi" {
		t.Fatalf("wanted: %q\ngot: %q", "Hello from Marasi", string(echo.Payload))
	}

	var requestID uuid.UUID
	select {
	case requestID = <-requestIDs:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for request ID")
	}

	wantedTransport := "ws"
	if secure {
		wantedTransport = "wss"
	}

	openDeadline := time.Now().Add(5 * time.Second)
	var openMetadata map[string]any
	for time.Now().Before(openDeadline) {
		metadata, metadataErr := repository.GetMetadata(requestID)
		if metadataErr == nil && metadata["websocket.state"] == "open" {
			openMetadata = metadata
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if openMetadata == nil {
		t.Fatalf("wanted open websocket metadata before close")
	}
	if openMetadata["protocol"] != "websocket" {
		t.Fatalf("wanted: %q\ngot: %q", "websocket", openMetadata["protocol"])
	}
	if openMetadata["websocket.transport"] != wantedTransport {
		t.Fatalf("wanted: %q\ngot: %q", wantedTransport, openMetadata["websocket.transport"])
	}

	closePayload, closePayloadErr := marasiws.EncodeClosePayload(1000, "Goodbye")
	if closePayloadErr != nil {
		t.Fatalf("encoding close payload: %v", closePayloadErr)
	}
	if writeErr := marasiws.WriteFrame(clientBuffer, marasiws.Frame{
		Fin:     true,
		Opcode:  marasiws.OpClose,
		Payload: closePayload,
	}, true); writeErr != nil {
		t.Fatalf("writing close frame: %v", writeErr)
	}
	if flushErr := clientBuffer.Flush(); flushErr != nil {
		t.Fatalf("flushing close frame: %v", flushErr)
	}

	deadline := time.Now().Add(5 * time.Second)
	var connectionRecord *domain.WebSocketConnection
	var messages []*domain.WebSocketMessage
	for time.Now().Before(deadline) {
		connectionRecord, dbErr = repository.GetConnectionByRequestID(requestID)
		if dbErr == nil && connectionRecord.State == "closed" {
			messages, dbErr = repository.GetMessagesByRequestID(requestID)
			if dbErr == nil && len(messages) >= 3 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	if connectionRecord == nil {
		t.Fatalf("wanted persisted connection\ngot: nil")
	}
	if connectionRecord.State != "closed" {
		t.Fatalf("wanted: %q\ngot: %q", "closed", connectionRecord.State)
	}
	if connectionRecord.CloseCode != 1000 {
		t.Fatalf("wanted: %d\ngot: %d", 1000, connectionRecord.CloseCode)
	}
	if connectionRecord.CloseReason != "Goodbye" {
		t.Fatalf("wanted: %q\ngot: %q", "Goodbye", connectionRecord.CloseReason)
	}
	if connectionRecord.Transport != wantedTransport {
		t.Fatalf("wanted: %q\ngot: %q", wantedTransport, connectionRecord.Transport)
	}
	if len(messages) < 3 {
		t.Fatalf("wanted at least 3 messages\ngot: %d", len(messages))
	}

	closedMetadata, closedMetadataErr := repository.GetMetadata(requestID)
	if closedMetadataErr != nil {
		t.Fatalf("getting request metadata: %v", closedMetadataErr)
	}
	if closedMetadata["protocol"] != "websocket" {
		t.Fatalf("wanted: %q\ngot: %q", "websocket", closedMetadata["protocol"])
	}
	if closedMetadata["websocket.transport"] != wantedTransport {
		t.Fatalf("wanted: %q\ngot: %q", wantedTransport, closedMetadata["websocket.transport"])
	}
	if closedMetadata["websocket.state"] != "closed" {
		t.Fatalf("wanted: %q\ngot: %q", "closed", closedMetadata["websocket.state"])
	}

	var clientText, serverText bool
	for _, message := range messages {
		if message.Opcode != marasiws.OpText || string(message.Payload) != "Hello from Marasi" {
			continue
		}
		if message.Direction == marasiws.DirectionFromClient {
			clientText = true
		}
		if message.Direction == marasiws.DirectionFromServer {
			serverText = true
		}
		if interceptHandler && message.Metadata["intercepted"] != true {
			t.Fatalf("wanted intercepted metadata: true\ngot: %v", message.Metadata["intercepted"])
		}
	}
	if !clientText {
		t.Fatalf("wanted client text message: true\ngot: false")
	}
	if !serverText {
		t.Fatalf("wanted server text message: true\ngot: false")
	}

	var clientTextEvents, serverTextEvents int

messageEventLoop:
	for {
		select {
		case message := <-messageEvents:
			if message.Opcode != marasiws.OpText || string(message.Payload) != "Hello from Marasi" {
				continue
			}
			if message.Direction == marasiws.DirectionFromClient {
				clientTextEvents++
			}
			if message.Direction == marasiws.DirectionFromServer {
				serverTextEvents++
			}
		default:
			break messageEventLoop
		}
	}
	if clientTextEvents != 1 {
		t.Fatalf("wanted client text message event count: %d\ngot: %d", 1, clientTextEvents)
	}
	if serverTextEvents != 1 {
		t.Fatalf("wanted server text message event count: %d\ngot: %d", 1, serverTextEvents)
	}

	var clientInterceptEvents, serverInterceptEvents int

interceptionEventLoop:
	for {
		select {
		case message := <-interceptionEvents:
			if message.Opcode != marasiws.OpText || string(message.Payload) != "Hello from Marasi" {
				continue
			}
			if message.Direction == marasiws.DirectionFromClient {
				clientInterceptEvents++
			}
			if message.Direction == marasiws.DirectionFromServer {
				serverInterceptEvents++
			}
		default:
			break interceptionEventLoop
		}
	}
	if interceptHandler {
		if clientInterceptEvents != 1 {
			t.Fatalf("wanted client interception event count: %d\ngot: %d", 1, clientInterceptEvents)
		}
		if serverInterceptEvents != 1 {
			t.Fatalf("wanted server interception event count: %d\ngot: %d", 1, serverInterceptEvents)
		}
	} else if clientInterceptEvents != 0 || serverInterceptEvents != 0 {
		t.Fatalf("wanted interception event count: %d\ngot: %d", 0, clientInterceptEvents+serverInterceptEvents)
	}
	if pending := proxy.GetPendingWebSocketInterceptions(); len(pending) != 0 {
		t.Fatalf("wanted pending interception count: %d\ngot: %d", 0, len(pending))
	}

	select {
	case originErr := <-originErrors:
		t.Fatalf("origin server returned an error: %v", originErr)
	default:
	}
}

type launchpadTestBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *launchpadTestBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *launchpadTestBody) Close() error {
	b.once.Do(func() {
		close(b.closed)
	})
	return nil
}

func TestProxy_HoldLaunchpadWebSocket(t *testing.T) {
	t.Run("hold drains and tracks body until close", func(t *testing.T) {
		proxy := &Proxy{
			launchpadWS: make(map[io.Closer]struct{}),
		}
		body := &launchpadTestBody{closed: make(chan struct{})}

		proxy.holdLaunchpadWebSocket(body)

		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			proxy.launchpadWSMu.Lock()
			count := len(proxy.launchpadWS)
			proxy.launchpadWSMu.Unlock()
			if count == 1 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		proxy.launchpadWSMu.Lock()
		count := len(proxy.launchpadWS)
		proxy.launchpadWSMu.Unlock()
		if count != 1 {
			t.Fatalf("wanted held body count: %d\ngot: %d", 1, count)
		}

		proxy.closeLaunchpadWebSockets()

		select {
		case <-body.closed:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for held body close")
		}

		deadline = time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			proxy.launchpadWSMu.Lock()
			count = len(proxy.launchpadWS)
			proxy.launchpadWSMu.Unlock()
			if count == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		proxy.launchpadWSMu.Lock()
		count = len(proxy.launchpadWS)
		proxy.launchpadWSMu.Unlock()
		if count != 0 {
			t.Fatalf("wanted held body count: %d\ngot: %d", 0, count)
		}
	})

	t.Run("nil body is ignored", func(t *testing.T) {
		proxy := &Proxy{
			launchpadWS: make(map[io.Closer]struct{}),
		}
		proxy.holdLaunchpadWebSocket(nil)
		if len(proxy.launchpadWS) != 0 {
			t.Fatalf("wanted held body count: %d\ngot: %d", 0, len(proxy.launchpadWS))
		}
	})
}

func TestProxy_LaunchWebSocket(t *testing.T) {
	originErrors := make(chan error, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			originErrors <- fmt.Errorf("response writer does not support hijacking")
			return
		}

		connection, buffered, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			originErrors <- fmt.Errorf("hijacking origin connection: %w", hijackErr)
			return
		}
		defer connection.Close()

		key := req.Header.Get("Sec-WebSocket-Key")
		accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		if _, writeErr := fmt.Fprintf(
			buffered,
			"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(accept[:]),
		); writeErr != nil {
			originErrors <- fmt.Errorf("writing origin handshake: %w", writeErr)
			return
		}
		if flushErr := buffered.Flush(); flushErr != nil {
			originErrors <- fmt.Errorf("flushing origin handshake: %w", flushErr)
			return
		}

		for {
			frame, readErr := marasiws.ReadFrame(buffered)
			if readErr != nil {
				return
			}
			if frame.Opcode == marasiws.OpClose {
				_ = marasiws.WriteFrame(buffered, frame, false)
				_ = buffered.Flush()
				return
			}
			if writeErr := marasiws.WriteFrame(buffered, frame, false); writeErr != nil {
				originErrors <- fmt.Errorf("echoing origin frame: %w", writeErr)
				return
			}
			if flushErr := buffered.Flush(); flushErr != nil {
				originErrors <- fmt.Errorf("flushing origin frame: %w", flushErr)
				return
			}
		}
	}))
	defer origin.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConnection, dbErr := db.New(":memory:", logger)
	if dbErr != nil {
		t.Fatalf("creating in-memory database: %v", dbErr)
	}
	repository := db.NewProxyRepo(dbConnection)
	defaultExtensions, extensionsErr := repository.GetExtensions()
	if extensionsErr != nil {
		repository.Close()
		t.Fatalf("getting default extensions: %v", extensionsErr)
	}

	launchpadID, launchpadErr := repository.CreateLaunchpad("websocket", "launch websocket")
	if launchpadErr != nil {
		repository.Close()
		t.Fatalf("creating launchpad: %v", launchpadErr)
	}

	requestIDs := make(chan uuid.UUID, 1)
	proxy, proxyErr := New(
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithExtensions(defaultExtensions),
		WithRequestHandler(func(req domain.ProxyRequest) error {
			requestIDs <- req.ID
			return nil
		}),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithBasePipeline(),
		WithDefaultModifierPipeline(),
	)
	if proxyErr != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", proxyErr)
	}

	proxyListener, listenerErr := proxy.GetListener("127.0.0.1", "0")
	if listenerErr != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", listenerErr)
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- proxy.Serve(proxyListener)
	}()
	defer func() {
		proxyListener.Close()
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
		}
	}()

	originAddress := strings.TrimPrefix(origin.URL, "http://")
	raw := fmt.Sprintf(
		"GET /socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n",
		originAddress,
	)

	launchResult := make(chan error, 1)
	go func() {
		launchResult <- proxy.Launch(raw, launchpadID.String(), false)
	}()

	select {
	case err := <-launchResult:
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Launch")
	}

	var requestID uuid.UUID
	select {
	case requestID = <-requestIDs:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for request ID")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, exists := proxy.GetWebSocketConnection(requestID); exists {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, exists := proxy.GetWebSocketConnection(requestID); !exists {
		t.Fatalf("wanted live websocket connection after Launch")
	}

	if err := proxy.InjectWebSocketMessage(
		requestID,
		marasiws.DirectionFromClient,
		marasiws.OpText,
		[]byte("launchpad"),
	); err != nil {
		t.Fatalf("injecting websocket message: %v", err)
	}

	openDeadline := time.Now().Add(5 * time.Second)
	var openMetadata map[string]any
	for time.Now().Before(openDeadline) {
		metadata, metadataErr := repository.GetMetadata(requestID)
		if metadataErr == nil && metadata["websocket.state"] == "open" {
			openMetadata = metadata
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if openMetadata == nil {
		t.Fatalf("wanted open websocket metadata after Launch")
	}
	if openMetadata["protocol"] != "websocket" {
		t.Fatalf("wanted: %q\ngot: %q", "websocket", openMetadata["protocol"])
	}
	if openMetadata["websocket.transport"] != "ws" {
		t.Fatalf("wanted: %q\ngot: %q", "ws", openMetadata["websocket.transport"])
	}

	if err := proxy.CloseWebSocket(requestID, 1000, "done"); err != nil {
		t.Fatalf("closing websocket: %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, exists := proxy.GetWebSocketConnection(requestID); !exists {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, exists := proxy.GetWebSocketConnection(requestID); exists {
		t.Fatalf("wanted websocket connection removed after close")
	}

	closedMetadata, closedMetadataErr := repository.GetMetadata(requestID)
	if closedMetadataErr != nil {
		t.Fatalf("getting closed request metadata: %v", closedMetadataErr)
	}
	if closedMetadata["websocket.state"] != "closed" {
		t.Fatalf("wanted: %q\ngot: %q", "closed", closedMetadata["websocket.state"])
	}

	select {
	case originErr := <-originErrors:
		t.Fatalf("origin server returned an error: %v", originErr)
	default:
	}
}
