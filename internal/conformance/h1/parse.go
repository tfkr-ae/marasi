package h1

import (
	"bytes"
	"strconv"
	"strings"
)

// ParseRequests parses the requests at the start of raw and returns them
// with the bytes after the last complete one. With eof set the bytes are all
// there will be, so a message cut short is returned as Truncated.
func ParseRequests(raw []byte, eof bool) ([]Message, []byte) {
	var messages []Message
	for len(raw) > 0 {
		message, n, ok := parseMessage(raw, false, func(Message) bool { return false }, eof)
		if !ok {
			break
		}
		messages = append(messages, message)
		raw = raw[n:]
		if message.Truncated {
			break
		}
	}
	return messages, raw
}

// ParseResponses parses the responses at the start of raw. methods are the
// methods of the requests they answer, in order; a HEAD response has no body.
// Interim (1xx) responses answer no request of their own.
func ParseResponses(raw []byte, methods []string, eof bool) ([]Message, []byte) {
	var messages []Message
	answered := 0
	for len(raw) > 0 {
		method := ""
		if answered < len(methods) {
			method = methods[answered]
		}
		message, n, ok := parseMessage(raw, true, func(m Message) bool {
			status := m.Status()
			return method == "HEAD" || status/100 == 1 || status == 204 || status == 304
		}, eof)
		if !ok {
			break
		}
		messages = append(messages, message)
		raw = raw[n:]
		if message.Truncated {
			break
		}
		if message.Status()/100 != 1 {
			answered++
		}
	}
	return messages, raw
}

// FinalResponses counts the responses that are not interim.
func FinalResponses(responses []Message) int {
	n := 0
	for _, response := range responses {
		if response.Status()/100 != 1 {
			n++
		}
	}
	return n
}

// parseMessage parses one message at the start of raw and returns it with
// the number of bytes it took. noBody says whether a message has no body
// whatever its fields say, such as a response to HEAD. ok is false when raw
// holds no complete message and eof is not set.
func parseMessage(raw []byte, response bool, noBody func(Message) bool, eof bool) (Message, int, bool) {
	var message Message
	head, n, complete := readHead(raw)
	if !complete {
		if !eof || n == 0 {
			return message, 0, false
		}
	}
	if len(head) > 0 {
		message.StartLine = head[0]
	}
	message.Fields = parseFields(head[min(1, len(head)):])
	if !complete {
		message.Truncated = true
		return message, len(raw), true
	}
	message.Framing.Kind = framing(message, response, noBody(message))
	body := raw[n:]
	switch message.Framing.Kind {
	case FramingNone:
		message.Body = nil
	case FramingContentLength:
		length, _ := contentLength(message)
		if int64(len(body)) < length {
			if !eof {
				return message, 0, false
			}
			message.Body, message.Truncated = body, true
			return message, len(raw), true
		}
		message.Body = body[:length]
		n += int(length)
	case FramingChunked:
		taken, ok := parseChunked(&message, body, eof)
		if !ok {
			return message, 0, false
		}
		n += taken
	case FramingClose:
		if !eof {
			return message, 0, false
		}
		message.Body = body
		n = len(raw)
	}
	return message, n, true
}

// readHead returns the lines of the start line and field section and the
// bytes they took, up to and including the empty line. A bare LF ends a line
// as well as CRLF. complete is false when no empty line came yet.
func readHead(raw []byte) (lines []string, n int, complete bool) {
	for {
		i := bytes.IndexByte(raw[n:], '\n')
		if i < 0 {
			if n < len(raw) {
				lines = append(lines, string(raw[n:]))
			}
			return lines, len(raw), false
		}
		line := strings.TrimSuffix(string(raw[n:n+i]), "\r")
		n += i + 1
		if line == "" {
			if len(lines) == 0 {
				// An empty line before a start line is skipped (RFC 9112 §2.2).
				continue
			}
			return lines, n, true
		}
		lines = append(lines, line)
	}
}

func parseFields(lines []string) []Field {
	fields := make([]Field, 0, len(lines))
	for _, line := range lines {
		name, value, _ := strings.Cut(line, ":")
		fields = append(fields, Field{Name: name, Value: value})
	}
	return fields
}

// framing picks a message's framing as RFC 9112 §6.3 does.
func framing(message Message, response, noBody bool) FramingKind {
	if noBody {
		return FramingNone
	}
	if codings := message.Values("Transfer-Encoding"); len(codings) > 0 {
		all := strings.Split(strings.Join(codings, ","), ",")
		if strings.EqualFold(strings.TrimSpace(all[len(all)-1]), "chunked") {
			return FramingChunked
		}
		if response {
			return FramingClose
		}
		// A request whose last coding is not chunked is invalid; the bytes
		// after its head are leftover.
		return FramingNone
	}
	if _, ok := contentLength(message); ok {
		return FramingContentLength
	}
	if response {
		return FramingClose
	}
	return FramingNone
}

// contentLength reads Content-Length; ok is false when it is missing or invalid.
func contentLength(message Message) (int64, bool) {
	values := message.Values("Content-Length")
	if len(values) == 0 {
		return 0, false
	}
	var length int64 = -1
	for _, value := range strings.Split(strings.Join(values, ","), ",") {
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || n < 0 || (length >= 0 && n != length) {
			return 0, false
		}
		length = n
	}
	return length, true
}

// parseChunked de-frames a chunked body into message and returns the bytes
// it took. ok is false when the body is not complete and eof is not set.
func parseChunked(message *Message, raw []byte, eof bool) (int, bool) {
	body := []byte{}
	n := 0
	truncated := func() (int, bool) {
		if !eof {
			return 0, false
		}
		message.Body, message.Truncated = body, true
		return len(raw), true
	}
	for {
		i := bytes.IndexByte(raw[n:], '\n')
		if i < 0 {
			return truncated()
		}
		line := strings.TrimSuffix(string(raw[n:n+i]), "\r")
		sizeText, _, _ := strings.Cut(line, ";")
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 16, 64)
		if err != nil || size < 0 {
			// An invalid chunk size ends the message; the rest is leftover.
			message.Body, message.Truncated = body, true
			return n, true
		}
		n += i + 1
		if size == 0 {
			break
		}
		if int64(len(raw)-n) < size {
			body = append(body, raw[n:]...)
			return truncated()
		}
		body = append(body, raw[n:n+int(size)]...)
		message.Framing.Chunks = append(message.Framing.Chunks, int(size))
		n += int(size)
		switch {
		case bytes.HasPrefix(raw[n:], []byte("\r\n")):
			n += 2
		case bytes.HasPrefix(raw[n:], []byte("\n")):
			n++
		case len(raw)-n < 2:
			return truncated()
		}
	}
	message.Body = body
	lines, taken, complete := readTrailers(raw[n:])
	if !complete {
		return truncated()
	}
	message.Trailers = parseFields(lines)
	return n + taken, true
}

// readTrailers reads the trailer section up to and including its empty line.
func readTrailers(raw []byte) ([]string, int, bool) {
	var lines []string
	n := 0
	for {
		i := bytes.IndexByte(raw[n:], '\n')
		if i < 0 {
			return lines, len(raw), false
		}
		line := strings.TrimSuffix(string(raw[n:n+i]), "\r")
		n += i + 1
		if line == "" {
			return lines, n, true
		}
		lines = append(lines, line)
	}
}
