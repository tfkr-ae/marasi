package marasi

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
)

func TestStoredResponseHead(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "should preserve an HTTP/1.1 head with its original transfer and content codings",
			raw:  "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n",
			want: "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n",
		},
		{
			name: "should preserve an HTTP/2 head unchanged",
			raw:  "HTTP/2 200\r\ncontent-type: text/event-stream\r\ncontent-encoding: deflate\r\n\r\n",
			want: "HTTP/2 200\r\ncontent-type: text/event-stream\r\ncontent-encoding: deflate\r\n\r\n",
		},
		{
			name: "should strip any bytes after the blank line",
			raw:  "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\nextra",
			want: "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n",
		},
		{
			name: "should append the blank line when missing",
			raw:  "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n",
			want: "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(storedResponseHead([]byte(c.raw))); got != c.want {
				t.Fatalf("storedResponseHead()\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}

func readJoinedSSE(t *testing.T, proxy *Proxy, id uuid.UUID) string {
	t.Helper()
	joined, err := proxy.OpenJoinedSSEResponse(id)
	if err != nil {
		t.Fatalf("OpenJoinedSSEResponse(%s) returned unexpected error: %v", id, err)
	}
	var out bytes.Buffer
	n, err := joined.WriteTo(&out)
	if err != nil {
		t.Fatalf("WriteTo() returned unexpected error: %v", err)
	}
	if n != int64(out.Len()) {
		t.Fatalf("WriteTo() byte count\nwanted:\n%d\ngot:\n%d", out.Len(), n)
	}
	return out.String()
}

// deflateStream is a sender's zlib compressor flushed after each event.
type deflateStream struct {
	buf bytes.Buffer
	w   *zlib.Writer
}

func newDeflateStream() *deflateStream {
	s := &deflateStream{}
	s.w = zlib.NewWriter(&s.buf)
	return s
}

func (s *deflateStream) message(text string) string {
	s.w.Write([]byte(text))
	s.w.Flush()
	out := s.buf.String()
	s.buf.Reset()
	return out
}

func (s *deflateStream) end() string {
	s.w.Close()
	out := s.buf.String()
	s.buf.Reset()
	return out
}

func TestProxy_JoinedSSEResponse(t *testing.T) {
	t.Run("should join an HTTP/1.1 SSE response preserving head and event delimiters, rejecting while live", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		const event1 = "id: 1\ndata: lf\n\n"
		const event2 = "event: ping\r\ndata: crlf\r\n\r\n"
		const event3 = "data: cr\r\r"
		io.WriteString(upstream, head+chunk(event1))
		readWithin(t, client, br, "the client", head+chunk(event1))

		req := <-captured.request
		if _, err := proxy.OpenJoinedSSEResponse(req.ID); !errors.Is(err, ErrStreamedResponseOpen) {
			t.Fatalf("OpenJoinedSSEResponse() while live\nwanted:\nErrStreamedResponseOpen\ngot:\n%v", err)
		}

		io.WriteString(upstream, chunk(event2)+chunk(event3)+"0\r\n\r\n")
		readWithin(t, client, br, "the client", chunk(event2)+chunk(event3)+"0\r\n\r\n")
		<-captured.response

		want := head + event1 + event2 + event3
		if got := readJoinedSSE(t, proxy, req.ID); got != want {
			t.Fatalf("joined HTTP/1.1 SSE response\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should join an HTTP/2 SSE response immediately when OnResponse fires", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		joinedOnEvent := make(chan string, 1)
		errOnEvent := make(chan error, 1)
		var proxy *Proxy
		addr, captured, ca := serveH1EngineMITMProxy(t, true, func(p *Proxy) {
			proxy = p
		})
		origOnResponse := proxy.OnResponse
		proxy.OnResponse = func(res domain.ProxyResponse) error {
			joined, err := proxy.OpenJoinedSSEResponse(res.ID)
			if err != nil {
				errOnEvent <- err
			} else {
				var buf bytes.Buffer
				_, err = joined.WriteTo(&buf)
				if err != nil {
					errOnEvent <- err
				} else {
					joinedOnEvent <- buf.String()
				}
			}
			if origOnResponse != nil {
				return origOnResponse(res)
			}
			return nil
		}

		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
		id := upstream.nthStream(t, 0)
		upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream", "cache-control", "no-cache")
		upstream.data(id, false, "id: 1\ndata: first\n\n")
		awaitData(t, client, "the client", 1, "id: 1\ndata: first\n\n")

		req := <-captured.request
		if _, err := proxy.OpenJoinedSSEResponse(req.ID); !errors.Is(err, ErrStreamedResponseOpen) {
			t.Fatalf("OpenJoinedSSEResponse() while live\nwanted:\nErrStreamedResponseOpen\ngot:\n%v", err)
		}

		upstream.data(id, true, "id: 2\ndata: final\n\n")
		client.stream(t, 1)
		<-captured.response

		want := "HTTP/2 200\r\ncontent-type: text/event-stream\r\ncache-control: no-cache\r\n\r\nid: 1\ndata: first\n\nid: 2\ndata: final\n\n"
		select {
		case err := <-errOnEvent:
			t.Fatalf("OpenJoinedSSEResponse() inside OnResponse returned error: %v", err)
		case got := <-joinedOnEvent:
			if got != want {
				t.Fatalf("joined HTTP/2 SSE response inside OnResponse\nwanted:\n%q\ngot:\n%q", want, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("OnResponse did not run")
		}
	})

	t.Run("should join gzip and deflate SSE responses with decoded payloads and original stored head", func(t *testing.T) {
		t.Run("should decode HTTP/1.1 gzip SSE", func(t *testing.T) {
			origin := newH1ScriptedOrigin(t)
			proxy, addr, captured := serveFramingProxy(t, nil)
			client, br := dialProxy(t, addr)
			fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

			upstream := origin.accept(t)
			gz := newGzipStream()
			const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n"
			io.WriteString(upstream, head+chunk(gz.message("data: one\n\n"))+chunk(gz.message("data: two\r\n\r\n"))+chunk(gz.end())+"0\r\n\r\n")
			readWithin(t, client, br, "the client", head)
			readToLastChunk(t, client, br, nil)

			req, _ := streamedResponse(t, captured)
			want := head + "data: one\n\ndata: two\r\n\r\n"
			if got := readJoinedSSE(t, proxy, req.ID); got != want {
				t.Fatalf("joined gzip SSE\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})

		t.Run("should decode HTTP/2 deflate SSE", func(t *testing.T) {
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true)
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
			id := upstream.nthStream(t, 0)
			df := newDeflateStream()
			upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream", "content-encoding", "deflate")
			upstream.data(id, false, df.message("data: alpha\n\n"))
			upstream.data(id, true, df.message("data: beta\n\n")+df.end())
			client.stream(t, 1)

			req, _ := streamedResponse(t, captured)
			want := "HTTP/2 200\r\ncontent-type: text/event-stream\r\ncontent-encoding: deflate\r\n\r\ndata: alpha\n\ndata: beta\n\n"
			if got := readJoinedSSE(t, proxyOf(t, addr), req.ID); got != want {
				t.Fatalf("joined deflate SSE\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})
	})

	t.Run("should include forwarded edits and injections and exclude dropped, cut-short, oversized, and undecodable events", func(t *testing.T) {
		t.Run("should include edited and injected events and exclude dropped and cut-short events", func(t *testing.T) {
			origin := newH1ScriptedOrigin(t)
			proxy, addr, captured := serveFramingProxy(t, withStreamExtension(t, "sse-hook", `
				function processStreamMessage(message)
					if message:payload() == "data: edit-me\n\n" then
						message:set_payload("data: edited\n\n")
					elseif message:payload() == "data: drop-me\n\n" then
						message:drop()
					end
				end
			`))
			client, br := dialProxy(t, addr)
			fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

			upstream := origin.accept(t)
			const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
			io.WriteString(upstream, head+chunk("data: edit-me\n\n"))
			readWithin(t, client, br, "the client", head+chunk("data: edited\n\n"))

			req := <-captured.request
			injectOnceCarried(t, proxy, req.ID, domain.StreamDirectionResponse, "data: injected\n\n", StreamInjectOptions{})
			readWithin(t, client, br, "the client", chunk("data: injected\n\n"))

			io.WriteString(upstream, chunk("data: drop-me\n\n")+chunk("data: kept\n\n")+chunk("data: incomplete"))
			readWithin(t, client, br, "the client", chunk("data: kept\n\n"))
			upstream.Close()
			readAllUntilClose(t, client, br, chunk("data: incomplete"))
			<-captured.response

			want := head + "data: edited\n\ndata: injected\n\ndata: kept\n\n"
			if got := readJoinedSSE(t, proxy, req.ID); got != want {
				t.Fatalf("joined SSE with edits, injection, drop, and truncation\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})

		t.Run("should retain complete forwarded events when cancelled", func(t *testing.T) {
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true)
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
			id := upstream.nthStream(t, 0)
			upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream")
			upstream.data(id, false, "data: before-cancel\n\n")
			awaitData(t, client, "the client", 1, "data: before-cancel\n\n")
			client.write(func(f *http2.Framer) error { return f.WriteRSTStream(1, http2.ErrCodeCancel) })

			req, _ := streamedResponse(t, captured)
			want := "HTTP/2 200\r\ncontent-type: text/event-stream\r\n\r\ndata: before-cancel\n\n"
			if got := readJoinedSSE(t, proxyOf(t, addr), req.ID); got != want {
				t.Fatalf("joined cancelled SSE\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})

		t.Run("should exclude oversized events and keep earlier complete events", func(t *testing.T) {
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true, func(p *Proxy) { p.Config.StreamMaxMessageSize = 24 })
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
			id := upstream.nthStream(t, 0)
			upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream")
			upstream.data(id, false, "data: ok\n\ndata: "+strings.Repeat("x", 64)+"\n\n")
			client.stream(t, 1)

			req, _ := streamedResponse(t, captured)
			want := "HTTP/2 200\r\ncontent-type: text/event-stream\r\n\r\ndata: ok\n\n"
			if got := readJoinedSSE(t, proxyOf(t, addr), req.ID); got != want {
				t.Fatalf("joined SSE with oversized event\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})

		t.Run("should exclude undecodable gzip chunks and keep earlier complete events", func(t *testing.T) {
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true)
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
			id := upstream.nthStream(t, 0)
			upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream", "content-encoding", "gzip")
			upstream.data(id, false, newGzipStream().message("data: ok\n\n")+"\xff\xff corrupt")
			client.stream(t, 1)

			req, _ := streamedResponse(t, captured)
			want := "HTTP/2 200\r\ncontent-type: text/event-stream\r\ncontent-encoding: gzip\r\n\r\ndata: ok\n\n"
			if got := readJoinedSSE(t, proxyOf(t, addr), req.ID); got != want {
				t.Fatalf("joined SSE with corrupt gzip\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})
	})

	t.Run("should return only the head for an ended SSE response with zero events", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+"0\r\n\r\n")
		readWithin(t, client, br, "the client", head+"0\r\n\r\n")

		req, _ := streamedResponse(t, captured)
		if got := readJoinedSSE(t, proxy, req.ID); got != head {
			t.Fatalf("joined empty SSE response\nwanted:\n%q\ngot:\n%q", head, got)
		}
	})

	t.Run("should page incrementally without buffering all pages before writing", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		pairID := uuid.Must(uuid.NewV7())
		const head = "HTTP/2 200\r\ncontent-type: text/event-stream\r\n\r\n"
		if err := h.repository.InsertRequest(&domain.ProxyRequest{
			ID: pairID, Scheme: "https", Method: "GET", Host: "example.com", Path: "/events",
			Raw: []byte("GET /events HTTP/2\r\n:authority: example.com\r\n\r\n"), RequestedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.repository.RecordStreamedPair(&domain.StreamedPairRecord{
			PairID: pairID,
			State: domain.StreamedState{
				Response: &domain.StreamedDirectionState{Framing: "sse", Ending: "complete"},
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.repository.InsertResponse(&domain.ProxyResponse{
			ID: pairID, Status: "200 OK", StatusCode: 200, ContentType: "text/event-stream",
			Raw: []byte(head), RespondedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}

		var want bytes.Buffer
		want.WriteString(head)
		for i := range 6 {
			payload := fmt.Sprintf("id: %d\ndata: event-%d\n\n", i, i)
			want.WriteString(payload)
			if err := h.repository.InsertStreamMessage(&domain.StreamMessage{
				ID: uuid.Must(uuid.NewV7()), PairID: pairID, Direction: domain.StreamDirectionResponse,
				Framing: "sse", Payload: []byte(payload), CreatedAt: time.Now(), Metadata: map[string]any{},
			}); err != nil {
				t.Fatal(err)
			}
		}

		var out bytes.Buffer
		var bytesWrittenWhenPageFetched []int
		spy := &pagingStreamRepo{
			StreamRepository: h.repository,
			pageSize:         2,
			onList: func() {
				bytesWrittenWhenPageFetched = append(bytesWrittenWhenPageFetched, out.Len())
			},
		}
		h.proxy.StreamRepo = spy

		joined, err := h.proxy.OpenJoinedSSEResponse(pairID)
		if err != nil {
			t.Fatalf("OpenJoinedSSEResponse() returned unexpected error: %v", err)
		}
		if _, err := joined.WriteToContext(context.Background(), &out); err != nil {
			t.Fatalf("WriteToContext() returned unexpected error: %v", err)
		}
		if out.String() != want.String() {
			t.Fatalf("paged output\nwanted:\n%q\ngot:\n%q", want.String(), out.String())
		}
		if len(bytesWrittenWhenPageFetched) < 3 {
			t.Fatalf("page fetches\nwanted:\nat least 3 pages\ngot:\n%d (%v)", len(bytesWrittenWhenPageFetched), bytesWrittenWhenPageFetched)
		}
		if bytesWrittenWhenPageFetched[1] <= len(head) || bytesWrittenWhenPageFetched[2] <= bytesWrittenWhenPageFetched[1] {
			t.Fatalf("bytes written before each page fetch\nwanted:\nincremental writes before later pages are fetched\ngot:\n%v", bytesWrittenWhenPageFetched)
		}
	})

	t.Run("should stop writing when the context is cancelled", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		pairID := uuid.Must(uuid.NewV7())
		if err := h.repository.InsertRequest(&domain.ProxyRequest{
			ID: pairID, Scheme: "https", Method: "GET", Host: "example.com", Path: "/events",
			Raw: []byte("GET /events HTTP/2\r\n:authority: example.com\r\n\r\n"), RequestedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.repository.RecordStreamedPair(&domain.StreamedPairRecord{
			PairID: pairID,
			State: domain.StreamedState{
				Response: &domain.StreamedDirectionState{Framing: "sse", Ending: "complete"},
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.repository.InsertResponse(&domain.ProxyResponse{
			ID: pairID, Status: "200 OK", StatusCode: 200, ContentType: "text/event-stream",
			Raw: []byte("HTTP/2 200\r\ncontent-type: text/event-stream\r\n\r\n"), RespondedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		joined, err := h.proxy.OpenJoinedSSEResponse(pairID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var out bytes.Buffer
		if _, err := joined.WriteToContext(ctx, &out); !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteToContext() with cancelled context\nwanted:\ncontext.Canceled\ngot:\n%v", err)
		}
	})
}

// pagingStreamRepo caps each ListStreamMessages page to pageSize and notifies
// onList before querying the underlying repository.
type pagingStreamRepo struct {
	domain.StreamRepository
	pageSize int
	onList   func()
}

func (r *pagingStreamRepo) ListStreamMessages(pairID uuid.UUID, cursor *uuid.UUID, _ int) ([]*domain.StreamMessage, *uuid.UUID, error) {
	if r.onList != nil {
		r.onList()
	}
	return r.StreamRepository.ListStreamMessages(pairID, cursor, r.pageSize)
}
