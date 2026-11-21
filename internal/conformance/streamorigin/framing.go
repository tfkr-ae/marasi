package streamorigin

import (
	"bytes"
	"encoding/binary"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// Message framings the splitter knows, named as in GLOSSARY.md.
const (
	framingNone    = ""
	framingGRPC    = "grpc"
	framingGRPCWeb = "grpc-web"
	framingConnect = "connect"
	framingSSE     = "sse"
	framingNDJSON  = "ndjson"
	// framingLines is the recording's own cut, not a message framing: an
	// ordinary text/plain body is recorded line by line as it arrives, so
	// a stall fixture can wait on part of a body that Marasi reads whole
	// until it stalls.
	framingLines = "lines"
)

// framingOf is the message framing a direction's content type names: the
// recording cuts that direction's body into stream messages by it.
func framingOf(contentType string) string {
	mediaType, _, _ := strings.Cut(strings.ToLower(contentType), ";")
	mediaType = strings.TrimSpace(mediaType)
	switch {
	case mediaType == "application/grpc" || strings.HasPrefix(mediaType, "application/grpc+"):
		return framingGRPC
	case mediaType == "application/grpc-web" || strings.HasPrefix(mediaType, "application/grpc-web+"):
		return framingGRPCWeb
	case strings.HasPrefix(mediaType, "application/connect+"):
		return framingConnect
	case mediaType == "text/event-stream":
		return framingSSE
	case mediaType == "application/x-ndjson" || mediaType == "application/jsonl":
		return framingNDJSON
	case mediaType == "text/plain":
		return framingLines
	}
	return framingNone
}

// splitter cuts one direction's body into stream messages as its bytes
// arrive, however they were split on the wire.
type splitter struct {
	framing string
	held    []byte
	// coding is the body's content coding (coding.go), "" for none;
	// coded is the compressed body so far, and taken how much of what it
	// inflates to was cut already.
	coding string
	coded  []byte
	taken  int
}

// head sets the framing and coding a body's head names.
func (s *splitter) head(contentType string, fields []h2.Field) {
	s.framing = framingOf(contentType)
	s.coding = headCoding(fields)
}

// write takes the next bytes of the body and returns every message they
// complete. A compressed body is cut as it inflates.
func (s *splitter) write(data []byte) [][]byte {
	if s.coding != "" {
		s.coded = append(s.coded, data...)
		out := inflated(s.coding, s.coded)
		if len(out) < s.taken {
			return nil
		}
		data = out[s.taken:]
		s.taken = len(out)
	}
	s.held = append(s.held, data...)
	var messages [][]byte
	for {
		n := s.next()
		if n == 0 {
			return messages
		}
		messages = append(messages, bytes.Clone(s.held[:n]))
		s.held = s.held[n:]
	}
}

// next is the length of the first whole message held, or 0.
func (s *splitter) next() int {
	switch s.framing {
	case framingGRPC, framingGRPCWeb, framingConnect:
		// A 5-byte envelope: a flags byte and a big-endian length
		// (gRPC PROTOCOL-HTTP2.md, Length-Prefixed-Message). A gRPC-Web
		// trailers frame and a Connect end-of-stream message are
		// envelopes too.
		if len(s.held) < 5 {
			return 0
		}
		if n := 5 + int(binary.BigEndian.Uint32(s.held[1:5])); len(s.held) >= n {
			return n
		}
	case framingSSE:
		// An event ends at a blank line (WHATWG HTML §9.2.5 event stream
		// interpretation).
		first := 0
		for _, end := range []string{"\n\n", "\r\n\r\n"} {
			if i := bytes.Index(s.held, []byte(end)); i >= 0 && (first == 0 || i+len(end) < first) {
				first = i + len(end)
			}
		}
		return first
	case framingNDJSON, framingLines:
		// A line ends at a LF.
		return bytes.IndexByte(s.held, '\n') + 1
	}
	return 0
}

// end returns the arrivals of what is still held when the body ends: the
// whole body with no framing, or a message cut short, then a compressed
// body's bytes whole.
func (s *splitter) end(side string) []Arrival {
	var arrivals []Arrival
	if len(s.held) > 0 {
		kind := KindPartial
		if s.framing == framingNone {
			kind = KindBody
		}
		arrivals = append(arrivals, Arrival{Side: side, Kind: kind, Data: s.held})
	}
	if len(s.coded) > 0 {
		arrivals = append(arrivals, Arrival{Side: side, Kind: KindCoded, Data: s.coded})
	}
	s.held, s.coded = nil, nil
	return arrivals
}
