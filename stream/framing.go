// Package stream cuts a streamed pair's body into stream messages
// (GLOSSARY: Streamed pair, Stream message, Message framing). It knows
// nothing of HTTP versions: an engine reads a direction's head, asks
// FramingOf whether it names a message framing, and feeds that direction's
// body to a Splitter, which hands back each stream message whole as the
// bytes it came in.
//
// See docs/adr/2026-11-20-streamed-pairs-are-recognised-from-the-head-and-other-pairs-stay-buffered.md.
package stream

import (
	"mime"
	"strings"
)

// Framing is a message framing: how a direction's body is cut into stream
// messages. FramingNone is an ordinary body.
type Framing string

// Message framings.
const (
	FramingNone    Framing = ""
	FramingGRPC    Framing = "grpc"
	FramingGRPCWeb Framing = "grpc-web"
	FramingConnect Framing = "connect"
	FramingSSE     Framing = "sse"
	FramingNDJSON  Framing = "ndjson"
)

// DefaultMaxMessageSize is the per-message limit when none is set: the
// WebSocket message limit.
const DefaultMaxMessageSize = 64 * 1024 * 1024

// FramingOf is the message framing a direction's head names by its content
// type, FramingNone when it names none. response is true for a response
// head: server-sent events and NDJSON are streamed only in responses. Only
// the content type decides; the path and the body never do.
func FramingOf(contentType string, response bool) Framing {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// A type with bad parameters still names its framing.
		mediaType, _, _ = strings.Cut(contentType, ";")
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch {
	case suffixed(mediaType, "application/grpc-web"), suffixed(mediaType, "application/grpc-web-text"):
		return FramingGRPCWeb
	case suffixed(mediaType, "application/grpc"):
		return FramingGRPC
	case strings.HasPrefix(mediaType, "application/connect+"):
		return FramingConnect
	case !response:
		return FramingNone
	case mediaType == "text/event-stream":
		return FramingSSE
	case mediaType == "application/x-ndjson", mediaType == "application/jsonl":
		return FramingNDJSON
	}
	return FramingNone
}

// Enveloped reports whether framing cuts its messages into 5-byte envelopes,
// whose flags byte each message has.
func (f Framing) Enveloped() bool {
	return f == FramingGRPC || f == FramingGRPCWeb || f == FramingConnect
}

// IsGRPCWebText reports whether contentType is gRPC-Web in base64
// (application/grpc-web-text), whose envelopes are cut only once decoded.
func IsGRPCWebText(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return suffixed(strings.ToLower(strings.TrimSpace(mediaType)), "application/grpc-web-text")
}

// suffixed reports whether mediaType is base or base+codec.
func suffixed(mediaType, base string) bool {
	rest, ok := strings.CutPrefix(mediaType, base)
	return ok && (rest == "" || rest[0] == '+')
}
