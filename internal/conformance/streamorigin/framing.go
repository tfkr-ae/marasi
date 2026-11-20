package streamorigin

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// Message framings the splitter knows, named as in GLOSSARY.md.
const (
	framingNone = ""
	framingGRPC = "grpc"
	framingSSE  = "sse"
)

// framingOf is the message framing a direction's content type names: the
// recording cuts that direction's body into stream messages by it.
func framingOf(contentType string) string {
	mediaType, _, _ := strings.Cut(strings.ToLower(contentType), ";")
	mediaType = strings.TrimSpace(mediaType)
	switch {
	case mediaType == "application/grpc" || strings.HasPrefix(mediaType, "application/grpc+"):
		return framingGRPC
	case mediaType == "text/event-stream":
		return framingSSE
	}
	return framingNone
}

// splitter cuts one direction's body into stream messages as its bytes
// arrive, however they were split on the wire.
type splitter struct {
	framing string
	held    []byte
}

// write takes the next bytes of the body and returns every message they
// complete.
func (s *splitter) write(data []byte) [][]byte {
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
	case framingGRPC:
		// A 5-byte envelope: a flags byte and a big-endian length
		// (gRPC PROTOCOL-HTTP2.md, Length-Prefixed-Message).
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
	}
	return 0
}

// end returns the arrival of what is still held when the body ends: the
// whole body with no framing, or a message cut short.
func (s *splitter) end(side string) (Arrival, bool) {
	if len(s.held) == 0 {
		return Arrival{}, false
	}
	kind := KindPartial
	if s.framing == framingNone {
		kind = KindBody
	}
	a := Arrival{Side: side, Kind: kind, Data: s.held}
	s.held = nil
	return a, true
}
