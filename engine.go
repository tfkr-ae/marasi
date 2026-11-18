package marasi

import (
	"net/http"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// The engine (h1Engine, h1engine*.go) serves the proxy listener: it reads
// each client connection with the HTTP/1.1 codec (internal/h1), answers
// CONNECT, runs the base pipeline's steps for each request and response, and
// carries requests to the origin itself
// (docs/adr/2026-11-17-marasi-owns-its-http1-wire-format.md). Everything
// Marasi does to a pair lives behind the steps below. The seam is:
//
//   - the request step (baseRequest): run for every request the engine
//     reads, a CONNECT included, which it admits through admitTunnel;
//   - the round trip, which belongs to the engine;
//   - the response step (baseResponse), which captures the pair and hands a
//     101 to the upgrade handler through the pair's clientHijacker;
//   - the wire message: the engine hands the request and response steps the
//     raw message each view came from, so capture stores the bytes that were
//     on the wire.

// requestStep is the base pipeline's request step. client takes over the
// client connection. wire is the request as read from the wire, nil for a
// CONNECT, which has no message to keep.
type requestStep func(req *http.Request, client clientHijacker, wire *wireRequest) error

// responseStep is the base pipeline's response step. res.Request is the
// pair's request. wire is the response as read from the origin, nil when
// Marasi made the response.
type responseStep func(res *http.Response, wire *wireResponse) error

// wireRequest is a request as it was read from the client: its head, raw
// lines kept byte for byte, and its body as framed on the wire. The pair's
// Request is a view of it; outboundRequest folds the view's edits back.
type wireRequest struct {
	head *h1.RequestHead
	body *h1.Body
	// refused is why the request's length cannot be framed safely, nil
	// for any other request. A refused request has no body and never goes
	// upstream (docs/adr/2026-11-17-ambiguous-request-framing-is-refused-with-a-400.md).
	refused error
}

// wireResponse is a response as it was read from the origin. The pair's
// Response is a view of it; outboundResponse folds the view's edits back.
type wireResponse struct {
	head *h1.ResponseHead
	body *h1.Body
}

// setRoundTripper makes the routed transport under base the one the engine
// dials origins through. Serve sets the proxy's own; tests that serve the
// engine directly set one that trusts their self-signed origins.
func (proxy *Proxy) setRoundTripper(base http.RoundTripper) {
	proxy.engine.setRoundTripper(base)
}
