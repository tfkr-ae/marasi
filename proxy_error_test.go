package marasi

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// proxyErrorHarness is a real proxy over an in-memory project with the default
// extensions, and an origin that records every request it receives.
type proxyErrorHarness struct {
	proxy      *Proxy
	repository *db.Repository
	origin     *httptest.Server
	hits       atomic.Int32
	warnings   atomic.Int32 // requests that reached the origin with a Warning header
	responses  chan domain.ProxyResponse
}

// startProxyErrorHarness serves a proxy whose chains pipeline adds, after the
// base pipeline.
func startProxyErrorHarness(t *testing.T, pipeline func(*Proxy) error) *proxyErrorHarness {
	t.Helper()
	h := &proxyErrorHarness{responses: make(chan domain.ProxyResponse, 16)}
	h.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		if r.Header.Get("Warning") != "" {
			h.warnings.Add(1)
		}
		_, _ = io.WriteString(w, "origin")
	}))
	t.Cleanup(h.origin.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	h.repository = db.NewProxyRepo(database)
	defaults, err := h.repository.GetExtensions()
	if err != nil {
		h.repository.Close()
		t.Fatalf("getting default extensions: %v", err)
	}
	h.proxy, err = New(
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(h.repository),
		WithExtensions(defaults),
		WithRequestHandler(func(domain.ProxyRequest) error { return nil }),
		WithResponseHandler(func(res domain.ProxyResponse) error { h.responses <- res; return nil }),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithBasePipeline(),
		pipeline,
	)
	if err != nil {
		h.repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	listener, err := h.proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		h.proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- h.proxy.Serve(listener) }()
	t.Cleanup(func() {
		listener.Close()
		h.proxy.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})
	return h
}

// send writes raw to the proxy listener and reads the response.
func (h *proxyErrorHarness) send(t *testing.T, raw string) (*http.Response, string) {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort(h.proxy.Addr, h.proxy.Port))
	if err != nil {
		t.Fatalf("dialing proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return res, string(body)
}

// get sends a GET for path to address through the proxy.
func (h *proxyErrorHarness) get(t *testing.T, address, path string) (*http.Response, string) {
	t.Helper()
	return h.send(t, "GET http://"+address+path+" HTTP/1.1\r\nHost: "+address+"\r\n\r\n")
}

// originAddress is the host:port of the harness origin.
func (h *proxyErrorHarness) originAddress() string {
	return strings.TrimPrefix(h.origin.URL, "http://")
}

// stored waits for the captured response and returns it as stored.
func (h *proxyErrorHarness) stored(t *testing.T) *domain.ProxyResponse {
	t.Helper()
	var captured domain.ProxyResponse
	select {
	case captured = <-h.responses:
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
	}
	if err := h.proxy.flushDBWrites(); err != nil {
		t.Fatalf("flushing database writes: %v", err)
	}
	res, err := h.repository.GetResponse(captured.ID)
	if err != nil {
		t.Fatalf("getting stored response: %v", err)
	}
	return res
}

// errorLogs returns the ERROR proxy logs whose message contains text.
func (h *proxyErrorHarness) errorLogs(t *testing.T, text string) []*domain.Log {
	t.Helper()
	if err := h.proxy.flushDBWrites(); err != nil {
		t.Fatalf("flushing database writes: %v", err)
	}
	logs, err := h.repository.GetLogs()
	if err != nil {
		t.Fatalf("getting logs: %v", err)
	}
	var matched []*domain.Log
	for _, entry := range logs {
		if entry.Level == "ERROR" && strings.Contains(entry.Message, text) {
			matched = append(matched, entry)
		}
	}
	return matched
}

// assertProxyError checks that res is a proxy error response: a 502 with no
// Warning header, whose plain-text body contains cause, after which the
// connection closes.
func assertProxyError(t *testing.T, res *http.Response, body, cause string) {
	t.Helper()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("wanted: 502\ngot: %d %q", res.StatusCode, body)
	}
	if warning := res.Header.Get("Warning"); warning != "" {
		t.Fatalf("wanted no Warning header\ngot: %q", warning)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("wanted a plain-text body\ngot Content-Type: %q", got)
	}
	if !strings.Contains(body, cause) {
		t.Fatalf("wanted the body to name the cause %q\ngot: %q", cause, body)
	}
	if !res.Close {
		t.Fatal("wanted the proxy to close the connection after a proxy error response")
	}
}

