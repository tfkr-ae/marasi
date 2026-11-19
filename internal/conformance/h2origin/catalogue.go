package h2origin

import (
	"bytes"
	"compress/gzip"
	"strings"

	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// dataRequest is the request of the two cases that must record the same:
// headers-with-data and data-padded-split.
var dataRequest = Request{Method: "GET", Path: "/data", Fields: h2.Fields("x-request-id", "abc", "accept", "text/plain")}

// Catalogue is the h2 origin fixture cases: the minimum that proves the
// recording format. Phase 5 grows it.
var Catalogue = []Case{
	{
		ID:       "headers-with-data",
		RFC:      "RFC 9113 §8.1 HTTP message framing: HEADERS then DATA",
		Requests: []Request{dataRequest},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "content-type", "text/plain", "x-origin", "fixture"),
			Data(0, true, "hello, world"),
		},
	},
	{
		// The same request and response as headers-with-data, with other
		// SETTINGS and the body split into padded frames: it must record
		// the same.
		ID:       "data-padded-split",
		RFC:      "RFC 9113 §6.1 DATA padding and §8.1 framing",
		Requests: []Request{dataRequest},
		Script: []Step{
			Settings(http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 7},
				http2.Setting{ID: http2.SettingInitialWindowSize, Val: 1 << 20}),
			Await(),
			Headers(0, false, ":status", "200", "content-type", "text/plain", "x-origin", "fixture"),
			PaddedData(0, false, "hel", 7),
			Data(0, false, "lo, "),
			Data(0, false, ""),
			PaddedData(0, true, "world", 3),
		},
	},
	{
		ID:  "trailers",
		RFC: "RFC 9113 §8.1 trailer section",
		Requests: []Request{{
			Method: "POST", Path: "/trailers",
			Fields:   h2.Fields("content-type", "text/plain", "te", "trailers"),
			Body:     "ping",
			Trailers: h2.Fields("x-request-checksum", "1"),
		}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "content-type", "text/plain", "trailer", "x-checksum"),
			Data(0, false, "pong"),
			Headers(0, true, "x-checksum", "abc", "x-trailer-two", "2"),
		},
	},
	{
		ID:       "rst-stream",
		RFC:      "RFC 9113 §6.4 RST_STREAM",
		Requests: []Request{{Method: "GET", Path: "/reset"}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "content-type", "text/plain"),
			Data(0, false, "partial"),
			Reset(0, http2.ErrCodeInternal),
		},
	},
	{
		ID:  "goaway",
		RFC: "RFC 9113 §6.8 GOAWAY",
		Requests: []Request{
			{Method: "GET", Path: "/first"},
			{Method: "GET", Path: "/second"},
		},
		Script: []Step{
			Await(),
			Await(),
			Headers(0, false, ":status", "200"),
			Data(0, true, "first"),
			GoAway(1, http2.ErrCodeNo),
		},
	},
	{
		// Repeated names, apart and together, in an order no sorting
		// gives, on both the request and the response.
		ID:  "field-order",
		RFC: "RFC 9113 §8.2 HTTP fields, and RFC 9110 §5.3 field order",
		Requests: []Request{{
			Method: "GET", Path: "/order",
			Fields: h2.Fields("x-b", "1", "accept", "*/*", "x-a", "2", "x-b", "3", "x-b", "4", "user-agent", "fixture"),
		}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "x-z", "1", "set-cookie", "a=1", "content-type", "text/plain",
				"set-cookie", "b=2", "x-z", "2", "x-a", "3", "x-z", "3"),
			Data(0, true, "ordered"),
		},
	},
	{
		// An HTTP/2 client may split cookie into crumbs, and a hop that
		// stays HTTP/2 keeps them as they are.
		ID:  "cookie-crumbs",
		RFC: "RFC 9113 §8.2.3 compressing the Cookie header field",
		Requests: []Request{{
			Method: "GET", Path: "/cookies",
			Fields: h2.Fields("cookie", "a=1", "accept", "*/*", "cookie", "b=2; c=3", "cookie", "d=4"),
		}},
		Script: []Step{
			Await(),
			Headers(0, true, ":status", "204", "set-cookie", "e=5"),
		},
	},
	{
		ID:       "early-hints",
		RFC:      "RFC 9113 §8.1 interim responses, and RFC 8297 103 Early Hints",
		Requests: []Request{{Method: "GET", Path: "/hints"}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "103", "link", "</style.css>; rel=preload; as=style"),
			Headers(0, false, ":status", "103", "link", "</script.js>; rel=preload; as=script"),
			Headers(0, false, ":status", "200", "content-type", "text/html", "link", "</style.css>; rel=preload; as=style"),
			Data(0, true, "<html></html>"),
		},
	},
	{
		// Both messages are malformed (§8.1.1); Marasi forwards each as it
		// came, so each peer sees what it would going direct.
		ID:  "content-length-mismatch",
		RFC: "RFC 9113 §8.1.1 malformed messages: content-length",
		Requests: []Request{{
			Method: "POST", Path: "/length",
			Fields: h2.Fields("content-type", "text/plain", "content-length", "10"),
			Body:   "short",
		}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "content-type", "text/plain", "content-length", "3"),
			Data(0, true, "longer than three"),
		},
	},
	{
		// A compressed body goes through as its bytes: no hop decodes it.
		ID:  "gzip-body",
		RFC: "RFC 9110 §8.4.1.3 gzip coding",
		Requests: []Request{{
			Method: "POST", Path: "/gzip",
			Fields: h2.Fields("content-type", "text/plain", "content-encoding", "gzip", "accept-encoding", "gzip"),
			Body:   gzipped("request body, compressed"),
		}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "content-type", "text/plain", "content-encoding", "gzip", "vary", "accept-encoding"),
			Data(0, true, gzipped("response body, compressed")),
		},
	},
	{
		// Trailers straight after the header block, with no DATA between.
		ID:  "trailers-no-data",
		RFC: "RFC 9113 §8.1 trailer section",
		Requests: []Request{{
			Method: "GET", Path: "/trailers-only",
			Fields:   h2.Fields("te", "trailers"),
			Trailers: h2.Fields("x-request-trailer", "1"),
		}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "trailer", "grpc-status"),
			Headers(0, true, "grpc-status", "0", "grpc-message", "ok"),
		},
	},
	{
		// Five streams open at once, answered out of order with their
		// DATA interleaved.
		ID:  "concurrent-streams",
		RFC: "RFC 9113 §5 streams and multiplexing",
		Requests: []Request{
			{Method: "GET", Path: "/s0"},
			{Method: "GET", Path: "/s1"},
			{Method: "POST", Path: "/s2", Fields: h2.Fields("content-type", "text/plain"), Body: "two"},
			{Method: "GET", Path: "/s3"},
			{Method: "GET", Path: "/s4"},
		},
		Script: []Step{
			Await(), Await(), Await(), Await(), Await(),
			Headers(3, false, ":status", "200", "x-stream", "3"),
			Headers(1, false, ":status", "200", "x-stream", "1"),
			Data(3, false, "three "),
			Headers(4, true, ":status", "204", "x-stream", "4"),
			Data(1, false, "one "),
			Headers(0, false, ":status", "200", "x-stream", "0"),
			Headers(2, false, ":status", "201", "x-stream", "2"),
			Data(3, true, "done"),
			Data(0, true, "zero"),
			Data(2, true, "two done"),
			Data(1, true, "done"),
		},
	},
	{
		// GOAWAY that names every open stream as processed: they finish
		// after it, and no new stream may open.
		ID:  "goaway-in-flight",
		RFC: "RFC 9113 §6.8 GOAWAY: graceful shutdown",
		Requests: []Request{
			{Method: "GET", Path: "/first"},
			{Method: "GET", Path: "/second"},
		},
		Script: []Step{
			Await(),
			Await(),
			Headers(0, false, ":status", "200"),
			GoAway(2, http2.ErrCodeNo),
			Data(0, true, "first"),
			Headers(1, false, ":status", "200"),
			Data(1, true, "second"),
		},
	},
	{
		// Both header blocks split into a HEADERS frame and CONTINUATION
		// frames, with END_STREAM on the HEADERS frame of the request.
		ID:  "continuation",
		RFC: "RFC 9113 §6.10 CONTINUATION",
		Requests: []Request{{
			Method: "GET", Path: "/continued",
			Fields:   h2.Fields("x-long", strings.Repeat("request ", 16), "accept", "*/*", "x-after", "continued"),
			Fragment: 16,
		}},
		Script: []Step{
			Await(),
			ContinuedHeaders(0, false, 10, ":status", "200", "content-type", "text/plain",
				"x-long", strings.Repeat("response ", 16), "x-after", "continued"),
			Data(0, true, "continued"),
		},
	},
	{
		// Bodies longer than the initial 65,535-byte windows, so neither
		// side can send all of one before the other gives window back.
		ID:  "flow-control-window",
		RFC: "RFC 9113 §6.9 flow control",
		Requests: []Request{{
			Method: "POST", Path: "/large",
			Fields: h2.Fields("content-type", "application/octet-stream"),
			Body:   largeBody("request"),
		}},
		Script: []Step{
			Await(),
			Headers(0, false, ":status", "200", "content-type", "application/octet-stream"),
			Data(0, true, largeBody("response")),
		},
	},
}

// gzipped compresses s with gzip. The header carries no name or time, so
// the bytes are the same on every run.
func gzipped(s string) string {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.String()
}

// largeBody is 81,920 bytes starting with label: more than the initial
// connection and stream windows of 65,535 bytes.
func largeBody(label string) string {
	line := label + strings.Repeat(".", 63-len(label)) + "\n"
	return strings.Repeat(line, 1280)
}
