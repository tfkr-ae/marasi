package marasi

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// An engine serves the proxy listener: it reads each client connection,
// answers CONNECT, runs the base pipeline's steps for each request and
// response, and carries requests to the origin. Two engines exist until the
// switch (docs/adr/2026-11-17-marasi-owns-its-http1-wire-format.md):
//
//   - martianEngine (martian_adapter.go), the default;
//   - h1Engine (h1engine*.go), Marasi's own HTTP/1.1 engine, chosen by the
//     h1engine build tag (conformance) or the withH1Engine option (tests).
//
// Everything Marasi does to a pair lives behind the steps below, so it never
// sees which engine runs. The seam is:
//
//   - the request step (baseRequest): run for every request the engine
//     reads, a CONNECT included, which it admits through admitTunnel;
//   - the round trip, which belongs to the engine;
//   - the response step (baseResponse), which captures the pair and hands a
//     101 to the upgrade handler through the pair's clientHijacker;
//   - the wire message: an engine that reads the wire itself hands the
//     request and response steps the raw message each view came from, so
//     capture stores the bytes that were on the wire.
type engine interface {
	// serve serves connections from l until l closes or the engine closes.
	serve(l net.Listener) error
	// startClose stops the engine taking new requests; waitClosed waits
	// for the connections it is serving to finish.
	startClose()
	waitClosed()
	// setMITM makes the engine intercept TLS, issuing leaf certificates
	// from the CA, and returns the TLS config it serves clients.
	setMITM(certificate *x509.Certificate, privateKey any) (*tls.Config, error)
	// setPipeline sets the steps the engine runs for each request and
	// response.
	setPipeline(request requestStep, response responseStep)
	// setRoundTripper sets the transport a Martian round trip goes
	// through. Marasi's own engine carries requests itself and ignores it.
	setRoundTripper(base http.RoundTripper)
}

// requestStep is the base pipeline's request step. client takes over the
// client connection. wire is the request as read from the wire, nil when the
// engine does not keep it.
type requestStep func(req *http.Request, client clientHijacker, wire *wireRequest) error

// responseStep is the base pipeline's response step. res.Request is the
// pair's request. wire is the response as read from the origin, nil when the
// engine does not keep it or Marasi made the response.
type responseStep func(res *http.Response, wire *wireResponse) error

// wireRequest is a request as it was read from the client: its head, raw
// lines kept byte for byte, and its body as framed on the wire. The pair's
// Request is a view of it; outboundRequest folds the view's edits back.
type wireRequest struct {
	head *h1.RequestHead
	body *h1.Body
}

// wireResponse is a response as it was read from the origin. The pair's
// Response is a view of it; outboundResponse folds the view's edits back.
type wireResponse struct {
	head *h1.ResponseHead
	body *h1.Body
}

// setRoundTripper makes base the transport the engine's round trip uses,
// behind the step that returns a response from Respond. Everything that sets
// the transport goes through it, so Respond works whatever the transport.
func (proxy *Proxy) setRoundTripper(base http.RoundTripper) {
	proxy.engine.setRoundTripper(base)
}

// errEngineConfigured is returned by withH1Engine when an earlier option
// already configured the engine it would replace.
var errEngineConfigured = errors.New("withH1Engine must be the first option")

// withH1Engine makes the proxy run on Marasi's own HTTP/1.1 engine instead of
// the default engine. It is for tests, and must be the first option, before
// any option that configures the engine (WithTLS, WithBasePipeline).
func withH1Engine() func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.mitmConfig != nil || proxy.pipelineSet {
			return errEngineConfigured
		}
		proxy.engine = newH1Engine(proxy)
		return nil
	}
}