// assertStoredProxyError checks that the stored response is the 502 and that
// metadata.proxy_error holds cause.
func assertStoredProxyError(t *testing.T, stored *domain.ProxyResponse, cause string) {
	t.Helper()
	if stored.StatusCode != http.StatusBadGateway {
		t.Fatalf("wanted the stored response to be the 502\ngot: %d", stored.StatusCode)
	}
	got, _ := stored.Metadata["proxy_error"].(string)
	if !strings.Contains(got, cause) {
		t.Fatalf("wanted metadata.proxy_error to hold %q\ngot: %v", cause, stored.Metadata)
	}
	// The stored length is the body's length, as for an origin response.
	_, body, _ := bytes.Cut(stored.Raw, []byte("\r\n\r\n"))
	if stored.Length != strconv.Itoa(len(body)) {
		t.Fatalf("wanted the stored length %d\ngot: %q", len(body), stored.Length)
	}
}

// closedAddress returns a loopback address nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	return strings.TrimPrefix(closed.URL, "http://")
}

func TestProxyErrorResponse_NoResponseFromOrigin(t *testing.T) {
	t.Run("should answer an unreachable origin with a 502 naming the cause, store it and log it", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())
		address := closedAddress(t)

		res, body := h.get(t, address, "/gone")

		assertProxyError(t, res, body, "connection refused")
		assertStoredProxyError(t, h.stored(t), "connection refused")
		if logs := h.errorLogs(t, "connection refused"); len(logs) != 1 {
			t.Fatalf("wanted one ERROR proxy log naming the cause\ngot: %d", len(logs))
		}
	})

	t.Run("should answer a route that fails closed with a 502 naming the cause, and keep its one proxy log", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())
		h.proxy.SelectUpstreamProxy("missing")

		res, body := h.get(t, h.originAddress(), "/routed")

		assertProxyError(t, res, body, "missing")
		assertStoredProxyError(t, h.stored(t), "missing")
		if h.hits.Load() != 0 {
			t.Fatalf("wanted nothing to reach the origin\ngot: %d requests", h.hits.Load())
		}
		if logs := h.errorLogs(t, "missing"); len(logs) != 1 {
			t.Fatalf("wanted one ERROR proxy log naming the upstream proxy\ngot: %d", len(logs))
		}
	})

	t.Run("should keep the 502 from extensions and Checkpoint", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())
		updateExtension(t, h.proxy, "workshop", `function processResponse(response) response:headers():set("X-Workshop", "saw") end`)
		updateExtension(t, h.proxy, "checkpoint", `function interceptResponse(response) return true end`)
		address := closedAddress(t)

		res, body := h.get(t, address, "/gone")

		assertProxyError(t, res, body, "connection refused")
		if got := res.Header.Get("X-Workshop"); got != "" {
			t.Fatalf("wanted no extension to see the proxy error response\ngot X-Workshop: %q", got)
		}
		if items := h.proxy.CheckpointItems(); len(items) != 0 {
			t.Fatalf("wanted Checkpoint to hold nothing\ngot: %d items", len(items))
		}
		assertStoredProxyError(t, h.stored(t), "connection refused")
	})
}

// failingModifier is a Go modifier that breaks.
func failingModifier(*Proxy, *Pair) error {
	return errors.New("the modifier broke")
}

