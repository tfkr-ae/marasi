package marasi

import (
	"bufio"
	"bytes"
	"cmp"
	"fmt"
	"io"
	"net/http"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// Checkpoint on a pair the engine read from the wire shows the message as it
// goes out, which is also what capture stores: the outbound head and the
// payload, chunked coding removed (dumpWireRequest, dumpWireResponse). An
// edit is raw text: its head replaces the head lines as written and the text
// after it is the payload, which the engine frames on the way out against
// the body it read, so a chunked message stays chunked with its trailers and
// an unchanged one goes out as it arrived
// (docs/adr/2026-11-17-marasi-owns-its-http1-wire-format.md).

// checkpointEdit is how Checkpoint reads an edit of one held message: rebuild
// reads the edited text and keeps the result for apply, which puts it on the
// pair once the hold has ended.
type checkpointEdit struct {
	rebuild func(raw []byte) error
	apply   func(pair *Pair)
}

// rebuildWireRequest reads an edited request through the codec. The view
// gets what the engine gave the original (client address, TLS, scheme,
// context), and the wire keeps the body that was read.
func rebuildWireRequest(raw []byte, original *http.Request, wire *wireRequest) (*http.Request, *wireRequest, error) {
	br := bufio.NewReader(bytes.NewReader(raw))
	head, err := h1.ReadRequestHead(br)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the edited request head: %w", err)
	}
	payload, _ := io.ReadAll(br)
	view, err := head.Request()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the edited request: %w", err)
	}
	view.RemoteAddr = original.RemoteAddr
	view.TLS = original.TLS
	// Checkpoint holds only requests the engine carries, a CONNECT sent
	// inside a tunnel included, so every one needs the scheme.
	view.URL.Scheme = original.URL.Scheme
	// An edit goes where the held request was going: its Host is only a
	// field. An edit whose request line names a target goes there.
	if view.URL.Host == "" {
		view.URL.Host = cmp.Or(original.URL.Host, view.Host)
	}
	if len(payload) > 0 {
		view.Body = io.NopCloser(bytes.NewReader(payload))
	}
	return view.WithContext(original.Context()), &wireRequest{head: head, body: wire.body}, nil
}

// rebuildWireResponse reads an edited response through the codec, keeping
// the original's request and the body that was read.
func rebuildWireResponse(raw []byte, original *http.Response, wire *wireResponse) (*http.Response, *wireResponse, error) {
	br := bufio.NewReader(bytes.NewReader(raw))
	head, err := h1.ReadResponseHead(br)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the edited response head: %w", err)
	}
	payload, _ := io.ReadAll(br)
	view := head.Response()
	view.Request = original.Request
	if len(payload) > 0 {
		view.Body = io.NopCloser(bytes.NewReader(payload))
	}
	return view, &wireResponse{head: head, body: wire.body}, nil
}

// wireRequestEdit is the Checkpoint edit of a request the engine read.
func wireRequestEdit(original *http.Request, wire *wireRequest) *checkpointEdit {
	if wire.h2 != nil {
		return h2RequestEdit(original, wire.h2)
	}
	var req *http.Request
	var rebuilt *wireRequest
	return &checkpointEdit{
		rebuild: func(raw []byte) (err error) {
			req, rebuilt, err = rebuildWireRequest(raw, original, wire)
			return err
		},
		apply: func(pair *Pair) {
			pair.Request, pair.requestWire = req, rebuilt
		},
	}
}

// wireResponseEdit is the Checkpoint edit of a response the engine read.
func wireResponseEdit(original *http.Response, wire *wireResponse) *checkpointEdit {
	if wire.h2 != nil {
		return h2ResponseEdit(original, wire.h2)
	}
	var res *http.Response
	var rebuilt *wireResponse
	return &checkpointEdit{
		rebuild: func(raw []byte) (err error) {
			res, rebuilt, err = rebuildWireResponse(raw, original, wire)
			return err
		},
		apply: func(pair *Pair) {
			*pair.Response = *res
			pair.responseWire = rebuilt
		},
	}
}
