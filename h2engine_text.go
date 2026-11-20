package marasi

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// The stored text form of an HTTP/2 message, read back into a field list
// (docs/adr/2026-11-19-marasi-owns-its-http2-framing.md). It is the inverse
// of h2RequestText and h2ResponseText: text they wrote reads back as the
// list they were given, so a message shown and forwarded unchanged goes out
// as it came. Text a user typed is kept as typed wherever HPACK can carry
// it: names keep their case, and no field is checked against RFC 9113.

// errH2Text is the cause when text is not in the HTTP/2 text form.
var errH2Text = errors.New("not an HTTP/2 message in the text form")

// h2TextRequest is a request read from the text form.
type h2TextRequest struct {
	// fields is the header block, :method and :path included.
	fields h2Fields
	// authority is the authority of an absolute-form request line, which
	// names where the request goes; empty for an origin-form one.
	authority, scheme string
	body              []byte
}

// readH2RequestText reads a request in the text form: the request line
// METHOD target HTTP/2, field lines, a blank line and the body. like is the
// list the text was shown from, or nil: :method and :path go back to the
// positions they had in it, and first, in that order, when it is nil or
// has none. A field line equal to one of like's keeps its never-indexed
// flag. An empty method or target stands for a field the message does not
// have when like has none either. An absolute-form target sets :path to its
// path and query and :authority to its authority.
func readH2RequestText(raw []byte, like h2Fields) (h2TextRequest, error) {
	head, body := splitH2Text(raw)
	line, rest, _ := strings.Cut(head, "\n")
	line = strings.TrimSuffix(line, "\r")
	method, after, ok := strings.Cut(line, " ")
	i := strings.LastIndex(after, " ")
	if !ok || i < 0 || after[i+1:] != "HTTP/2" {
		return h2TextRequest{}, fmt.Errorf("%w: the request line %q is not METHOD target HTTP/2", errH2Text, line)
	}
	target := after[:i]
	fields, err := readH2FieldLines(rest)
	if err != nil {
		return h2TextRequest{}, err
	}
	req := h2TextRequest{body: body}
	path := target
	if !strings.HasPrefix(target, "/") && target != "*" && strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			return h2TextRequest{}, fmt.Errorf("%w: the request target %q", errH2Text, target)
		}
		req.scheme, req.authority, path = u.Scheme, u.Host, originForm(target)
		fields = fields.set(":authority", u.Host)
	}
	fields = restorePseudo(fields, like, h2Field{Name: ":method", Value: method}, h2Field{Name: ":path", Value: path})
	req.fields = keepSensitive(fields, like)
	return req, nil
}

// readH2ResponseText reads a response in the text form: HTTP/2 <status>,
// field lines, a blank line and the body. Text after the status, such as a
// reason phrase, is ignored. :status goes back to its position in like, as
// readH2RequestText puts back :method.
func readH2ResponseText(raw []byte, like h2Fields) (h2Fields, []byte, error) {
	head, body := splitH2Text(raw)
	line, rest, _ := strings.Cut(head, "\n")
	line = strings.TrimSuffix(line, "\r")
	version, after, _ := strings.Cut(line, " ")
	if version != "HTTP/2" {
		return nil, nil, fmt.Errorf("%w: the status line %q is not HTTP/2 <status>", errH2Text, line)
	}
	status, _, _ := strings.Cut(after, " ")
	fields, err := readH2FieldLines(rest)
	if err != nil {
		return nil, nil, err
	}
	fields = restorePseudo(fields, like, h2Field{Name: ":status", Value: status})
	return keepSensitive(fields, like), body, nil
}

// splitH2Text splits text at the first blank line, CRLF or LF, into the
// head and the body. Text with no blank line is all head.
func splitH2Text(raw []byte) (string, []byte) {
	end, sep := len(raw), 0
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		end, sep = i, 4
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 && i < end {
		end, sep = i, 2
	}
	return string(raw[:end]), bytes.Clone(raw[min(end+sep, len(raw)):])
}

// readH2FieldLines reads "name: value" lines. The name is everything before
// the first colon after its first character, so a pseudo-header field
// reads as itself; one space after the colon is the separator the text form
// writes, and the rest of the line is the value as typed.
func readH2FieldLines(head string) (h2Fields, error) {
	var fields h2Fields
	for line := range strings.SplitSeq(head, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		i := strings.Index(line[1:], ":") + 1
		if i <= 0 {
			return nil, fmt.Errorf("%w: the field line %q has no colon", errH2Text, line)
		}
		name, value := line[:i], line[i+1:]
		value = strings.TrimPrefix(value, " ")
		fields = append(fields, h2Field{Name: name, Value: value})
	}
	return fields, nil
}

// restorePseudo inserts each field the text form carries outside the field
// lines at the position the first field of its name had in like, or after
// the ones before it when like has none. An empty value is left out when
// like has no such field.
func restorePseudo(fields, like h2Fields, lines ...h2Field) h2Fields {
	out := slices.Clone(fields)
	next := 0
	for _, f := range lines {
		at := slices.IndexFunc(like, func(l h2Field) bool { return l.Name == f.Name })
		if at < 0 {
			if f.Value == "" && like != nil {
				continue
			}
			at = next
		}
		at = min(at, len(out))
		out = slices.Insert(out, at, f)
		next = at + 1
	}
	return out
}

// keepSensitive marks a field never-indexed when like has the same name and
// value marked, each of like's fields matching at most one.
func keepSensitive(fields, like h2Fields) h2Fields {
	used := make([]bool, len(like))
	for i, f := range fields {
		for j, l := range like {
			if !used[j] && l.Name == f.Name && l.Value == f.Value {
				used[j] = true
				fields[i].Sensitive = l.Sensitive
				break
			}
		}
	}
	return fields
}
