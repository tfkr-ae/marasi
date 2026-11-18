package marasi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/internal/h1"
)

func TestH1Engine_AmbiguousFramingIsRefused(t *testing.T) {
	// Each request is followed by a second one that would be smuggled if
	// the proxy read the first one's length differently from the origin.
	const smuggled = "GET /smuggled HTTP/1.1\r\nHost: x\r\n\r\n"
	tests := []struct {
		name   string
		head   string
		body   string
		cause  h1.FramingCause
		stored string
	}{
		{
			name:  "both Content-Length and Transfer-Encoding",
			head:  "Content-Length: 4\r\nTransfer-Encoding: chunked\r\n",
			body:  "0\r\n\r\n",
			cause: h1.CauseLengthAndTransferEncoding,
		},
		{
			name:  "differing Content-Length lines",
			head:  "Content-Length: 3\r\nContent-Length: 5\r\n",
			body:  "hello",
			cause: h1.CauseDifferingLengths,
		},
		{
			name:  "differing Content-Length values in one list",
			head:  "Content-Length: 3, 5\r\n",
			body:  "hello",
			cause: h1.CauseDifferingLengths,
		},
		{
			name:  "a Content-Length that is not a length",
			head:  "Content-Length: +5\r\n",
			body:  "hello",
			cause: h1.CauseBadLength,
		},
		{
			name:  "a Transfer-Encoding that does not end in chunked",
			head:  "Transfer-Encoding: chunked, gzip\r\n",
			body:  "0\r\n\r\n",
			cause: h1.CauseChunkedNotFinal,
		},
		{
			name:  "chunked applied twice",
			head:  "Transfer-Encoding: chunked, chunked\r\n",
			body:  "0\r\n\r\n",
			cause: h1.CauseChunkedTwice,
		},
		{
			name:  "a chunk size that is not hexadecimal",
			head:  "Transfer-Encoding: chunked\r\n",
			body:  "5z\r\nhello\r\n0\r\n\r\n",
			cause: h1.CauseBadChunkSize,
		},
		{
			name:  "chunk data not followed by a line end",
			head:  "Transfer-Encoding: chunked\r\n",
			body:  "3\r\nhello\r\n0\r\n\r\n",
			cause: h1.CauseBadChunkEnd,
		},
	}
	for _, test := range tests {
		t.Run("should refuse "+test.name+" with a stored 400 and close the connection", func(t *testing.T) {
			origin := newRawOrigin(t, originResponse)
			addr, captured := serveH1EngineProxy(t)
			conn, br := dialProxy(t, addr)

			head := fmt.Sprintf("POST http://%s/upload HTTP/1.1\r\nHost: %s\r\n%s\r\n", origin.addr, origin.addr, test.head)
			io.WriteString(conn, head+test.body+smuggled)

			res, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("reading the response: %v", err)
			}
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), string(test.cause)) {
				t.Fatalf("wanted a 400 naming %q\ngot: %d %q", test.cause, res.StatusCode, body)
			}
			if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
				t.Fatalf("wanted the connection closed after the 400\ngot: %q, %v", rest, err)
			}
			if requests, connections := origin.received(); connections != 0 {
				t.Fatalf("wanted the origin to receive nothing\ngot: %d connections, %q", connections, requests)
			}

			select {
			case req := <-captured.request:
				if want := fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: %s\r\n%s\r\n", origin.addr, test.head); string(req.Raw) != want {
					t.Fatalf("wanted the stored request to be the head as read, in origin form:\n%q\ngot:\n%q", want, req.Raw)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the refused request was not captured")
			}
			select {
			case stored := <-captured.response:
				cause, _ := stored.Metadata[proxyErrorMetadataKey].(string)
				if stored.StatusCode != http.StatusBadRequest || !strings.Contains(cause, string(test.cause)) {
					t.Fatalf("wanted a stored 400 with %s naming %q\ngot: %d %v", proxyErrorMetadataKey, test.cause, stored.StatusCode, stored.Metadata)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the 400 was not captured")
			}
		})
	}

	t.Run("should refuse ambiguous framing inside a CONNECT tunnel", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		if res, err := http.ReadResponse(br, nil); err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("wanted the tunnel opened with a 200\ngot: %v, %v", res, err)
		}
		fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n0\r\n\r\n%s", origin.addr, smuggled)
		res, err := http.ReadResponse(br, nil)
		if err != nil || res.StatusCode != http.StatusBadRequest {
			t.Fatalf("wanted a 400\ngot: %v, %v", res, err)
		}
		io.ReadAll(res.Body)
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
			t.Fatalf("wanted the connection closed after the 400\ngot: %v", err)
		}
		if _, connections := origin.received(); connections != 0 {
			t.Fatalf("wanted the origin to receive nothing\ngot: %d connections", connections)
		}
	})

	t.Run("should refuse ambiguous framing with no base pipeline", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		database, err := db.New(":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("creating in-memory database: %v", err)
		}
		proxy, err := New(withH1Engine(), WithConfigDir(t.TempDir()), WithDefaultRepositories(db.NewProxyRepo(database)))
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		listener, err := proxy.GetListener("127.0.0.1", "0")
		if err != nil {
			t.Fatalf("creating proxy listener: %v", err)
		}
		go proxy.Serve(listener)
		t.Cleanup(func() { proxy.Close() })
		conn, br := dialProxy(t, listener.Addr().String())

		fmt.Fprintf(conn, "POST http://%s/ HTTP/1.1\r\nHost: %s\r\nContent-Length: 1\r\nContent-Length: 2\r\n\r\nab", origin.addr, origin.addr)
		res, err := http.ReadResponse(br, nil)
		if err != nil || res.StatusCode != http.StatusBadRequest {
			t.Fatalf("wanted a 400\ngot: %v, %v", res, err)
		}
		if _, connections := origin.received(); connections != 0 {
			t.Fatalf("wanted the origin to receive nothing\ngot: %d connections", connections)
		}
	})
}

