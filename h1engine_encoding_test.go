package marasi

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
)

// serveEncodingProxy serves a proxy with the default pipeline and the seeded
// extensions, and reports each stored response.
func serveEncodingProxy(t *testing.T) (*Proxy, string, chan domain.ProxyResponse) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(database)
	seeded, err := repository.GetExtensions()
	if err != nil {
		repository.Close()
		t.Fatalf("getting the seeded extensions: %v", err)
	}
	stored := make(chan domain.ProxyResponse, 16)
	options := []func(*Proxy) error{
		WithLogger(logger),
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithExtensions(seeded),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithRequestHandler(func(domain.ProxyRequest) error { return nil }),
		WithResponseHandler(func(res domain.ProxyResponse) error { stored <- res; return nil }),
		WithBasePipeline(),
		WithDefaultModifierPipeline(),
	}
	proxy, err := New(options...)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- proxy.Serve(listener) }()
	t.Cleanup(func() {
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})
	return proxy, net.JoinHostPort(proxy.Addr, proxy.Port), stored
}

func gzipped(t *testing.T, text string) string {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	io.WriteString(w, text)
	if err := w.Close(); err != nil {
		t.Fatalf("gzipping: %v", err)
	}
	return b.String()
}

func brotlied(t *testing.T, text string) string {
	t.Helper()
	var b bytes.Buffer
	w := brotli.NewWriter(&b)
	io.WriteString(w, text)
	if err := w.Close(); err != nil {
		t.Fatalf("compressing with brotli: %v", err)
	}
	return b.String()
}

// storedResponse waits for the stored response.
func storedResponse(t *testing.T, stored chan domain.ProxyResponse) domain.ProxyResponse {
	t.Helper()
	select {
	case res := <-stored:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not stored")
		return domain.ProxyResponse{}
	}
}

// roundTripThrough sends one GET for origin through the proxy and reads
// exactly len(want) bytes back.
func roundTripThrough(t *testing.T, addr string, origin *rawOrigin, want string) {
	t.Helper()
	conn, br := dialProxy(t, addr)
	fmt.Fprintf(conn, "GET http://%s/coded HTTP/1.1\r\nHost: %s\r\nAccept-Encoding: gzip, br\r\n\r\n", origin.addr, origin.addr)
	readExactly(t, br, want)
}

func wantDecodingMetadata(t *testing.T, res domain.ProxyResponse, coding string, wireLength int) {
	t.Helper()
	if got := res.Metadata["decoded_content_encoding"]; got != coding {
		t.Errorf("wanted metadata.decoded_content_encoding %q\ngot: %v", coding, got)
	}
	if got := fmt.Sprint(res.Metadata["wire_content_length"]); got != fmt.Sprint(wireLength) {
		t.Errorf("wanted metadata.wire_content_length %d\ngot: %v", wireLength, got)
	}
}

