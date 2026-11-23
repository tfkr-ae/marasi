package armory

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tfkr-ae/marasi/protoscope"
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
	case stream.FramingNone, stream.FramingGRPC, stream.FramingConnect:
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

// encodeMessageForm turns a rendered request whose body is in message form
// into the request to send: the head as rendered and each message encoded
// for the head's content type, in a new envelope. Every length is computed
// here, so payloads of any length give well-formed messages.
func encodeMessageForm(raw, framing string) (string, error) {
	head, blank, body := splitHead(raw)
	if blank == "" {
		return "", errors.New("a message-form request needs a blank line after its head")
	}
	contentType, _ := headField(head, "content-type")
	if stream.FramingOf(contentType, false) != stream.Framing(framing) {
		return "", fmt.Errorf("the content type %q does not name the template's framing %s", contentType, framing)
	}
	bodyLine := strings.Count(head, "\n") + strings.Count(blank, "\n") + 1
	messages, err := splitMessages(body, bodyLine)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString(head)
	out.WriteString(blank)
	for i, message := range messages {
		payload, err := stream.FromText(contentType, message.text)
		if errors.Is(err, protoscope.ErrSyntax) {
			// Encoding again behind the lines before it numbers the error's
			// line in the whole request; whitespace encodes to nothing.
			_, err = stream.FromText(contentType, strings.Repeat("\n", message.line-1)+message.text)
		}
		if err != nil {
			return "", fmt.Errorf("message %d: %w", i+1, err)
		}
		var flags byte
		if message.compressed {
			if payload, err = compressMessage(head, framing, payload); err != nil {
				return "", fmt.Errorf("message %d (line %d): %w", i+1, message.line-1, err)
			}
			flags = stream.CompressedFlags(flags)
		}
		out.Write(stream.Envelope(flags, payload))
	}
	return out.String(), nil
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
// content type gives the messages no text form.
func MessageFormTemplate(head string, messages []TemplateMessage) (string, bool) {
	headText, _, _ := splitHead(head)
	contentType, _ := headField(headText, "content-type")
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
		if text != "" && !strings.HasSuffix(text, "\n") {
			body.WriteString("\n")
		}
	}
	return EscapeTemplate(head + body.String()), true
}
