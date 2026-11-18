package h1

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// ErrInvalidField is returned by Merge for an edited name that is not a token
// or a value holding CR, LF or NUL, which would split one field into several
// on the wire.
var ErrInvalidField = errors.New("invalid header field")

// View returns the section as an http.Header. Keys are canonical, so Get and
// Set work as usual, and each field gives one value, in wire order. The map
// is a copy; Merge folds edits made to it back into the section.
func (h *Header) View() http.Header {
	v := make(http.Header, len(h.Fields))
	for _, f := range h.Fields {
		k := http.CanonicalHeaderKey(f.Name())
		v[k] = append(v[k], f.Value())
	}
	return v
}

// Merge folds an edited view back into the section, changing only what the
// edit names:
//
//   - a name whose values are unchanged keeps its lines byte for byte;
//   - a changed name keeps its first line's position and wire spelling, with
//     the new values on lines in that position and the name's other lines
//     removed;
//   - a name missing from v, or left with no values, loses all its lines;
//   - a new name is appended at the end, spelled exactly as its map key.
//
// Names are compared without regard to case, so keys in v that differ only
// in case are one name, their values taken in key order. Merge changes
// nothing and returns ErrInvalidField when an edited field cannot be written.
func (h *Header) Merge(v http.Header) error {
	if h.matches(v) {
		return nil
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	// Names are matched in canonical form, which folds case for any token;
	// a key that is not a token stays as written and fails as a new name.
	names := make([]string, len(h.Fields))
	original := make(map[string][]string, len(h.Fields))
	for i, f := range h.Fields {
		names[i] = canonicalName(f.name())
		original[names[i]] = append(original[names[i]], f.Value())
	}
	edited := make(map[string][]string, len(keys))
	var fresh []string // keys of names the section does not have
	for _, k := range keys {
		name := http.CanonicalHeaderKey(k)
		edited[name] = append(edited[name], v[k]...)
		if _, ok := original[name]; !ok {
			fresh = append(fresh, k)
		}
	}

	unchanged := len(fresh) == 0 && len(edited) == len(original)
	if unchanged {
		for name, values := range original {
			if !slices.Equal(values, edited[name]) {
				unchanged = false
				break
			}
		}
	}
	if unchanged {
		return nil
	}

	eol := h.eol()
	fields := make([]Field, 0, len(h.Fields)+len(fresh))
	done := make(map[string]bool)
	for i, f := range h.Fields {
		name := names[i]
		values, kept := edited[name]
		switch {
		case !kept || len(values) == 0:
		case slices.Equal(values, original[name]):
			fields = append(fields, f)
		case done[name]:
			// The name's new lines went in at its first line.
		default:
			done[name] = true
			lineEnd := string(eolOf(f.Raw))
			if lineEnd == "" {
				lineEnd = eol
			}
			for _, value := range values {
				nf, err := newField(f.Name(), value, lineEnd)
				if err != nil {
					return err
				}
				fields = append(fields, nf)
			}
		}
	}
	for _, k := range fresh {
		for _, value := range v[k] {
			nf, err := newField(k, value, eol)
			if err != nil {
				return err
			}
			fields = append(fields, nf)
		}
	}
	h.Fields = fields
	return nil
}

// matches reports, without allocating, whether v holds exactly the section's
// values, the common case of a view nobody edited. It answers false when it
// cannot tell cheaply, such as for a name not in canonical form on the wire,
// and Merge then compares in full.
func (h *Header) matches(v http.Header) bool {
	total := 0
	for _, values := range v {
		total += len(values)
	}
	if total != len(h.Fields) {
		return false
	}
	for i, f := range h.Fields {
		name := f.name()
		if !isCanonical(name) {
			return false
		}
		values := v[string(name)]
		occurrence := 0
		for _, earlier := range h.Fields[:i] {
			if bytes.Equal(earlier.name(), name) {
				occurrence++
			}
		}
		if occurrence >= len(values) || !f.hasValue(values[occurrence]) {
			return false
		}
	}
	return true
}

// hasValue reports whether the field's value is value, as Value would give
// it.
func (f Field) hasValue(value string) bool {
	_, rest, _ := bytes.Cut(f.Raw, []byte(":"))
	if line, next, _ := bytes.Cut(rest, []byte("\n")); len(next) == 0 {
		return string(trimOWS(trimEOL(line))) == value
	}
	return f.Value() == value
}

func isCanonical(name []byte) bool {
	upper := true
	for _, c := range name {
		if upper && 'a' <= c && c <= 'z' || !upper && 'A' <= c && c <= 'Z' {
			return false
		}
		upper = c == '-'
	}
	return true
}

// canonicalName is a wire field name in http.CanonicalHeaderKey form.
func canonicalName(name []byte) string {
	if isCanonical(name) {
		return string(name)
	}
	return http.CanonicalHeaderKey(string(name))
}

func newField(name, value, eol string) (Field, error) {
	if !isToken([]byte(name)) || !validValue([]byte(value)) {
		return Field{}, fmt.Errorf("writing field %q: %w", name, ErrInvalidField)
	}
	return Field{Raw: []byte(name + ": " + value + eol)}, nil
}

// Request returns the head as an http.Request. As with net/http, the Host
// field is lifted into r.Host and left out of r.Header; for an absolute-form
// target r.Host is the target's authority. The body is http.NoBody and
// ContentLength comes from the framing, -1 when it is not a length. The view
// is a copy; Merge folds edits made to it back into the head.
func (h *RequestHead) Request() (*http.Request, error) {
	u, err := parseTarget(h.Method, h.Target)
	if err != nil {
		return nil, err
	}
	major, minor, _ := http.ParseHTTPVersion(h.Proto)
	r := &http.Request{
		Method:        h.Method,
		URL:           u,
		Proto:         h.Proto,
		ProtoMajor:    major,
		ProtoMinor:    minor,
		Header:        h.Header.View(),
		Body:          http.NoBody,
		ContentLength: -1,
		Host:          h.host(u),
		RequestURI:    h.Target,
	}
	delete(r.Header, "Host")
	if f, err := RequestFraming(h); err == nil {
		switch f.Kind {
		case NoBody:
			r.ContentLength = 0
		case LengthFramed:
			r.ContentLength = f.Length
		}
	}
	return r, nil
}

// Merge folds an edited request view back into the head. The request line is
// rewritten only when the method, URL or protocol changed; a new URL keeps
// the target's form. The Host field follows r.Host, and a Host key in r.Header
// is ignored. Header edits follow Header.Merge.
func (h *RequestHead) Merge(r *http.Request) error {
	orig, err := parseTarget(h.Method, h.Target)
	if err != nil {
		return err
	}
	view := make(http.Header, len(r.Header)+1)
	for k, vs := range r.Header {
		if !strings.EqualFold(k, "Host") {
			view[k] = vs
		}
	}
	if r.Host == h.host(orig) {
		if hosts := h.Header.Values("Host"); len(hosts) > 0 {
			view["Host"] = hosts
		}
	} else if r.Host != "" {
		view["Host"] = []string{r.Host}
	}

	method, target, proto := h.Method, h.Target, h.Proto
	if r.URL != nil && r.URL.String() != orig.String() {
		target = formTarget(h.Method, h.Target, r.URL)
	}
	if r.Method != "" {
		method = r.Method
	}
	if r.Proto != "" {
		proto = r.Proto
	}
	var line []byte
	if method != h.Method || target != h.Target || proto != h.Proto {
		if !isToken([]byte(method)) || strings.ContainsAny(target, " \t\r\n\x00") || !validProto([]byte(proto)) {
			return fmt.Errorf("writing request line %q: %w", method+" "+target+" "+proto, ErrInvalidField)
		}
		line = []byte(method + " " + target + " " + proto + h.Header.eol())
	}
	if err := h.Header.Merge(view); err != nil {
		return err
	}
	if line != nil {
		h.Line, h.Method, h.Target, h.Proto = line, method, target, proto
	}
	return nil
}

// host is what the request view shows as r.Host.
func (h *RequestHead) host(u *url.URL) string {
	if u.Host != "" {
		return u.Host
	}
	return h.Header.Get("Host")
}

// parseTarget parses a request target in any of the forms RFC 9112 §3.2
// allows.
func parseTarget(method, target string) (*url.URL, error) {
	if method == http.MethodConnect && !strings.HasPrefix(target, "/") {
		return &url.URL{Host: target}, nil
	}
	if target == "*" {
		return &url.URL{Path: "*"}, nil
	}
	u, err := url.ParseRequestURI(target)
	if err != nil {
		return nil, fmt.Errorf("parsing request target %q: %w", target, err)
	}
	return u, nil
}

// formTarget writes u in the form the original target used.
func formTarget(method, orig string, u *url.URL) string {
	switch {
	case method == http.MethodConnect && !strings.HasPrefix(orig, "/"):
		return u.Host
	case strings.HasPrefix(orig, "/"), orig == "*":
		return u.RequestURI()
	default:
		return u.String()
	}
}

// Response returns the head as an http.Response. Status is the status line
// after the protocol, as net/http gives it. ContentLength is the
// Content-Length when the response has one, -1 otherwise. The view is a copy;
// Merge folds edits made to it back into the head.
func (h *ResponseHead) Response() *http.Response {
	major, minor, _ := http.ParseHTTPVersion(h.Proto)
	r := &http.Response{
		Status:        h.status(),
		StatusCode:    h.StatusCode,
		Proto:         h.Proto,
		ProtoMajor:    major,
		ProtoMinor:    minor,
		Header:        h.Header.View(),
		Body:          http.NoBody,
		ContentLength: -1,
	}
	if f, err := framing(&h.Header, false); err == nil && f.Kind == LengthFramed {
		r.ContentLength = f.Length
	}
	return r
}

// Merge folds an edited response view back into the head. The status line is
// rewritten only when the status, status code or protocol changed. A new
// reason comes from r.Status when it changed, and otherwise is the standard
// one for the new code. Header edits follow Header.Merge.
func (h *ResponseHead) Merge(r *http.Response) error {
	code, reason, proto := h.StatusCode, h.Reason, h.Proto
	if r.Proto != "" {
		proto = r.Proto
	}
	switch {
	case r.Status != h.status():
		code = r.StatusCode
		prefix := strconv.Itoa(code)
		reason = strings.TrimPrefix(strings.TrimPrefix(r.Status, prefix), " ")
	case r.StatusCode != h.StatusCode:
		code = r.StatusCode
		reason = http.StatusText(code)
	}
	var line []byte
	if code != h.StatusCode || reason != h.Reason || proto != h.Proto {
		if code < 100 || code > 999 || !validValue([]byte(reason)) || !validProto([]byte(proto)) {
			return fmt.Errorf("writing status line %q: %w", proto+" "+strconv.Itoa(code)+" "+reason, ErrInvalidField)
		}
		line = []byte(proto + " " + strconv.Itoa(code) + " " + reason + h.Header.eol())
	}
	if err := h.Header.Merge(r.Header); err != nil {
		return err
	}
	if line != nil {
		h.Line, h.StatusCode, h.Reason, h.Proto = line, code, reason, proto
	}
	return nil
}

// status is the status line after the protocol and its space.
func (h *ResponseHead) status() string {
	_, rest, _ := strings.Cut(string(trimEOL(h.Line)), " ")
	return rest
}
