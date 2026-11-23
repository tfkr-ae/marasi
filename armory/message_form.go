package armory

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/protoscope"
	"github.com/tfkr-ae/marasi/rawhttp"
	"github.com/tfkr-ae/marasi/stream"
)

// Message-form body separators (GLOSSARY: Message-form body). Each is a whole line.
const (
	messageSeparator           = "--- message ---"
	compressedMessageSeparator = "--- message compressed ---"
)

// formMessage is one message of a rendered message-form body.
type formMessage struct {
	// text is the message's text form.
	text string
	// line is the 1-based line of the request the text starts on.
	line int
	// compressed reports that the message sits under a
	// "--- message compressed ---" separator and is sent compressed.
	compressed bool
}

// supportedFraming reports whether Marasi can send a template that names framing.
func supportedFraming(framing string) error {
	switch stream.Framing(framing) {
	case stream.FramingNone, stream.FramingGRPC, stream.FramingGRPCWeb, stream.FramingConnect:
		return nil
	}
	return fmt.Errorf("armory template framing %q is not supported", framing)
}

// splitHead cuts a rendered request into its head, the blank line ending it
// and its body, as splitH2Text and the HTTP/1.1 reader do.
func splitHead(raw string) (head, blank, body string) {
	crlf := strings.Index(raw, "\r\n\r\n")
	lf := strings.Index(raw, "\n\n")
	switch {
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return raw[:crlf], "\r\n\r\n", raw[crlf+4:]
	case lf >= 0:
		return raw[:lf], "\n\n", raw[lf+2:]
	}
	return raw, "", ""
}

