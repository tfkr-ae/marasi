package marasi

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tfkr-ae/marasi/internal/h1"
	"github.com/tfkr-ae/marasi/rawhttp"
)

// This file is the seam between a pair's views and the wire. The request and
// response a modifier edits are views (http.Request, http.Response) of the
// raw messages the engine read; outboundRequest and outboundResponse fold the
// views' edits back into a copy of the raw message by the codec's merge rules
// and give the bytes that go out. Capture stores the same bytes, so the
// stored pair is the wire form (docs/adr/2026-11-17-marasi-owns-its-http1-wire-format.md).

// outbound is a message as it goes out: its head, the body exactly as framed
// on the wire, and the body's payload (chunked coding removed), which is what
// capture stores.
type outbound[H any] struct {
	head    H
	body    []byte
	payload []byte
}

// outboundRequest gives the request as it goes upstream: the wire head with
// the view's edits merged, its target in origin form, and its body.
func outboundRequest(wire *wireRequest, req *http.Request) (outbound[*h1.RequestHead], error) {
	var out outbound[*h1.RequestHead]
	payload, err := readViewBody(&req.Body)
	if err != nil {
		return out, err
	}
	head, err := h1.ReadRequestHead(bufio.NewReader(bytes.NewReader(wire.head.Bytes())))
	if err != nil {
		return out, fmt.Errorf("copying the request head: %w", err)
	}
	if err := head.Merge(req); err != nil {
		return out, fmt.Errorf("merging the request: %w", err)
	}
	if err := toOriginForm(head); err != nil {
		return out, err
	}
	head.Header.RemoveConnectionOptions(true)
	if wire.refused != nil {
		// Stored as read: its framing fields are the evidence, and it
		// never goes out to be framed.
		return outbound[*h1.RequestHead]{head: head}, nil
	}
	body := h1.FrameRequestBody(head, wire.body, payload)
	return outbound[*h1.RequestHead]{head: head, body: body, payload: payload}, nil
}

// outboundResponse gives the response as it goes to the client: the wire
// head with the view's edits merged, and its body.
func outboundResponse(wire *wireResponse, res *http.Response) (outbound[*h1.ResponseHead], error) {
	var out outbound[*h1.ResponseHead]
	payload, err := readViewBody(&res.Body)
	if err != nil {
		return out, err
	}
	head, err := h1.ReadResponseHead(bufio.NewReader(bytes.NewReader(wire.head.Bytes())))
	if err != nil {
		return out, fmt.Errorf("copying the response head: %w", err)
	}
	if err := head.Merge(res); err != nil {
		return out, fmt.Errorf("merging the response: %w", err)
	}
	head.Header.RemoveConnectionOptions(head.StatusCode == http.StatusSwitchingProtocols)
	method := ""
	if res.Request != nil {
		method = res.Request.Method
	}
	body := h1.FrameResponseBody(head, method, wire.body, payload)
	return outbound[*h1.ResponseHead]{head: head, body: body, payload: payload}, nil
}

// dumpWireRequest is the stored raw text of a request the engine read from
// the wire: the request as it goes upstream.
func dumpWireRequest(wire *wireRequest, req *http.Request) ([]byte, string, error) {
	out, err := outboundRequest(wire, req)
	if err != nil {
		return nil, "", err
	}
	raw, prettified := rawhttp.Assemble(out.head.Bytes(), out.payload)
	return raw, prettified, nil
}

// dumpWireResponse is the stored raw text of a response the engine read from
// the origin, after the response modifiers.
func dumpWireResponse(wire *wireResponse, res *http.Response) ([]byte, string, error) {
	out, err := outboundResponse(wire, res)
	if err != nil {
		return nil, "", err
	}
	raw, prettified := rawhttp.Assemble(out.head.Bytes(), out.payload)
	return raw, prettified, nil
}

// readViewBody reads a view's body whole and puts it back, so the next reader
// sees it too.
func readViewBody(body *io.ReadCloser) ([]byte, error) {
	if *body == nil || *body == http.NoBody {
		return nil, nil
	}
	data, err := io.ReadAll(*body)
	(*body).Close()
	if err != nil {
		*body = cutShortBody(data, err)
		return nil, fmt.Errorf("reading the body: %w", err)
	}
	*body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

// toOriginForm rewrites an absolute-form target into the origin form an
// origin expects (RFC 9112 §3.2.1), adding Host when the request had none.
func toOriginForm(head *h1.RequestHead) error {
	if head.Method == http.MethodConnect || strings.HasPrefix(head.Target, "/") || head.Target == "*" {
		return nil
	}
	target, err := url.ParseRequestURI(head.Target)
	if err != nil {
		return fmt.Errorf("parsing the request target %q: %w", head.Target, err)
	}
	if !head.Header.Has("Host") {
		view := head.Header.View()
		view["Host"] = []string{target.Host}
		if err := head.Header.Merge(view); err != nil {
			return fmt.Errorf("adding the Host field: %w", err)
		}
	}
	eol := "\n"
	if bytes.HasSuffix(head.Line, []byte("\r\n")) {
		eol = "\r\n"
	}
	head.Target = target.RequestURI()
	head.Line = []byte(head.Method + " " + head.Target + " " + head.Proto + eol)
	return nil
}
