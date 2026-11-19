package marasi

import (
	"bytes"
	"io"
	"net/http"
)

// Checkpoint on an HTTP/2 message shows its text form as it goes out, which
// is also what capture stores, and reads an edit back with
// readH2RequestText or readH2ResponseText. The edit's field list replaces
// the wire message's and its body is the payload as typed, so a field or a
// content-length that breaks RFC 9113 goes out as written
// (docs/adr/2026-11-19-marasi-owns-its-http2-framing.md). Trailers and
// where END_STREAM fell are kept from the message the engine read.

// h2RequestEdit is the Checkpoint edit of an HTTP/2 request. An edit goes
// where the held request was going: :authority and host are only fields.
// An edit whose request line names a target goes there.
func h2RequestEdit(original *http.Request, wire *h2Request) *checkpointEdit {
	var req *http.Request
	var rebuilt *wireRequest
	return &checkpointEdit{
		rebuild: func(raw []byte) error {
			out, err := h2OutboundRequest(wire, original)
			if err != nil {
				return err
			}
			text, err := readH2RequestText(raw, out.fields)
			if err != nil {
				return err
			}
			message := &h2Request{fields: text.fields, body: text.body, trailers: wire.trailers, endsOnData: wire.endsOnData || len(text.body) > 0}
			view := message.request()
			view.RemoteAddr = original.RemoteAddr
			view.TLS = original.TLS
			view.URL.Scheme = original.URL.Scheme
			view.URL.Host = original.URL.Host
			if text.authority != "" {
				view.URL.Host = text.authority
			}
			if len(text.body) > 0 {
				view.Body = io.NopCloser(bytes.NewReader(text.body))
			}
			req, rebuilt = view.WithContext(original.Context()), &wireRequest{h2: message}
			return nil
		},
		apply: func(pair *Pair) {
			pair.Request, pair.requestWire = req, rebuilt
		},
	}
}

// h2ResponseEdit is the Checkpoint edit of an HTTP/2 response.
func h2ResponseEdit(original *http.Response, wire *h2Response) *checkpointEdit {
	var res *http.Response
	var rebuilt *wireResponse
	return &checkpointEdit{
		rebuild: func(raw []byte) error {
			out, err := h2OutboundResponse(wire, original)
			if err != nil {
				return err
			}
			fields, body, err := readH2ResponseText(raw, out.fields)
			if err != nil {
				return err
			}
			message := &h2Response{fields: fields, body: body, trailers: wire.trailers, endsOnData: wire.endsOnData || len(body) > 0}
			view := message.response()
			view.Request = original.Request
			if len(body) > 0 {
				view.Body = io.NopCloser(bytes.NewReader(body))
			}
			res, rebuilt = view, &wireResponse{h2: message}
			return nil
		},
		apply: func(pair *Pair) {
			*pair.Response = *res
			pair.responseWire = rebuilt
		},
	}
}
