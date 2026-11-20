package marasi

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// An HTTP/2 message's ordered field list, pseudo-header fields included, is
// its source of truth (docs/adr/2026-11-19-marasi-owns-its-http2-framing.md).
// The http.Request and http.Response a modifier edits are views of it, and
// merge folds the edits back by the HTTP/1.1 rules, so what goes out and what
// is stored is the list as it came in with only the edits changed.

// h2Field is one header field as it was on the wire.
type h2Field struct {
	Name, Value string
	// Sensitive is HPACK's never-indexed flag, kept so a field the peer
	// marked goes on marked.
	Sensitive bool
}

// pseudo reports whether the field is a pseudo-header field.
func (f h2Field) pseudo() bool { return strings.HasPrefix(f.Name, ":") }

// h2Fields is a header block's fields in wire order.
type h2Fields []h2Field

// get returns the first value of name, compared exactly.
func (fields h2Fields) get(name string) (string, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// set gives the first field named name the value, keeping its position, or
// appends the field after the last pseudo-header field when there is none.
func (fields h2Fields) set(name, value string) h2Fields {
	for i, f := range fields {
		if f.Name == name {
			out := slices.Clone(fields)
			out[i].Value = value
			return out
		}
	}
	at := 0
	for i, f := range fields {
		if f.pseudo() {
			at = i + 1
		}
	}
	return slices.Insert(slices.Clone(fields), at, h2Field{Name: name, Value: value})
}

// view returns the regular fields as an http.Header with canonical keys, each
// field one value in wire order. The pseudo-header fields are not in it.
func (fields h2Fields) view() http.Header {
	v := make(http.Header, len(fields))
	for _, f := range fields {
		if !f.pseudo() {
			k := http.CanonicalHeaderKey(f.Name)
			v[k] = append(v[k], f.Value)
		}
	}
	return v
}

// merge folds an edited view of the regular fields back into the list by the
// rules of h1.Header.Merge: a name whose values are unchanged keeps its
// fields as they were; a changed name keeps its first field's position and
// spelling, with the new values there and its other fields removed; a name
// missing from v loses its fields; a new name is appended at the end. HTTP/2
// names are lowercase on the wire, so a new name is written in lowercase
// rather than in the view's canonical case. Pseudo-header fields are kept.
func (fields h2Fields) merge(v http.Header) h2Fields {
	original := make(map[string][]string)
	for _, f := range fields {
		if !f.pseudo() {
			k := http.CanonicalHeaderKey(f.Name)
			original[k] = append(original[k], f.Value)
		}
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	edited := make(map[string][]string, len(v))
	var order []string
	for _, k := range keys {
		c := http.CanonicalHeaderKey(k)
		if _, ok := edited[c]; !ok {
			order = append(order, c)
		}
		edited[c] = append(edited[c], v[k]...)
	}
	same := len(edited) == len(original)
	for k, values := range original {
		same = same && slices.Equal(values, edited[k])
	}
	if same {
		return fields
	}
	out := make(h2Fields, 0, len(fields))
	written := make(map[string]bool)
	for _, f := range fields {
		if f.pseudo() {
			out = append(out, f)
			continue
		}
		k := http.CanonicalHeaderKey(f.Name)
		values := edited[k]
		switch {
		case len(values) == 0:
		case slices.Equal(values, original[k]):
			out = append(out, f)
		case !written[k]:
			written[k] = true
			for _, value := range values {
				out = append(out, h2Field{Name: f.Name, Value: value})
			}
		}
	}
	for _, k := range order {
		if _, had := original[k]; had {
			continue
		}
		for _, value := range edited[k] {
			out = append(out, h2Field{Name: strings.ToLower(k), Value: value})
		}
	}
	return out
}

// h2Request is an HTTP/2 request as the engine read it from the client: its
// header block, body and trailers.
type h2Request struct {
	fields   h2Fields
	body     []byte
	trailers h2Fields
	// endsOnData is set when END_STREAM came on a DATA frame, so an empty
	// body still goes out as one.
	endsOnData bool
	// streamed is the body's source when the head named a message
	// framing: body stays empty and the body is carried as stream
	// messages (h2engine_streamed.go).
	streamed *h2StreamedBody
}

// h2Response is an HTTP/2 response as the engine read it from the origin.
type h2Response struct {
	fields     h2Fields
	body       []byte
	trailers   h2Fields
	endsOnData bool
	// streamed is as on h2Request.
	streamed *h2StreamedBody
}

// target is what the request's view shows as its URL and Host: the URL from
// :scheme, :authority and :path, and the Host from :authority, or from a
// host field when there is no :authority.
func (m *h2Request) target() (*url.URL, string) {
	path, _ := m.fields.get(":path")
	scheme, _ := m.fields.get(":scheme")
	host, ok := m.fields.get(":authority")
	if !ok {
		host = http.Header(m.fields.view()).Get("Host")
	}
	method, _ := m.fields.get(":method")
	var u *url.URL
	switch {
	case method == http.MethodConnect && path == "":
		u = &url.URL{Host: host}
	case path == "*":
		u = &url.URL{Path: "*"}
	default:
		parsed, err := url.ParseRequestURI(path)
		if err != nil {
			// A malformed :path still goes to the origin as it came, as
			// it would without Marasi.
			parsed = &url.URL{Opaque: path}
		}
		u = parsed
	}
	u.Scheme, u.Host = scheme, host
	return u, host
}

// request returns the request as an http.Request. Its body is
// http.NoBody; the engine sets the payload. Pseudo-header fields map to
// Method, URL and Host and are not in Header, nor is a host field.
func (m *h2Request) request() *http.Request {
	method, _ := m.fields.get(":method")
	u, host := m.target()
	header := m.fields.view()
	delete(header, "Host")
	path, _ := m.fields.get(":path")
	r := &http.Request{
		Method:        method,
		URL:           u,
		Proto:         "HTTP/2.0",
		ProtoMajor:    2,
		Header:        header,
		Body:          http.NoBody,
		ContentLength: int64(len(m.body)),
		Host:          host,
		RequestURI:    path,
	}
	if len(m.trailers) > 0 {
		r.Trailer = m.trailers.view()
	}
	return r
}

// merge returns the request's fields with the view's edits folded back:
// Method into :method, the URL's path and query into :path, Host into
// :authority (or the host field when there is no :authority), and the
// Header by the rules of h2Fields.merge. A field the edit leaves alone stays
// as it came, even one that breaks RFC 9113.
func (m *h2Request) merge(r *http.Request) h2Fields {
	fields := m.fields
	if method, _ := fields.get(":method"); r.Method != "" && r.Method != method {
		fields = fields.set(":method", r.Method)
	}
	origURL, origHost := m.target()
	if r.URL != nil && r.URL.Opaque == origURL.Opaque && requestURI(r.URL) != requestURI(origURL) {
		fields = fields.set(":path", requestURI(r.URL))
	}
	view := make(http.Header, len(r.Header)+1)
	for k, values := range r.Header {
		if !strings.EqualFold(k, "Host") {
			view[k] = values
		}
	}
	_, authority := fields.get(":authority")
	if hosts := fields.view().Values("Host"); len(hosts) > 0 {
		view["Host"] = hosts
	}
	if r.Host != origHost && r.Host != "" {
		if authority {
			fields = fields.set(":authority", r.Host)
		} else {
			view["Host"] = []string{r.Host}
		}
	}
	return fields.merge(view)
}

// requestURI is the path and query of u as :path carries them.
func requestURI(u *url.URL) string {
	if u.Opaque != "" {
		return u.Opaque
	}
	if u.Path == "*" && u.RawQuery == "" {
		return "*"
	}
	return u.RequestURI()
}

// response returns the response as an http.Response with an http.NoBody
// body; the engine sets the payload. :status maps to StatusCode and Status
// and is not in Header.
func (m *h2Response) response() *http.Response {
	status, _ := m.fields.get(":status")
	code, _ := strconv.Atoi(status)
	r := &http.Response{
		Status:        strings.TrimSpace(status + " " + http.StatusText(code)),
		StatusCode:    code,
		Proto:         "HTTP/2.0",
		ProtoMajor:    2,
		Header:        m.fields.view(),
		Body:          http.NoBody,
		ContentLength: -1,
	}
	if n, err := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64); err == nil {
		r.ContentLength = n
	}
	if len(m.trailers) > 0 {
		r.Trailer = m.trailers.view()
	}
	return r
}

// merge returns the response's fields with the view's edits folded back:
// StatusCode into :status, and the Header by the rules of h2Fields.merge.
func (m *h2Response) merge(r *http.Response) h2Fields {
	fields := m.fields
	if status, _ := fields.get(":status"); r.StatusCode != 0 && strconv.Itoa(r.StatusCode) != status {
		fields = fields.set(":status", strconv.Itoa(r.StatusCode))
	}
	return fields.merge(r.Header)
}

// h2RequestText is the stored text form of an HTTP/2 request: the request
// line METHOD path HTTP/2, then each other pseudo-header field and each
// regular field as a line in wire order, then the blank line that ends the
// head. Readers that do not know the
// form skip lines that start with ":".
func h2RequestText(fields h2Fields) []byte {
	method, _ := fields.get(":method")
	path, _ := fields.get(":path")
	var b bytes.Buffer
	b.WriteString(method + " " + path + " HTTP/2\r\n")
	writeH2Fields(&b, fields, ":method", ":path")
	return b.Bytes()
}

// h2ResponseText is the stored text form of an HTTP/2 response head:
// HTTP/2 <status>, then the other fields in wire order.
func h2ResponseText(fields h2Fields) []byte {
	status, _ := fields.get(":status")
	var b bytes.Buffer
	b.WriteString("HTTP/2 " + status + "\r\n")
	writeH2Fields(&b, fields, ":status")
	return b.Bytes()
}

// writeH2Fields writes each field but the first of each name in skip as a
// "name: value" line, then the blank line that ends the head.
func writeH2Fields(b *bytes.Buffer, fields h2Fields, skip ...string) {
	skipped := make(map[string]bool)
	for _, f := range fields {
		if slices.Contains(skip, f.Name) && !skipped[f.Name] {
			skipped[f.Name] = true
			continue
		}
		b.WriteString(f.Name + ": " + f.Value + "\r\n")
	}
	b.WriteString("\r\n")
}

// h2FieldsFromHeader turns a response Marasi made into a header block: the
// status, then its fields in lowercase, leaving out the fields HTTP/2 does
// not carry (RFC 9113 §8.2.2), with a content-length for its body.
func h2FieldsFromHeader(status int, header http.Header, bodyLength int) h2Fields {
	fields := h2Fields{{Name: ":status", Value: strconv.Itoa(status)}}
	keys := make([]string, 0, len(header))
	for k := range header {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		name := strings.ToLower(k)
		switch name {
		case "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade", "content-length":
			continue
		}
		for _, value := range header[k] {
			fields = append(fields, h2Field{Name: name, Value: value})
		}
	}
	if status != http.StatusNoContent && status != http.StatusNotModified && status >= 200 {
		fields = append(fields, h2Field{Name: "content-length", Value: strconv.Itoa(bodyLength)})
	}
	return fields
}
