package h1origin

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
}
