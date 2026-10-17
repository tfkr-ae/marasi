package service

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
)

// hungOriginHarness is a served proxy bound to a project lifecycle, in front of
// an origin that answers only when released.
type hungOriginHarness struct {
	server    *Server
	lifecycle *ProjectLifecycle
	dir       string
	current   string
	address   string
	origin    *httptest.Server
	entered   chan struct{}
	release   func()
	secure    bool
}

// newHungOriginHarness serves the proxy. A secure harness has its client
// CONNECT and speak TLS to the proxy, while the origin still speaks HTTP.
func newHungOriginHarness(t *testing.T, secure bool) *hungOriginHarness {
	t.Helper()
	server, lifecycle, dir, current := newProjectOpenServer(t)
	proxy := lifecycle.proxy
	if err := proxy.WithOptions(marasi.WithBasePipeline(), marasi.WithDefaultModifierPipeline(), marasi.WithRequestHandler(server.HandleRequest), marasi.WithResponseHandler(server.HandleResponse)); err != nil {
		t.Fatal(err)
	}
	if secure {
		if err := proxy.WithOptions(marasi.WithTLS()); err != nil {
			t.Fatal(err)
		}
		proxy.AddRequestModifier(func(_ *marasi.Proxy, req *http.Request) error { req.URL.Scheme = "http"; return nil })
	}
	harness := &hungOriginHarness{server: server, lifecycle: lifecycle, dir: dir, current: current, entered: make(chan struct{}, 8), secure: secure}
	released := make(chan struct{})
	var once sync.Once
	harness.release = func() { once.Do(func() { close(released) }) }
	harness.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hung":
			harness.entered <- struct{}{}
			<-released
		case "/hung-upgrade":
			harness.entered <- struct{}{}
			<-released
			conn, buffered, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
			buffered.Flush()
			io.Copy(io.Discard, buffered)
			return
		case "/hung-body":
			// The headers arrive and the body hangs.
			w.Header().Set("Content-Length", "11")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			harness.entered <- struct{}{}
			<-released
		}
		fmt.Fprint(w, "late answer")
	}))
	listener := NewListenerLifecycle(proxy, io.Discard)
	listener.BindProject(lifecycle)
	t.Cleanup(func() {
		harness.release()
		listener.Shutdown()
		harness.origin.Close()
	})
	if _, err := listener.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
		t.Fatal(err)
	}
	harness.address = net.JoinHostPort(proxy.Addr, proxy.Port)
	return harness
}

// sendHung writes a GET for path through the proxy and waits until the origin
// holds it.
func (harness *hungOriginHarness) sendHung(t *testing.T, path string) net.Conn {
	t.Helper()
	raw, err := net.Dial("tcp", harness.address)
	if err != nil {
		t.Fatalf("dialing proxy: %v", err)
	}
	t.Cleanup(func() { raw.Close() })
	host := strings.TrimPrefix(harness.origin.URL, "http://")
	client := raw
	target := harness.origin.URL + path
	if harness.secure {
		fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
		response, err := http.ReadResponse(bufio.NewReader(raw), nil)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("connect: %v %v", response, err)
		}
		secured := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, ServerName: "127.0.0.1"})
		if err := secured.Handshake(); err != nil {
			t.Fatalf("tls handshake: %v", err)
		}
		client = secured
		target = path
	}
	if _, err := fmt.Fprintf(client, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, host); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	select {
	case <-harness.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("origin did not receive the request")
	}
	return client
}

// pairs returns the open project's traffic with path.
func (harness *hungOriginHarness) pairs(t *testing.T, path string) []trafficSummary {
	t.Helper()
	response := requestControlAPI(harness.server, http.MethodGet, "/traffic", "")
	if response.Code != http.StatusOK {
		t.Fatalf("listing traffic: %d %s", response.Code, response.Body.String())
	}
	var list trafficList
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var matched []trafficSummary
	for _, item := range list.Items {
		if item.Path == path {
			matched = append(matched, item)
		}
	}
	return matched
}

