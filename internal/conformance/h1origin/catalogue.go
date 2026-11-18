package h1origin

import (
	"encoding/hex"
	"strings"
	"time"
)

// gzipHello is "hello, world" gzip-compressed, kept as bytes so a new Go
// release cannot change the baseline.
var gzipHello = func() string {
	b, err := hex.DecodeString("1f8b08000000000002ffca48cdc9c9d75128cf2fca4901040000ffff3a72abff0c000000")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// brHello is "hello, world" brotli-compressed, kept as bytes for the same
// reason.
var brHello = func() string {
	b, err := hex.DecodeString("1b0b0000a440586a90496af29c1e01")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// resetSettle is how long an origin waits after the bytes it sends before
// a reset, so the peer has read them before the reset can discard them.
const resetSettle = 100 * time.Millisecond

// Catalogue is every origin fixture case, in report order. Every case also
// runs the leg rules on both legs; the cases after chunked-trailers each
// exercise one rule.
var Catalogue = []Case{
	{
		ID:  "chunked-trailers",
		RFC: "RFC 9112 §7.1.2 chunked trailer section",
		Requests: []string{
			"GET /chunked HTTP/1.1\r\nHost: {authority}\r\nX-Request-ID: 1\r\nx-lower-case: request\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nX-ORIGIN-case: Mixed\r\nx-lower: yes\r\n" +
				"Transfer-Encoding: chunked\r\nTrailer: X-Checksum\r\n\r\n" +
				"5\r\nhello\r\n7\r\n, world\r\n0\r\nX-Checksum: 42\r\n\r\n"),
		},
	},
	{
		ID:  "connection-named-fields",
		RFC: "RFC 9110 §7.6.1 remove Connection and the fields it names",
		Requests: []string{
			"GET /hop HTTP/1.1\r\nHost: {authority}\r\nConnection: X-Hop\r\nX-Hop: request\r\nX-End: request\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: X-Back\r\nX-Back: response\r\nX-End: response\r\n\r\nok"),
		},
	},
	{
		ID:  "request-body-length",
		RFC: "RFC 9112 §6.2 Content-Length",
		Requests: []string{
			"POST /length HTTP/1.1\r\nHost: {authority}\r\nContent-Type: text/plain\r\nContent-Length: 11\r\n\r\nhello world",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"),
		},
	},
	{
		ID:  "request-body-chunked",
		RFC: "RFC 9112 §6.1 never send Content-Length with Transfer-Encoding",
		Requests: []string{
			"POST /chunked HTTP/1.1\r\nHost: {authority}\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n" +
				"5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"),
		},
	},
	{
		ID:  "same-name-fields",
		RFC: "RFC 9110 §5.3 keep the order of same-name field lines",
		Requests: []string{
			"GET /order HTTP/1.1\r\nHost: {authority}\r\nX-Multi: one\r\nX-Other: between\r\nX-Multi: two\r\nX-Multi: three\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nSet-Cookie: b=2\r\nX-Other: between\r\nSet-Cookie: a=1\r\nX-Multi: z\r\nX-Multi: y\r\n" +
				"Content-Length: 2\r\n\r\nok"),
		},
	},

	// Response bodies.
	{
		ID:       "chunked-odd-sizes",
		RFC:      "RFC 9112 §7.1 chunk sizes in hex, with leading zeros and extensions",
		Requests: []string{"GET /odd-chunks HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Sum\r\n\r\n" +
				"1\r\na\r\n00A\r\n0123456789\r\n1F;name=value\r\n" + strings.Repeat("b", 31) + "\r\n" +
				"c;quoted=\"x y\"\r\n" + strings.Repeat("c", 12) + "\r\n000\r\nX-Sum: 54\r\n\r\n"),
		},
	},
	{
		ID:       "content-length-body",
		RFC:      "RFC 9112 §6.2 Content-Length delimits the body",
		Requests: []string{"GET /length HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: 64\r\n\r\n" +
				strings.Repeat("0123456789abcdef", 4)),
		},
	},
	{
		ID:       "close-delimited-body",
		RFC:      "RFC 9112 §6.3 a response with neither Content-Length nor Transfer-Encoding ends when the connection closes",
		Requests: []string{"GET /close HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nread until the server closes"),
			Close(),
		},
	},

	// Field lines.
	{
		ID:       "field-name-case",
		RFC:      "RFC 9110 §5.1 field names are case-insensitive and sent as the sender wrote them",
		Requests: []string{"GET /case HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\nWWW-Authenticate: Basic realm=\"x\"\r\n" +
				"X-XSS-Protection: 0\r\nx-MiXeD-cAsE: 1\r\nETAG: \"v1\"\r\nCONTENT-LENGTH: 2\r\n\r\nok"),
		},
	},
	{
		ID:       "field-value-whitespace",
		RFC:      "RFC 9110 §5.5 field values with optional whitespace around and inside them",
		Requests: []string{"GET /whitespace HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nX-Leading:    lead\r\nX-Trailing: trail   \r\nX-Tab:\ttab\t\r\n" +
				"X-Inner: a  b\t c\r\nX-Bare:none\r\nX-Empty:\r\nContent-Length: 2\r\n\r\nok"),
		},
	},
	{
		ID:  "keep-alive",
		RFC: "RFC 9110 §7.6.1 Keep-Alive is hop-by-hop",
		Requests: []string{
			"GET /keep-alive HTTP/1.1\r\nHost: {authority}\r\nConnection: keep-alive\r\nKeep-Alive: timeout=30\r\nX-End: request\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nConnection: keep-alive\r\nKeep-Alive: timeout=5, max=100\r\nX-End: response\r\n" +
				"Content-Length: 2\r\n\r\nok"),
		},
	},

	// Transfer codings.
	{
		ID:       "transfer-encoding-gzip",
		RFC:      "RFC 9112 §6.1 a transfer coding other than chunked, applied before chunked",
		Requests: []string{"GET /gzip HTTP/1.1\r\nHost: {authority}\r\nTE: gzip\r\nConnection: TE\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: gzip, chunked\r\n\r\n" +
				"24\r\n" + gzipHello + "\r\n0\r\n\r\n"),
		},
	},
	// Content codings, which a proxy passes on as the origin sent them.
	{
		ID:       "content-encoding-gzip",
		RFC:      "RFC 9110 §8.4 a content coding is end to end",
		Requests: []string{"GET /content-gzip HTTP/1.1\r\nHost: {authority}\r\nAccept-Encoding: gzip\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Encoding: gzip\r\nContent-Length: 36\r\n\r\n" + gzipHello),
		},
	},
	{
		ID:       "content-encoding-br",
		RFC:      "RFC 9110 §8.4 a content coding is end to end",
		Requests: []string{"GET /content-br HTTP/1.1\r\nHost: {authority}\r\nAccept-Encoding: br\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Encoding: br\r\nTransfer-Encoding: chunked\r\n\r\n" +
				"f\r\n" + brHello + "\r\n0\r\n\r\n"),
		},
	},
	{
		ID:       "content-length-and-transfer-encoding",
		RFC:      "RFC 9112 §6.3 Transfer-Encoding overrides Content-Length, which a proxy must remove",
		Requests: []string{"GET /cl-te HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 99\r\nTransfer-Encoding: chunked\r\n\r\n" +
				"5\r\nhello\r\n0\r\n\r\n"),
		},
	},

	// Responses with no body, and interim responses.
	{
		ID:  "interim-100-continue",
		RFC: "RFC 9110 §15.2 a proxy forwards 1xx responses",
		Requests: []string{
			"POST /continue HTTP/1.1\r\nHost: {authority}\r\nExpect: 100-continue\r\nContent-Length: 5\r\n\r\nhello",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 100 Continue\r\n\r\n"),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"),
		},
	},
	{
		ID:       "interim-103-early-hints",
		RFC:      "RFC 8297 §2 103 Early Hints, forwarded as RFC 9110 §15.2 says",
		Requests: []string{"GET /hints HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload; as=style\r\n\r\n"),
			Send("HTTP/1.1 200 OK\r\nLink: </style.css>; rel=preload; as=style\r\nContent-Length: 2\r\n\r\nok"),
		},
	},
	{
		ID:       "head",
		RFC:      "RFC 9110 §9.3.2 a response to HEAD has no body, whatever its Content-Length",
		Requests: []string{"HEAD /head HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 11\r\n\r\n"),
		},
	},
	{
		ID:       "no-content-204",
		RFC:      "RFC 9110 §15.3.5 a 204 response has no body",
		Requests: []string{"DELETE /item HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 204 No Content\r\nX-Deleted: item\r\n\r\n"),
		},
	},
	{
		ID:  "not-modified-304",
		RFC: "RFC 9110 §15.4.5 a 304 response has no body, whatever its Content-Length",
		Requests: []string{
			"GET /cached HTTP/1.1\r\nHost: {authority}\r\nIf-None-Match: \"v1\"\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 304 Not Modified\r\nETag: \"v1\"\r\nCache-Control: max-age=60\r\nContent-Length: 11\r\n\r\n"),
		},
	},

	// Origins that fail mid-response.
	{
		ID:       "early-close",
		RFC:      "RFC 9112 §8 a message cut short by a close is incomplete",
		Requests: []string{"GET /early-close HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 20\r\n\r\nonly ten b"),
			Close(),
		},
	},
	{
		ID:       "reset-mid-body",
		RFC:      "RFC 9112 §8 a message cut short by a reset is incomplete",
		Requests: []string{"GET /reset HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n"),
			Delay(resetSettle),
			Reset(),
		},
	},

	// Persistent connections.
	{
		ID:  "kept-alive-requests",
		RFC: "RFC 9112 §9.3 several requests on one persistent connection",
		Requests: []string{
			"GET /first HTTP/1.1\r\nHost: {authority}\r\n\r\n",
			"POST /second HTTP/1.1\r\nHost: {authority}\r\nContent-Length: 3\r\n\r\ntwo",
			"GET /third HTTP/1.1\r\nHost: {authority}\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nfirst"),
			Await(),
			Send("HTTP/1.1 201 Created\r\nTransfer-Encoding: chunked\r\n\r\n6\r\nsecond\r\n0\r\n\r\n"),
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nthird"),
		},
	},

	// A request that names the proxy itself.
	{
		ID:       "loop-back",
		RFC:      "RFC 9110 §7.6 a proxy must not forward a request back to itself",
		Requests: []string{"GET /loop HTTP/1.1\r\nHost: {proxy}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"),
		},
	},

	// Request fields.
	{
		ID:       "request-fields-not-added",
		RFC:      "RFC 9110 §7.6 a proxy forwards the client's fields and adds no end-to-end ones of its own",
		Requests: []string{"GET /bare HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"),
		},
	},
	{
		ID:  "request-fields-as-sent",
		RFC: "RFC 9110 §5.3 and §5.5 request field names, order and values reach the origin as sent",
		Requests: []string{
			"GET /fields HTTP/1.1\r\nuser-agent: conformance/1\r\nhost: {authority}\r\nACCEPT: text/plain\r\n" +
				"cookie: a=1\r\nX-Pad:  two spaces \r\ncookie: b=2\r\naccept-encoding: identity\r\nX-Empty:\r\n\r\n",
		},
		Script: []Step{
			Await(),
			Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"),
		},
	},
}
