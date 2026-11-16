// Package h1 is the conformance harness's HTTP/1.1 recording: each
// connection's messages as they were on the wire, parsed from the raw bytes
// in each direction, and the comparison of two recordings that leaves out
// hop-by-hop fields and framing. The h1 suites build their recordings with it.
package h1

import (
	"bytes"
	"strconv"
	"strings"
)

// Recording is one h1 case: the connections the harness client opened and
// the connections the origin accepted. Comparison reads only what each side
// received: the responses on a client connection and the requests on an
// origin connection.
type Recording struct {
	Client  []Connection `json:"client,omitempty"`
	Origin  []Connection `json:"origin,omitempty"`
	Verdict string       `json:"verdict,omitempty"` // a third-party tester's verdict, where one ran
}

// Connection is one recorded connection.
type Connection struct {
	// Tunnel is the start line of the proxy's answer to CONNECT, when the
	// client tunnelled. Raw and the messages start after it.
	Tunnel    string    `json:"tunnel,omitempty"`
	Requests  []Message `json:"requests"`  // client to server
	Responses []Message `json:"responses"` // server to client
	// Leftover holds the bytes in each direction after the last message
	// that parsed.
	Leftover Raw `json:"leftover,omitzero"`
	End      End `json:"end"`
	Raw      Raw `json:"raw"`
}

// Raw is a connection's bytes in each direction, base64 in JSON.
type Raw struct {
	ToServer []byte `json:"to_server,omitempty"`
	ToClient []byte `json:"to_client,omitempty"`
}

// End is how a connection ended, seen by the side that recorded it.
type End string

// Connection ends.
const (
	EndOpen     End = "open"        // still open when the case finished
	EndClosed   End = "closed"      // the peer closed it
	EndReset    End = "reset"       // the peer reset it
	EndTimeout  End = "timeout"     // no response came before the timeout
	EndClosedBy End = "closed-here" // the recorder closed it, as its script said
	EndResetBy  End = "reset-here"  // the recorder reset it, as its script said
)

// Message is one request or response as sent.
type Message struct {
	StartLine string  `json:"start_line"`
	Fields    []Field `json:"fields"`
	Body      []byte  `json:"body,omitempty"` // after de-framing
	Trailers  []Field `json:"trailers,omitempty"`
	Framing   Framing `json:"framing"`
	Truncated bool    `json:"truncated,omitempty"` // the connection ended inside the message
}

// Field is one field line as sent: the name with its case, and the raw
// value after the colon with its whitespace.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Framing is how a message's body was delimited.
type Framing struct {
	Kind   FramingKind `json:"kind"`
	Chunks []int       `json:"chunks,omitempty"` // chunk sizes, without the last chunk
}

// FramingKind names a body delimitation.
type FramingKind string

// Framing kinds.
const (
	FramingNone          FramingKind = "none" // no body by definition
	FramingContentLength FramingKind = "content-length"
	FramingChunked       FramingKind = "chunked"
	FramingClose         FramingKind = "close" // delimited by the connection closing
)

// Method is a request's method.
func (m Message) Method() string {
	method, _, _ := strings.Cut(m.StartLine, " ")
	return method
}

// Target is a request's request target.
func (m Message) Target() string {
	_, rest, _ := strings.Cut(m.StartLine, " ")
	target, _, _ := strings.Cut(rest, " ")
	return target
}

// Status is a response's status code, or 0 when it has none.
func (m Message) Status() int {
	_, rest, _ := strings.Cut(m.StartLine, " ")
	code, _, _ := strings.Cut(rest, " ")
	status, err := strconv.Atoi(code)
	if err != nil {
		return 0
	}
	return status
}

// Reason is a response's reason phrase.
func (m Message) Reason() string {
	parts := strings.SplitN(m.StartLine, " ", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// Values returns the raw values of every field line named name, any case.
func (m Message) Values(name string) []string {
	var values []string
	for _, field := range m.Fields {
		if strings.EqualFold(field.Name, name) {
			values = append(values, field.Value)
		}
	}
	return values
}

// NewConnection parses a connection's raw bytes. The connection has ended,
// so a close-delimited body ends with the bytes.
func NewConnection(raw Raw, end End) Connection {
	connection := Connection{End: end, Raw: raw, Requests: []Message{}, Responses: []Message{}}
	requests, restToServer := ParseRequests(raw.ToServer, true)
	methods := make([]string, len(requests))
	for i, request := range requests {
		methods[i] = request.Method()
	}
	responses, restToClient := ParseResponses(raw.ToClient, methods, true)
	connection.Requests = append(connection.Requests, requests...)
	connection.Responses = append(connection.Responses, responses...)
	connection.Leftover = Raw{ToServer: orNil(restToServer), ToClient: orNil(restToClient)}
	return connection
}

func orNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// Replace returns raw with every old replaced by new, in both directions.
// Suites use it to write a per-run value, such as an origin's port, the
// same way in every run.
func (r Raw) Replace(old, new string) Raw {
	return Raw{
		ToServer: bytes.ReplaceAll(r.ToServer, []byte(old), []byte(new)),
		ToClient: bytes.ReplaceAll(r.ToClient, []byte(old), []byte(new)),
	}
}