func TestProxyErrorResponse_GoModifierError(t *testing.T) {
	t.Run("should stop the request and answer a 502 naming the cause when a Go request modifier fails", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())
		h.proxy.AddRequestPairModifier(failingModifier)

		res, body := h.get(t, h.originAddress(), "/stopped")

		assertProxyError(t, res, body, "the modifier broke")
		if h.hits.Load() != 0 {
			t.Fatalf("wanted nothing to reach the origin\ngot: %d requests", h.hits.Load())
		}
		assertStoredProxyError(t, h.stored(t), "the modifier broke")
		if logs := h.errorLogs(t, "the modifier broke"); len(logs) != 1 {
			t.Fatalf("wanted one ERROR proxy log naming the cause\ngot: %d", len(logs))
		}
	})

	t.Run("should store the pair when the failing modifier runs before the request is captured", func(t *testing.T) {
		h := startProxyErrorHarness(t, func(proxy *Proxy) error {
			proxy.AddRequestPairModifier(SetupRequestModifier)
			proxy.AddRequestPairModifier(failingModifier)
			proxy.AddRequestPairModifier(WriteRequestModifier)
			proxy.AddResponsePairModifier(ResponseFilterModifier)
			proxy.AddResponsePairModifier(WriteResponseModifier)
			return nil
		})

		res, body := h.get(t, h.originAddress(), "/stopped")

		assertProxyError(t, res, body, "the modifier broke")
		if h.hits.Load() != 0 {
			t.Fatalf("wanted nothing to reach the origin\ngot: %d requests", h.hits.Load())
		}
		stored := h.stored(t)
		assertStoredProxyError(t, stored, "the modifier broke")
		request, err := h.repository.GetRequestResponseRow(stored.ID)
		if err != nil || request == nil {
			t.Fatalf("wanted the request stored with the response\ngot: %v, %v", request, err)
		}
	})

	t.Run("should answer a 502 when a Go modifier fails before the pair is set up", func(t *testing.T) {
		h := startProxyErrorHarness(t, func(proxy *Proxy) error {
			proxy.AddRequestPairModifier(failingModifier)
			return WithDefaultModifierPipeline()(proxy)
		})

		res, body := h.get(t, h.originAddress(), "/stopped")

		assertProxyError(t, res, body, "the modifier broke")
		if h.hits.Load() != 0 {
			t.Fatalf("wanted nothing to reach the origin\ngot: %d requests", h.hits.Load())
		}
	})

	t.Run("should never send a Warning header or error text to the origin", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())
		var failed atomic.Bool
		h.proxy.AddRequestPairModifier(func(*Proxy, *Pair) error {
			if failed.CompareAndSwap(false, true) {
				return errors.New("the modifier broke")
			}
			return nil
		})

		h.get(t, h.originAddress(), "/first")
		res, body := h.get(t, h.originAddress(), "/second")

		if res.StatusCode != http.StatusOK || body != "origin" {
			t.Fatalf("wanted the second request to reach the origin\ngot: %d %q", res.StatusCode, body)
		}
		if h.hits.Load() != 1 || h.warnings.Load() != 0 {
			t.Fatalf("wanted one request at the origin, with no Warning\ngot: %d requests, %d with Warning", h.hits.Load(), h.warnings.Load())
		}
	})

	t.Run("should let an ordinary Lua error fail open", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())
		updateExtension(t, h.proxy, "workshop", `function processRequest(request) error("the script broke") end`)

		res, body := h.get(t, h.originAddress(), "/lua")

		if res.StatusCode != http.StatusOK || body != "origin" {
			t.Fatalf("wanted the request to reach the origin\ngot: %d %q", res.StatusCode, body)
		}
		if h.hits.Load() != 1 {
			t.Fatalf("wanted one request at the origin\ngot: %d", h.hits.Load())
		}
		if stored := h.stored(t); stored.Metadata["proxy_error"] != nil {
			t.Fatalf("wanted no proxy_error on an origin response\ngot: %v", stored.Metadata)
		}
	})
}

