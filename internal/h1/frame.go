package h1

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
)

// FrameRequestBody gives the body bytes that carry payload as the body of
// the request head, which modifiers may have edited, and makes the head's
// framing fields match them. original is the body as it was read, nil for
// none. See frameBody for the rules.
func FrameRequestBody(head *RequestHead, original *Body, payload []byte) []byte {
	return frameBody(&head.Header, true, false, original, payload)
}

// FrameResponseBody is FrameRequestBody for a response to a request with
// method. A response that has no body by its status or method gets none.
func FrameResponseBody(head *ResponseHead, method string, original *Body, payload []byte) []byte {
	bodyless := head.StatusCode < 200 || head.StatusCode == http.StatusNoContent ||
		head.StatusCode == http.StatusNotModified || method == http.MethodHead ||
		method == http.MethodConnect && head.StatusCode < 300
	return frameBody(&head.Header, false, bodyless, original, payload)
}

// frameBody frames payload from its final bytes (RFC 9112 §6):
//
//   - a Transfer-Encoding always wins over Content-Length, whose lines are
//     removed, as an intermediary must before forwarding (RFC 9112 §6.3);
//   - a body nobody changed, still framed the way it was read, goes out
//     exactly as it was read;
//   - otherwise a chunked body is re-chunked as one chunk, keeping the
//     original's trailers; a Content-Length value is rewritten in place;
//     and a message with neither that now has a body gets Content-Length
//     appended at the end. A transfer coding other than chunked runs until
//     the connection closes, so its payload goes out as it is.
func frameBody(h *Header, request, bodyless bool, original *Body, payload []byte) []byte {
	if original == nil {
		original = &Body{}
	}
	coded := h.Has("Transfer-Encoding")
	if coded && h.Has("Content-Length") {
		h.remove("Content-Length")
	}
	if bodyless {
		return nil
	}
	f, err := framing(h, request)
	if request && f.Kind == UntilClose {
		f.Kind = NoBody
	}
	if err == nil && f == original.Framing && bytes.Equal(payload, original.Payload()) {
		var body bytes.Buffer
		original.WriteTo(&body)
		return body.Bytes()
	}
	switch {
	case coded && f.Kind == Chunked:
		return chunk(payload, original)
	case coded:
		return payload
	case h.Has("Content-Length"), len(payload) > 0:
		v := h.View()
		v["Content-Length"] = []string{strconv.Itoa(len(payload))}
		// The value is digits, so the merge cannot fail.
		h.Merge(v)
		return payload
	default:
		return nil
	}
}

// chunk codes payload as one chunk followed by the last chunk and the
// original's trailer section, or an empty one.
func chunk(payload []byte, original *Body) []byte {
	var body bytes.Buffer
	if len(payload) > 0 {
		body.WriteString(strconv.FormatInt(int64(len(payload)), 16) + "\r\n")
		body.Write(payload)
		body.WriteString("\r\n")
	}
	body.WriteString("0\r\n")
	if original.Framing.Kind == Chunked && original.Trailer.End != nil {
		original.Trailer.WriteTo(&body)
	} else {
		body.WriteString("\r\n")
	}
	return body.Bytes()
}

// remove removes every field named name.
func (h *Header) remove(name string) {
	fields := h.Fields[:0:0]
	for _, f := range h.Fields {
		if !strings.EqualFold(f.Name(), name) {
			fields = append(fields, f)
		}
	}
	h.Fields = fields
}