func TestH1Engine_CompressedResponses(t *testing.T) {
	const text = "hello, world"

	t.Run("should send a gzip response with Content-Length to the client as the origin sent it and store it decoded", func(t *testing.T) {
		coded := gzipped(t, text)
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\ncontent-encoding: gzip\r\nContent-Length: %d\r\nX-Last: 1\r\n\r\n%s", len(coded), coded)
		origin := newRawOrigin(t, response)
		_, addr, stored := serveEncodingProxy(t)

		roundTripThrough(t, addr, origin, response)

		res := storedResponse(t, stored)
		want := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 12\r\nX-Last: 1\r\n\r\n" + text
		if string(res.Raw) != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, res.Raw)
		}
		wantDecodingMetadata(t, res, "gzip", len(coded))
	})

	t.Run("should send a chunked br response to the client as the origin sent it and store it decoded", func(t *testing.T) {
		coded := brotlied(t, text)
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Encoding: br\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(coded), coded)
		origin := newRawOrigin(t, response)
		_, addr, stored := serveEncodingProxy(t)

		roundTripThrough(t, addr, origin, response)

		res := storedResponse(t, stored)
		want := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n" + text
		if string(res.Raw) != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, res.Raw)
		}
		// A chunked response has no Content-Length; the Length column is the
		// stored payload's length, as for any other response.
		if res.Length != fmt.Sprint(len(text)) {
			t.Fatalf("wanted the stored length %d\ngot: %q", len(text), res.Length)
		}
		wantDecodingMetadata(t, res, "br", len(coded))
	})

	t.Run("should send an extension's edit of the decoded body decoded, without Content-Encoding", func(t *testing.T) {
		coded := gzipped(t, text)
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n%s", len(coded), coded)
		origin := newRawOrigin(t, response)
		proxy, addr, stored := serveEncodingProxy(t)
		updateExtension(t, proxy, "workshop", `
			function processResponse(response)
				response:set_body(response:body() .. "!")
			end
		`)

		want := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 13\r\n\r\n" + text + "!"
		roundTripThrough(t, addr, origin, want)

		res := storedResponse(t, stored)
		if string(res.Raw) != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, res.Raw)
		}
		wantDecodingMetadata(t, res, "gzip", len(coded))
	})

	t.Run("should send each coding Marasi decodes to the client as the origin sent it and store it decoded", func(t *testing.T) {
		cases := []struct {
			name    string
			lines   []string
			applied []string
			coding  string
		}{
			{name: "zstd", lines: []string{"zstd"}, applied: []string{"zstd"}, coding: "zstd"},
			{name: "deflate", lines: []string{"deflate"}, applied: []string{"deflate"}, coding: "deflate"},
			{name: "x-gzip", lines: []string{"x-gzip"}, applied: []string{"gzip"}, coding: "x-gzip"},
			{name: "GZIP", lines: []string{"GZIP"}, applied: []string{"gzip"}, coding: "gzip"},
			{name: "Br", lines: []string{"Br"}, applied: []string{"br"}, coding: "br"},
			{name: "stacked on one line", lines: []string{"gzip, br"}, applied: []string{"gzip", "br"}, coding: "gzip, br"},
			{name: "stacked on two lines", lines: []string{"gzip", "zstd"}, applied: []string{"gzip", "zstd"}, coding: "gzip, zstd"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				coded := codedWith(t, tc.applied, text)
				var head strings.Builder
				for _, line := range tc.lines {
					fmt.Fprintf(&head, "Content-Encoding: %s\r\n", line)
				}
				response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n%sContent-Length: %d\r\n\r\n%s", head.String(), len(coded), coded)
				origin := newRawOrigin(t, response)
				_, addr, stored := serveEncodingProxy(t)

				roundTripThrough(t, addr, origin, response)

				res := storedResponse(t, stored)
				want := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 12\r\n\r\n" + text
				if string(res.Raw) != want {
					t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, res.Raw)
				}
				wantDecodingMetadata(t, res, tc.coding, len(coded))
			})
		}
	})

	t.Run("should send and store a response with a coding Marasi does not decode as received", func(t *testing.T) {
		coded := gzipped(t, text)
		for _, coding := range []string{"compress", "gzip, compress", "exi"} {
			t.Run(coding, func(t *testing.T) {
				response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Encoding: %s\r\nContent-Length: %d\r\n\r\n%s", coding, len(coded), coded)
				origin := newRawOrigin(t, response)
				_, addr, stored := serveEncodingProxy(t)

				roundTripThrough(t, addr, origin, response)

				res := storedResponse(t, stored)
				if string(res.Raw) != response {
					t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", response, res.Raw)
				}
				if _, ok := res.Metadata["decoded_content_encoding"]; ok {
					t.Fatalf("wanted no decoded_content_encoding\ngot: %v", res.Metadata)
				}
				if _, ok := res.Metadata["content_decoding_error"]; ok {
					t.Fatalf("wanted no content_decoding_error\ngot: %v", res.Metadata)
				}
			})
		}
	})
}