// headField is the value of the first field named name in head, read in
// either the HTTP/1.1 form or the HTTP/2 text form. Pseudo-header fields
// are skipped.
func headField(head, name string) (string, bool) {
	lines := strings.Split(head, "\n")
	for _, line := range lines[1:] {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// splitMessages cuts a message-form body into its messages. firstLine is
// the request line the body starts on. The body must start with a separator.
func splitMessages(body string, firstLine int) ([]formMessage, error) {
	var messages []formMessage
	var current *formMessage
	var text strings.Builder
	line := firstLine
	for rest := body; rest != ""; line++ {
		content, after, newline := strings.Cut(rest, "\n")
		whole := content
		if newline {
			whole += "\n"
		}
		rest = after
		if separator := strings.TrimSuffix(content, "\r"); separator == messageSeparator || separator == compressedMessageSeparator {
			if current != nil {
				current.text = text.String()
				messages = append(messages, *current)
			}
			current = &formMessage{line: line + 1, compressed: separator == compressedMessageSeparator}
			text.Reset()
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("line %d: a message-form body must start with a %q line", line, messageSeparator)
		}
		text.WriteString(whole)
	}
	if current == nil {
		return nil, fmt.Errorf("line %d: a message-form body must start with a %q line", firstLine, messageSeparator)
	}
	current.text = text.String()
	return append(messages, *current), nil
}

// connectUnaryCodec is the codec a Connect unary content type names,
// application/proto or application/json, and false for any other type.
// A Connect unary request's body is its one message, with no envelope.
func connectUnaryCodec(contentType string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType, _, _ = strings.Cut(contentType, ";")
	}
	switch mediaType = strings.ToLower(strings.TrimSpace(mediaType)); mediaType {
	case "application/proto", "application/json":
		return strings.TrimPrefix(mediaType, "application/"), true
	}
	return "", false
}

// messageContentType is the content type a message of a request whose head
// has contentType is read and written with by stream.TextForm and
// stream.FromText: a Connect unary type is read as its Connect streaming
// type, which names the same codec.
func messageContentType(contentType string) string {
	if codec, ok := connectUnaryCodec(contentType); ok {
		return "application/connect+" + codec
	}
	return contentType
}

// namesFraming reports whether contentType names framing, a Connect unary
// type naming connect.
func namesFraming(contentType, framing string) bool {
	if _, unary := connectUnaryCodec(contentType); unary {
		return stream.Framing(framing) == stream.FramingConnect
	}
	return stream.FramingOf(contentType, false) == stream.Framing(framing)
}

// encodeMessageForm turns a rendered request whose body is in message form
// into the request to send: the head as rendered and each message encoded
// for the head's content type, in a new envelope, the whole body in base64
// for gRPC-Web text, or a Connect unary request's one message as the body.
// Every length is computed here, so payloads of any length give
// well-formed messages, and an HTTP/1.1 head's Content-Length is set to
// the body's length.
func encodeMessageForm(raw, framing string) (string, error) {
	head, blank, body := splitHead(raw)
	if blank == "" {
		return "", errors.New("a message-form request needs a blank line after its head")
	}
	contentType, _ := headField(head, "content-type")
	if !namesFraming(contentType, framing) {
		return "", fmt.Errorf("the content type %q does not name the template's framing %s", contentType, framing)
	}
	bodyLine := strings.Count(head, "\n") + strings.Count(blank, "\n") + 1
	messages, err := splitMessages(body, bodyLine)
	if err != nil {
		return "", err
	}
	_, unary := connectUnaryCodec(contentType)
	if unary && len(messages) != 1 {
		return "", fmt.Errorf("a Connect unary body must hold exactly one message, not %d", len(messages))
	}
	textType := messageContentType(contentType)
	protoscopeText := stream.HasProtoscopeText(textType)
	var encoded []byte
	for i, message := range messages {
		text := message.text
		if !protoscopeText {
			// The line ending before the next separator, or ending the
			// body, is the separator's, not the JSON text's.
			text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
		}
		payload, err := stream.FromText(textType, text)
		if errors.Is(err, protoscope.ErrSyntax) {
			// Encoding again behind the lines before it numbers the error's
			// line in the whole request; whitespace encodes to nothing.
			_, err = stream.FromText(textType, strings.Repeat("\n", message.line-1)+text)
		}
		if err != nil {
			return "", fmt.Errorf("message %d: %w", i+1, err)
		}
		if unary {
			if message.compressed {
				return "", fmt.Errorf("message %d (line %d): a Connect unary message is compressed by the request's Content-Encoding, not under a compressed separator", i+1, message.line-1)
			}
			encoded = payload
			break
		}
		var flags byte
		if message.compressed {
			if payload, err = compressMessage(head, framing, payload); err != nil {
				return "", fmt.Errorf("message %d (line %d): %w", i+1, message.line-1, err)
			}
			flags = stream.CompressedFlags(flags)
		}
		encoded = append(encoded, stream.Envelope(flags, payload)...)
	}
	if stream.IsGRPCWebText(contentType) {
		encoded = []byte(base64.StdEncoding.EncodeToString(encoded))
	}
	return withContentLength(head, len(encoded)) + blank + string(encoded), nil
}

// withContentLength is an HTTP/1.1 head with every Content-Length value set
// to n, and one added when it has none and n is not 0. A head that uses
// Transfer-Encoding is returned unchanged. An HTTP/2 head carries no length,
// so it loses any content-length field it was captured with: the h2 engine
// sends fields as written, and a captured length would not fit a payload of
// another length.
func withContentLength(head string, n int) string {
	if rawhttp.IsHTTP2Text(head) {
		return withoutContentLength(head)
	}
	if _, chunked := headField(head, "transfer-encoding"); chunked {
		return head
	}
	requestLine, _, _ := strings.Cut(head, "\n")
	lines := strings.Split(head, "\n")
	found := false
	for i, line := range lines[1:] {
		name, ok := contentLengthLine(line)
		if !ok {
			continue
		}
		found = true
		eol := ""
		if strings.HasSuffix(line, "\r") {
			eol = "\r"
		}
		lines[i+1] = name + ": " + strconv.Itoa(n) + eol
	}
	if !found && n > 0 {
		eol := ""
		if strings.HasSuffix(requestLine, "\r") {
			eol = "\r"
		}
		lines = append(lines, "Content-Length: "+strconv.Itoa(n)+eol)
	}
	return strings.Join(lines, "\n")
}

// withoutContentLength is head with every Content-Length field line removed.
func withoutContentLength(head string) string {
	lines := strings.Split(head, "\n")
	kept := lines[:1]
	for _, line := range lines[1:] {
		if _, ok := contentLengthLine(line); !ok {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// contentLengthLine reports whether line is a Content-Length field line,
// and the field name as written.
func contentLengthLine(line string) (string, bool) {
	name, _, ok := strings.Cut(line, ":")
	return name, ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length")
}

// compressMessage compresses payload, a message under a compressed
// separator, with the per-message encoding head names for framing:
// grpc-encoding, or connect-content-encoding for Connect streaming. A head
// that names no encoding, identity, or one Marasi does not encode is an
// error.
func compressMessage(head, framing string, payload []byte) ([]byte, error) {
	field := stream.EncodingHeader(stream.Framing(framing))
	encoding, _ := headField(head, field)
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return nil, fmt.Errorf("a compressed message needs a %s the request names, and it names none", field)
	}
	if !stream.Decodable(encoding) {
		return nil, fmt.Errorf("a compressed message cannot be compressed with the %s %q: Marasi compresses gzip and deflate", field, encoding)
	}
	return stream.Compress(encoding, payload)
}

// TemplateMessage is one message MessageFormTemplate puts in a
// message-form body.
type TemplateMessage struct {
	// Payload is the message's uncompressed payload.
	Payload []byte
	// Compressed reports that the message was sent compressed, so it goes
	// under a "--- message compressed ---" separator and is compressed
	// again when the template renders.
	Compressed bool
}

// MessageFormTemplate is an Armory template that sends head followed by
// messages: a message-form body with each message's payload in its text
// form for the head's content type under a separator, in order, escaped so
// the template renders back to head and the payloads' bytes. A compressed
// message goes under a compressed separator. It reports false when the
// content type gives the messages no text form. An HTTP/2 head loses its
// content-length field, which the render would drop (withContentLength).
func MessageFormTemplate(head string, messages []TemplateMessage) (string, bool) {
	if rawhttp.IsHTTP2Text(head) {
		head = withoutContentLength(head)
	}
	headText, _, _ := splitHead(head)
	contentType, _ := headField(headText, "content-type")
	contentType = messageContentType(contentType)
	protoscopeText := stream.HasProtoscopeText(contentType)
	var body strings.Builder
	for _, message := range messages {
		text, ok := stream.TextForm(contentType, message.Payload)
		if !ok {
			return "", false
		}
		if message.Compressed {
			body.WriteString(compressedMessageSeparator + "\n")
		} else {
			body.WriteString(messageSeparator + "\n")
		}
		body.WriteString(text)
		// A JSON message's text always gets the line ending encoding
		// takes off again, so the message renders byte for byte.
		if text != "" && (!protoscopeText || !strings.HasSuffix(text, "\n")) {
			body.WriteString("\n")
		}
	}
	return EscapeTemplate(head + body.String()), true
}

// ConnectUnaryTemplate is an Armory template that sends the captured
// Connect unary request raw: its head and its body as one message under a
// separator (MessageFormTemplate), for the framing connect. It reports
// false for any other request. A request is Connect unary when its content
// type is application/proto, or application/json with a
// Connect-Protocol-Version field, which an ordinary JSON API does not
// send. A body with a Content-Encoding or Transfer-Encoding is not read.
func ConnectUnaryTemplate(raw string) (string, bool) {
	head, blank, body := splitHead(raw)
	if blank == "" {
		return "", false
	}
	contentType, _ := headField(head, "content-type")
	codec, unary := connectUnaryCodec(contentType)
	if !unary {
		return "", false
	}
	if _, versioned := headField(head, "connect-protocol-version"); codec == "json" && !versioned {
		return "", false
	}
	for _, field := range []string{"content-encoding", "transfer-encoding"} {
		if value, ok := headField(head, field); ok && !strings.EqualFold(value, "identity") {
			return "", false
		}
	}
	return MessageFormTemplate(head+blank, []TemplateMessage{{Payload: []byte(body)}})
}
