package h1origin

// Catalogue is every origin fixture case, in report order.
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
}