// codedWith is text with each of codings applied in order.
func codedWith(t *testing.T, codings []string, text string) string {
	t.Helper()
	for _, coding := range codings {
		var b bytes.Buffer
		var w io.WriteCloser
		switch coding {
		case "gzip":
			w = gzip.NewWriter(&b)
		case "deflate":
			w = zlib.NewWriter(&b)
		case "br":
			w = brotli.NewWriter(&b)
		case "zstd":
			zw, err := zstd.NewWriter(&b)
			if err != nil {
				t.Fatalf("creating the zstd writer: %v", err)
			}
			w = zw
		default:
			t.Fatalf("no encoder for %q", coding)
		}
		io.WriteString(w, text)
		if err := w.Close(); err != nil {
			t.Fatalf("closing the %s writer: %v", coding, err)
		}
		text = b.String()
	}
	return text
}

// storedBody is the body of a stored raw message.
func storedBody(t *testing.T, raw []byte) string {
	t.Helper()
	_, body, found := strings.Cut(string(raw), "\r\n\r\n")
	if !found {
		t.Fatalf("wanted a stored message with a head\ngot: %q", raw)
	}
	return body
}

func TestCompressedResponses_StoredAsToday(t *testing.T) {
	const text = `{"greeting":"hello, world","n":1}`
	coded := gzipped(t, text)
	response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n%s", len(coded), coded)

	// What Martian stored is what every reader shows: search, prettify, the
	// CLI, reports and the browser view all read the stored raw text, its
	// prettified copy and its content type and length.
	origin := newRawOrigin(t, response)
	_, addr, responses := serveEncodingProxy(t)
	conn, br := dialProxy(t, addr)
	fmt.Fprintf(conn, "GET http://%s/coded HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
	if _, err := http.ReadResponse(br, nil); err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	stored := storedResponse(t, responses)

	t.Run("should store the decoded body", func(t *testing.T) {
		if got := storedBody(t, stored.Raw); got != text {
			t.Fatalf("wanted the stored body %q\ngot: %q", text, got)
		}
	})
	t.Run("should store the prettified body", func(t *testing.T) {
		want, err := rawhttp.Prettify([]byte(text))
		if err != nil {
			t.Fatalf("prettifying: %v", err)
		}
		if got := storedBody(t, []byte(stored.Prettified)); got != string(want) || got == "" {
			t.Fatalf("wanted the prettified body %q\ngot: %q", want, got)
		}
	})
	t.Run("should store the content type, the decoded length and no Content-Encoding", func(t *testing.T) {
		if stored.ContentType != "application/json" || stored.Length != fmt.Sprint(len(text)) {
			t.Fatalf("wanted %q and %q\ngot: %q and %q", "application/json", fmt.Sprint(len(text)), stored.ContentType, stored.Length)
		}
		if strings.Contains(strings.ToLower(string(stored.Raw)), "content-encoding") {
			t.Fatalf("wanted no Content-Encoding in the stored response\ngot: %q", stored.Raw)
		}
	})
	t.Run("should record the decoding", func(t *testing.T) {
		wantDecodingMetadata(t, stored, "gzip", len(coded))
	})
}

func TestH1Engine_UndecodableResponses(t *testing.T) {
	t.Run("should send a gzip body that does not decode as received, and store it coded with the decoding error", func(t *testing.T) {
		const corrupt = "not gzip at all"
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n%s", len(corrupt), corrupt)
		origin := newRawOrigin(t, response)
		_, addr, stored := serveEncodingProxy(t)

		roundTripThrough(t, addr, origin, response)

		res := storedResponse(t, stored)
		if string(res.Raw) != response {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", response, res.Raw)
		}
		if got, _ := res.Metadata["content_decoding_error"].(string); got == "" {
			t.Fatalf("\nwanted:\nmetadata.content_decoding_error set\ngot:\n%v", res.Metadata)
		}
		if _, ok := res.Metadata["decoded_content_encoding"]; ok {
			t.Fatalf("\nwanted:\nno decoded_content_encoding\ngot:\n%v", res.Metadata)
		}
		if _, ok := res.Metadata["proxy_error"]; ok {
			t.Fatalf("\nwanted:\nno proxy_error\ngot:\n%v", res.Metadata)
		}
	})
}
