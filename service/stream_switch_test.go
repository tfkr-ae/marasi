package service

import (
	"context"
	"crypto/tls"
	"encoding/binary"
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
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestProjectLifecycle_StreamedPairs(t *testing.T) {
	t.Run("should cancel a live streamed pair on a project switch and store its messages in the project it started in", func(t *testing.T) {
		// A gRPC origin that answers each message with "reply" and ends
		// only once the request does.
		origin := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/grpc")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			for {
				var prefix [5]byte
				if _, err := io.ReadFull(r.Body, prefix[:]); err != nil {
					return
				}
				payload := make([]byte, binary.BigEndian.Uint32(prefix[1:]))
				if _, err := io.ReadFull(r.Body, payload); err != nil {
					return
				}
				_, _ = w.Write(grpcFrame([]byte("reply")))
				w.(http.Flusher).Flush()
			}
		}), &http2.Server{}))
		t.Cleanup(origin.Close)

		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		first := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		second := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		if err := lifecycle.Open(context.Background(), first); err != nil {
			t.Fatalf("opening the first project: %v", err)
		}
		started := make(chan uuid.UUID, 1)
		if err := proxy.WithOptions(
			marasi.WithStreamedPairStartedHandler(func(event domain.StreamedPairStarted) { started <- event.PairID }),
			marasi.WithBasePipeline(),
			marasi.WithDefaultModifierPipeline(),
		); err != nil {
			t.Fatalf("installing the pipeline: %v", err)
		}
		listener, err := proxy.GetListener("127.0.0.1", "0")
		if err != nil {
			t.Fatalf("creating the proxy listener: %v", err)
		}
		served := make(chan error, 1)
		go func() { served <- proxy.Serve(listener) }()
		t.Cleanup(func() {
			_ = proxy.CloseTransport()
			<-served
		})
		proxyAddr := listener.Addr().String()
		client := &http.Client{Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, _ string, _ *tls.Config) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
			},
		}}

		body, send := io.Pipe()
		t.Cleanup(func() { send.Close() })
		request, err := http.NewRequest(http.MethodPost, origin.URL+"/test.Echo/PingPong", body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/grpc")
		request.Header.Set("Te", "trailers")
		responses := make(chan *http.Response, 1)
		go func() {
			response, err := client.Do(request)
			if err != nil {
				t.Errorf("the call: %v", err)
				close(responses)
				return
			}
			responses <- response
		}()
		go send.Write(grpcFrame([]byte("ping")))
		var response *http.Response
		select {
		case response = <-responses:
		case <-time.After(5 * time.Second):
			t.Fatal("the call's response head did not arrive")
		}
		if response == nil {
			t.FailNow()
		}
		defer response.Body.Close()
		reply := make([]byte, len(grpcFrame([]byte("reply"))))
		if _, err := io.ReadFull(response.Body, reply); err != nil {
			t.Fatalf("reading the reply: %v", err)
		}
		var pairID uuid.UUID
		select {
		case pairID = <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("the streamed pair was not announced")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := lifecycle.Open(ctx, second); err != nil {
			t.Fatalf("opening the second project\nwanted:\nthe live streamed pair cancelled and the project opened\ngot:\n%v", err)
		}
		if _, err := io.ReadAll(response.Body); err == nil {
			t.Fatal("the call\nwanted:\ncancelled by the switch\ngot:\nended cleanly")
		}

		if messages, _, err := proxy.StreamRepo.ListStreamMessages(pairID, nil, 100); err != nil || len(messages) != 0 {
			t.Fatalf("the second project's stream messages for the pair\nwanted:\nnone\ngot:\n%d (%v)", len(messages), err)
		}
		connection, err := db.New(first, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("reopening the first project: %v", err)
		}
		repository := db.NewProxyRepo(connection)
		defer repository.Close()
		messages, _, err := repository.ListStreamMessages(pairID, nil, 100)
		if err != nil || len(messages) != 2 {
			t.Fatalf("the first project's stream messages for the pair\nwanted:\nping and reply\ngot:\n%d (%v)", len(messages), err)
		}
	})
}