func TestH1Engine_OddButFramableRequestsAreForwarded(t *testing.T) {
	tests := []struct {
		name    string
		request string
	}{
		{
			name:    "an unusual method",
			request: "PURGE /cache HTTP/1.1\r\nHost: {origin}\r\n\r\n",
		},
		{
			name:    "a method in odd case with a body",
			request: "Post /form HTTP/1.1\r\nHost: {origin}\r\nContent-Length: 5\r\n\r\nhello",
		},
		{
			name:    "odd whitespace around values",
			request: "GET / HTTP/1.1\r\nHost:{origin}\r\nX-Spaced: \t a  b \t\r\nContent-Length:   0  \r\n\r\n",
		},
		{
			name:    "odd header names",
			request: "GET / HTTP/1.1\r\nhOST: {origin}\r\nX_Under~score!: 1\r\n#hash|pipe: 2\r\n\r\n",
		},
		{
			name:    "repeated equal Content-Length values",
			request: "POST / HTTP/1.1\r\nHost: {origin}\r\nContent-Length: 5\r\ncontent-length: 5, 5\r\n\r\nhello",
		},
		{
			name:    "a Transfer-Encoding in odd case with chunk extensions",
			request: "POST / HTTP/1.1\r\nHost: {origin}\r\ntransfer-encoding: CHUNKED\r\n\r\n5;name=v\r\nhello\r\n0\r\n\r\n",
		},
	}
	for _, test := range tests {
		t.Run("should forward "+test.name+" as written", func(t *testing.T) {
			origin := newBodyOrigin(t, originResponse)
			addr, _ := serveH1EngineProxy(t)
			conn, br := dialProxy(t, addr)

			request := strings.ReplaceAll(test.request, "{origin}", origin.addr)
			// Through CONNECT, the request needs no rewrite to origin form.
			fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
			if res, err := http.ReadResponse(br, nil); err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("wanted the tunnel opened with a 200\ngot: %v, %v", res, err)
			}
			io.WriteString(conn, request)

			readExactly(t, br, originResponse)
			if got := origin.request(t); got != request {
				t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, got)
			}
		})
	}
}
