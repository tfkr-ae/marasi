package streamorigin

import (
	"encoding/binary"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// Protocols a case speaks on both legs.
const (
	ProtocolH2    = "h2"
	ProtocolHTTP1 = "http/1.1"
)

// Case is one stream origin fixture case: one request/response pair on one
// connection, with a script for each side. The client runs its script from
// the start; the origin runs its own once the request's head arrives.
type Case struct {
	ID       string // case ID before the client approach
	RFC      string // the RFC or protocol rule the case exercises
	Protocol string // ProtocolH2 (TLS inside CONNECT only) or ProtocolHTTP1 (every approach)
	Client   []Step
	Origin   []Step
}

// approaches are the client approaches the case's protocol can use.
func (c Case) approaches() []conformance.Approach {
	if c.Protocol == ProtocolH2 {
		return []conformance.Approach{conformance.ApproachConnectTLS}
	}
	return []conformance.Approach{conformance.ApproachAbsolute, conformance.ApproachConnectPlain, conformance.ApproachConnectTLS}
}

// StepKind names what a script step does.
type StepKind string

// Step kinds.
const (
	StepHead     StepKind = "head"     // send a header section
	StepMessage  StepKind = "message"  // send body bytes: one stream message
	StepTrailers StepKind = "trailers" // send a trailer section, ending the direction
	StepAwait    StepKind = "await"    // wait for the next arrival of a kind from the peer
	StepAnswer   StepKind = "answer"   // tell the origin, outside the proxy, what was awaited arrived
)

// Step is one step of a side's script.
type Step struct {
	Kind StepKind
	// Fields are a header or trailer section. A head starts with its
	// pseudo-header fields: :method and :path, or :status. On HTTP/1.1
	// they become the start line and the rest are sent as they are, with
	// "{authority}" replaced by the origin's host:port; on h2 the client
	// adds :scheme and :authority after :method.
	Fields []h2.Field
	Data   string // StepMessage
	End    bool   // StepHead, StepMessage: the direction ends with this step
	Await  string // StepAwait: the arrival kind awaited
}

// Head sends a header section of name, value pairs.
func Head(end bool, pairs ...string) Step {
	return Step{Kind: StepHead, End: end, Fields: h2.Fields(pairs...)}
}

// Message sends one stream message's bytes.
func Message(end bool, data string) Step { return Step{Kind: StepMessage, End: end, Data: data} }

// Trailers sends a trailer section and ends the direction.
func Trailers(pairs ...string) Step { return Step{Kind: StepTrailers, Fields: h2.Fields(pairs...)} }

// Await waits for the next arrival of kind from the peer. On timeout the
// side records it and goes on with its script, so a proxy that holds
// messages back still gets the rest of the case and the order shows it.
func Await(kind string) Step { return Step{Kind: StepAwait, Await: kind} }

// Answer tells the origin, which awaits KindAnswer, that the client saw
// what it waited for: a server-sent event stream has no request body to
// answer in.
func Answer() Step { return Step{Kind: StepAnswer} }

// GRPC is payload in a gRPC Length-Prefixed-Message: an uncompressed
// flag and a big-endian length. gRPC-Web and Connect messages are the same.
func GRPC(payload string) string { return envelope(0, payload) }

// GRPCWebTrailers is a gRPC-Web trailers frame: the trailer block in an
// envelope with flag 0x80 (gRPC PROTOCOL-WEB.md).
func GRPCWebTrailers(block string) string { return envelope(0x80, block) }

// ConnectEnd is a Connect end-of-stream message: its JSON in an envelope
// with flag 0x02 (Connect protocol, Streaming-Response).
func ConnectEnd(json string) string { return envelope(0x02, json) }

func envelope(flags byte, payload string) string {
	var prefix [5]byte
	prefix[0] = flags
	binary.BigEndian.PutUint32(prefix[1:], uint32(len(payload)))
	return string(prefix[:]) + payload
}

// grpcRequest is the head of a gRPC call's request (gRPC PROTOCOL-HTTP2.md,
// Requests).
func grpcRequest(end bool, path string) Step {
	return Head(end, ":method", "POST", ":path", path, "content-type", "application/grpc", "te", "trailers")
}

// Catalogue is the stream origin fixture cases. The wait-for-answer cases
// deadlock through a proxy that reads a body whole before forwarding it:
// each side sends its next stream message only after the peer's last one
// arrived.
var Catalogue = []Case{
	{
		// Bidirectional streaming: the origin answers each request
		// message before the request ends.
		ID:       "grpc-wait-for-answer",
		RFC:      "gRPC PROTOCOL-HTTP2.md bidirectional streaming, over RFC 9113 §8.1 HTTP message framing",
		Protocol: ProtocolH2,
		Client: []Step{
			grpcRequest(false, "/conformance.Stream/PingPong"),
			Await(KindMessage),
			Message(false, GRPC("pong 1")),
			Await(KindMessage),
			Message(true, GRPC("pong 2")),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "content-type", "application/grpc"),
			Message(false, GRPC("ping 1")),
			Await(KindMessage),
			Message(false, GRPC("ping 2")),
			Await(KindEnd),
			Trailers("grpc-status", "0", "grpc-message", ""),
		},
	},
	{
		// An error with no messages: the status goes in the one header
		// section, which ends the stream.
		ID:       "grpc-trailers-only",
		RFC:      "gRPC PROTOCOL-HTTP2.md Trailers-Only response",
		Protocol: ProtocolH2,
		Client: []Step{
			grpcRequest(false, "/conformance.Stream/Unknown"),
			Message(true, GRPC("hello")),
			Await(KindEnd),
		},
		Origin: []Step{
			Await(KindEnd),
			Head(true, ":status", "200", "content-type", "application/grpc", "grpc-status", "12", "grpc-message", "unknown method"),
		},
	},
	{
		// Server streaming over gRPC-Web: the status goes in a trailers
		// frame at the end of the body, not in HTTP trailers.
		ID:       "grpc-web-trailers-frame-h2",
		RFC:      "gRPC PROTOCOL-WEB.md trailers frame, over RFC 9113 §8.1 HTTP message framing",
		Protocol: ProtocolH2,
		Client: []Step{
			Head(false, ":method", "POST", ":path", "/conformance.Stream/Watch",
				"content-type", "application/grpc-web+proto", "x-grpc-web", "1"),
			Message(true, GRPC("watch")),
			Await(KindMessage),
			Answer(),
			Await(KindEnd),
		},
		Origin: []Step{
			Await(KindEnd),
			Head(false, ":status", "200", "content-type", "application/grpc-web+proto"),
			Message(false, GRPC("tick 1")),
			Await(KindAnswer),
			Message(false, GRPC("tick 2")),
			Message(true, GRPCWebTrailers("grpc-status: 0\r\n")),
		},
	},
	{
		// Bidirectional Connect streaming: the origin answers each
		// request message before the request ends, and ends its
		// response with an end-of-stream message.
		ID:       "connect-wait-for-answer-h2",
		RFC:      "Connect protocol bidirectional streaming and EndStreamResponse, over RFC 9113 §8.1 HTTP message framing",
		Protocol: ProtocolH2,
		Client: []Step{
			Head(false, ":method", "POST", ":path", "/conformance.Stream/PingPong",
				"content-type", "application/connect+proto", "connect-protocol-version", "1"),
			Await(KindMessage),
			Message(false, GRPC("pong 1")),
			Await(KindMessage),
			Message(true, GRPC("pong 2")),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "content-type", "application/connect+proto"),
			Message(false, GRPC("ping 1")),
			Await(KindMessage),
			Message(false, GRPC("ping 2")),
			Await(KindEnd),
			Message(true, ConnectEnd("{}")),
		},
	},
	{
		ID:       "ndjson-wait-for-answer-h2",
		RFC:      "NDJSON (one JSON text per line), over RFC 9113 §8.1 HTTP message framing",
		Protocol: ProtocolH2,
		Client: []Step{
			Head(true, ":method", "GET", ":path", "/logs", "accept", "application/x-ndjson"),
			Await(KindMessage),
			Answer(),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "content-type", "application/x-ndjson"),
			Message(false, "{\"n\":1}\n"),
			Await(KindAnswer),
			Message(true, "{\"n\":2}\n"),
		},
	},
	{
		ID:       "sse-wait-for-answer-h2",
		RFC:      "WHATWG HTML §9.2 server-sent events, over RFC 9113 §8.1 HTTP message framing",
		Protocol: ProtocolH2,
		Client: []Step{
			Head(true, ":method", "GET", ":path", "/events", "accept", "text/event-stream"),
			Await(KindMessage),
			Answer(),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "content-type", "text/event-stream", "cache-control", "no-cache"),
			Message(false, "id: 1\ndata: first\n\n"),
			Await(KindAnswer),
			Message(true, "id: 2\ndata: second\n\n"),
		},
	},
	{
		ID:       "sse-wait-for-answer-http1",
		RFC:      "WHATWG HTML §9.2 server-sent events, over RFC 9112 §7.1 chunked transfer coding",
		Protocol: ProtocolHTTP1,
		Client: []Step{
			Head(true, ":method", "GET", ":path", "/events", "Host", "{authority}", "Accept", "text/event-stream"),
			Await(KindMessage),
			Answer(),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "Content-Type", "text/event-stream", "Cache-Control", "no-cache",
				"Transfer-Encoding", "chunked"),
			Message(false, "id: 1\ndata: first\n\n"),
			Await(KindAnswer),
			Message(true, "id: 2\ndata: second\n\n"),
		},
	},
	{
		// gRPC-Web in both directions over chunked bodies: the origin
		// answers each request message before the request ends, and ends
		// with a trailers frame.
		ID:       "grpc-web-wait-for-answer-http1",
		RFC:      "gRPC PROTOCOL-WEB.md over RFC 9112 §7.1 chunked transfer coding",
		Protocol: ProtocolHTTP1,
		Client: []Step{
			Head(false, ":method", "POST", ":path", "/conformance.Stream/PingPong", "Host", "{authority}",
				"Content-Type", "application/grpc-web+proto", "X-Grpc-Web", "1", "Transfer-Encoding", "chunked"),
			Await(KindMessage),
			Message(false, GRPC("pong 1")),
			Await(KindMessage),
			Message(true, GRPC("pong 2")),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "Content-Type", "application/grpc-web+proto", "Transfer-Encoding", "chunked"),
			Message(false, GRPC("ping 1")),
			Await(KindMessage),
			Message(false, GRPC("ping 2")),
			Await(KindEnd),
			Message(true, GRPCWebTrailers("grpc-status: 0\r\n")),
		},
	},
	{
		ID:       "connect-wait-for-answer-http1",
		RFC:      "Connect protocol streaming and EndStreamResponse, over RFC 9112 §7.1 chunked transfer coding",
		Protocol: ProtocolHTTP1,
		Client: []Step{
			Head(false, ":method", "POST", ":path", "/conformance.Stream/PingPong", "Host", "{authority}",
				"Content-Type", "application/connect+proto", "Connect-Protocol-Version", "1", "Transfer-Encoding", "chunked"),
			Await(KindMessage),
			Message(false, GRPC("pong 1")),
			Await(KindMessage),
			Message(true, GRPC("pong 2")),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "Content-Type", "application/connect+proto", "Transfer-Encoding", "chunked"),
			Message(false, GRPC("ping 1")),
			Await(KindMessage),
			Message(false, GRPC("ping 2")),
			Await(KindEnd),
			Message(true, ConnectEnd("{}")),
		},
	},
	{
		// A response with no length and no chunked coding runs until the
		// origin closes its connection (RFC 9112 §6.3).
		ID:       "ndjson-wait-for-answer-http1",
		RFC:      "NDJSON (one JSON text per line), over an RFC 9112 §6.3 body read until close",
		Protocol: ProtocolHTTP1,
		Client: []Step{
			Head(true, ":method", "GET", ":path", "/logs", "Host", "{authority}", "Accept", "application/x-ndjson"),
			Await(KindMessage),
			Answer(),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "Content-Type", "application/x-ndjson"),
			Message(false, "{\"n\":1}\n"),
			Await(KindAnswer),
			Message(true, "{\"n\":2}\n"),
		},
	},
	// The stall cases are ordinary pairs: their heads name no message
	// framing, so Marasi reads each body whole until it stalls for 2
	// seconds after some arrived, then forwards it as it arrives. The
	// waiting side waits longer than that. The recording cuts a text/plain
	// body into lines so a side can wait on part of it.
	{
		ID:       "ordinary-request-stall-http1",
		RFC:      "RFC 9112 §7.1 chunked transfer coding: a request body that stalls partway",
		Protocol: ProtocolHTTP1,
		Client: []Step{
			Head(false, ":method", "POST", ":path", "/upload", "Host", "{authority}", "Content-Type", "text/plain",
				"Transfer-Encoding", "chunked"),
			Message(false, "part 1\n"),
			Await(KindMessage),
			Message(true, "part 2\n"),
			Await(KindEnd),
		},
		Origin: []Step{
			Await(KindMessage),
			Head(false, ":status", "200", "Content-Type", "text/plain", "Content-Length", "11"),
			Message(true, "got part 1\n"),
			Await(KindEnd),
		},
	},
	{
		ID:       "ordinary-request-stall-h2",
		RFC:      "RFC 9113 §8.1 HTTP message framing: a request body that stalls partway",
		Protocol: ProtocolH2,
		Client: []Step{
			Head(false, ":method", "POST", ":path", "/upload", "content-type", "text/plain"),
			Message(false, "part 1\n"),
			Await(KindMessage),
			Message(true, "part 2\n"),
			Await(KindEnd),
		},
		Origin: []Step{
			Await(KindMessage),
			Head(false, ":status", "200", "content-type", "text/plain"),
			Message(true, "got part 1\n"),
			Await(KindEnd),
		},
	},
	{
		ID:       "ordinary-response-stall-http1",
		RFC:      "RFC 9112 §7.1 chunked transfer coding: a response body that stalls partway",
		Protocol: ProtocolHTTP1,
		Client: []Step{
			Head(true, ":method", "GET", ":path", "/download", "Host", "{authority}"),
			Await(KindMessage),
			Answer(),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "Content-Type", "text/plain", "Transfer-Encoding", "chunked"),
			Message(false, "first\n"),
			Await(KindAnswer),
			Message(true, "second\n"),
		},
	},
	{
		ID:       "ordinary-response-stall-h2",
		RFC:      "RFC 9113 §8.1 HTTP message framing: a response body that stalls partway",
		Protocol: ProtocolH2,
		Client: []Step{
			Head(true, ":method", "GET", ":path", "/download"),
			Await(KindMessage),
			Answer(),
			Await(KindEnd),
		},
		Origin: []Step{
			Head(false, ":status", "200", "content-type", "text/plain"),
			Message(false, "first\n"),
			Await(KindAnswer),
			Message(true, "second\n"),
		},
	},
}