func TestProxyErrorResponse_Loop(t *testing.T) {
	t.Run("should answer a request to the proxy's own listener with a proxy error response, store it and log it", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())

		res, body := h.get(t, net.JoinHostPort(h.proxy.Addr, h.proxy.Port), "/loop")

		assertProxyError(t, res, body, "loops back to the proxy listener")
		assertStoredProxyError(t, h.stored(t), "loops back to the proxy listener")
		if logs := h.errorLogs(t, "loops back to the proxy listener"); len(logs) != 1 {
			t.Fatalf("\nwanted:\n1 ERROR proxy log naming the cause\ngot:\n%d", len(logs))
		}
	})
}

// halfEditingModifier is a Go response modifier that edits the response and
// then breaks.
func halfEditingModifier(_ *Proxy, pair *Pair) error {
	pair.Response.Header.Set("X-Half", "edited")
	return errors.New("the response modifier broke")
}

func TestProxyErrorResponse_GoResponseModifierError(t *testing.T) {
	t.Run("should answer a 502 naming the cause, store it and log it when a Go response modifier fails", func(t *testing.T) {
		h := startProxyErrorHarness(t, func(proxy *Proxy) error {
			proxy.AddRequestPairModifier(SetupRequestModifier)
			proxy.AddRequestPairModifier(WriteRequestModifier)
			proxy.AddResponsePairModifier(ResponseFilterModifier)
			proxy.AddResponsePairModifier(halfEditingModifier)
			proxy.AddResponsePairModifier(WriteResponseModifier)
			return nil
		})

		res, body := h.get(t, h.originAddress(), "/half")

		assertProxyError(t, res, body, "the response modifier broke")
		if got := res.Header.Get("X-Half"); got != "" {
			t.Fatalf("\nwanted:\nno half-edited header\ngot:\nX-Half: %q", got)
		}
		assertStoredProxyError(t, h.stored(t), "the response modifier broke")
		if logs := h.errorLogs(t, "the response modifier broke"); len(logs) != 1 {
			t.Fatalf("\nwanted:\n1 ERROR proxy log naming the cause\ngot:\n%d", len(logs))
		}
	})

	t.Run("should answer a 502 when a Go response modifier fails after the default chain", func(t *testing.T) {
		h := startProxyErrorHarness(t, WithDefaultModifierPipeline())
		h.proxy.AddResponsePairModifier(halfEditingModifier)

		res, body := h.get(t, h.originAddress(), "/late")

		assertProxyError(t, res, body, "the response modifier broke")
		if logs := h.errorLogs(t, "the response modifier broke"); len(logs) != 1 {
			t.Fatalf("\nwanted:\n1 ERROR proxy log naming the cause\ngot:\n%d", len(logs))
		}
	})
}

func TestProxyErrorResponse_ClientLegFailure(t *testing.T) {
	t.Run("should answer a 502 naming the cause and log it when the response cannot be written for the client", func(t *testing.T) {
		h := startProxyErrorHarness(t, func(proxy *Proxy) error {
			proxy.AddRequestPairModifier(SetupRequestModifier)
			proxy.AddRequestPairModifier(WriteRequestModifier)
			proxy.AddResponsePairModifier(ResponseFilterModifier)
			// A modifier that leaves a body nobody can read, and
			// reports nothing.
			proxy.AddResponsePairModifier(func(_ *Proxy, pair *Pair) error {
				pair.Response.Body = io.NopCloser(failingReader{errors.New("the body went away")})
				return nil
			})
			return nil
		})

		res, body := h.get(t, h.originAddress(), "/unwritable")

		assertProxyError(t, res, body, "the body went away")
		if logs := h.errorLogs(t, "the body went away"); len(logs) != 1 {
			t.Fatalf("\nwanted:\n1 ERROR proxy log naming the cause\ngot:\n%d", len(logs))
		}
	})
}
