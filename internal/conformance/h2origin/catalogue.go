package h2origin

import (
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
}