func (harness *hungOriginHarness) admitted() int {
	gate := harness.lifecycle.gate
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.active
}

func TestProjectOpenHungOrigin(t *testing.T) {
	for _, test := range []struct {
		path   string
		secure bool
	}{{"/hung", false}, {"/hung", true}, {"/hung-body", false}, {"/hung-body", true}} {
		t.Run(fmt.Sprintf("should release admission once the client of a hung request disconnects %s secure=%v", test.path, test.secure), func(t *testing.T) {
			harness := newHungOriginHarness(t, test.secure)
			client := harness.sendHung(t, test.path)
			client.Close()
			time.Sleep(time.Second)
			if got := harness.admitted(); got != 0 {
				t.Fatalf("\nwanted:\nno admitted work a second after the client left\ngot:\n%d admitted", got)
			}
		})
	}

	for _, test := range []struct {
		path   string
		secure bool
	}{{"/hung", false}, {"/hung", true}, {"/hung-body", false}} {
		t.Run(fmt.Sprintf("should switch while a request waits on a hung origin and still answer its client %s secure=%v", test.path, test.secure), func(t *testing.T) {
			harness := newHungOriginHarness(t, test.secure)
			client := harness.sendHung(t, test.path)
			target := canonicalProjectPath(t, filepath.Join(harness.dir, "target.marasi"))
			prepared := awaitTargetPrepared(t, harness.lifecycle)
			opened := make(chan error, 1)
			go func() { opened <- harness.lifecycle.Open(context.Background(), target) }()
			prepared()
			select {
			case err := <-opened:
				if err != nil {
					t.Fatalf("opening target: %v", err)
				}
			case <-time.After(time.Second):
				harness.release()
				<-opened
				t.Fatal("the switch waited on the hung origin")
			}

			harness.release()
			client.SetReadDeadline(time.Now().Add(5 * time.Second))
			response, err := http.ReadResponse(bufio.NewReader(client), nil)
			if err != nil {
				t.Fatalf("reading the late response: %v", err)
			}
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusOK || string(body) != "late answer" {
				t.Fatalf("\nwanted:\n200 late answer\ngot:\n%d %q", response.StatusCode, body)
			}
			if err := harness.lifecycle.proxy.CloseWebSocketsAndFlush(); err != nil {
				t.Fatal(err)
			}
			if pairs := harness.pairs(t, test.path); len(pairs) != 0 {
				t.Fatalf("the late pair reached the target project: %+v", pairs)
			}
			if err := harness.lifecycle.Open(context.Background(), harness.current); err != nil {
				t.Fatalf("reopening the old project: %v", err)
			}
			pairs := harness.pairs(t, test.path)
			if len(pairs) != 1 || pairs[0].RespondedAt != nil {
				t.Fatalf("\nwanted:\none response-less pair in the old project\ngot:\n%+v", pairs)
			}
		})
	}

	t.Run("should refuse a WebSocket upgrade answered after the switch", func(t *testing.T) {
		harness := newHungOriginHarness(t, false)
		client, err := net.Dial("tcp", harness.address)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		host := strings.TrimPrefix(harness.origin.URL, "http://")
		fmt.Fprintf(client, "GET %s/hung-upgrade HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", harness.origin.URL, host)
		select {
		case <-harness.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("origin did not receive the handshake")
		}
		target := canonicalProjectPath(t, filepath.Join(harness.dir, "target.marasi"))
		if err := harness.lifecycle.Open(context.Background(), target); err != nil {
			t.Fatalf("opening target: %v", err)
		}
		harness.release()
		client.SetReadDeadline(time.Now().Add(5 * time.Second))
		response, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatalf("reading the handshake response: %v", err)
		}
		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502 for a handshake that crossed the switch\ngot:\n%d", response.StatusCode)
		}
	})
}
