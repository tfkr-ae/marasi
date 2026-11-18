package marasi

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/google/uuid"
)

// proxyErrorMetadataKey is the metadata field that holds the cause of a proxy
// error response, so a stored pair tells Marasi's answer from the origin's.
const proxyErrorMetadataKey = "proxy_error"

// errLoop is the cause of the proxy error response to a request that would
// loop back to the proxy listener.
var errLoop = errors.New("the request loops back to the proxy listener")

// respondWithProxyError answers pair with a proxy error response: a plain-text
// status response giving cause, which Marasi made because it could not get a
// response from the origin (docs/adr/2026-11-17-go-modifier-errors-fail-closed-and-proxy-error-responses-never-go-upstream.md).
// The request never goes upstream, the cause goes into metadata.proxy_error,
// and the client connection closes after the response.
func (proxy *Proxy) respondWithProxyError(pair *Pair, status int, cause error) {
	message := cause.Error()
	pair.proxyError = message
	if pair.Metadata != nil {
		pair.Metadata[proxyErrorMetadataKey] = message
	}
	res := plainTextResponse(pair.Request, status, message)
	// The request's body may be unread or malformed, so nothing after it on
	// the connection can be trusted to start a new request.
	res.Close = true
	pair.Respond(res)
}

// logProxyError writes the cause of pair's proxy error response to the proxy
// log. An upstream proxy failure was already logged by the round trip, and a
// request whose client left is not an error worth a log.
func (proxy *Proxy) logProxyError(pair *Pair, cause error) {
	var routeErr *UpstreamProxyError
	if errors.As(cause, &routeErr) || pair.Context().Err() != nil {
		return
	}
	request := "request"
	if pair.Request != nil && pair.Request.URL != nil {
		request = fmt.Sprintf("request %s %s", pair.Request.Method, pair.Request.URL.Redacted())
	}
	entry := fmt.Sprintf("Proxy error response (%s): %s", request, cause)
	if err := proxy.WriteLog("ERROR", entry, pairLogOptions(pair)...); err != nil {
		log.Printf("writing proxy error log: %v", err)
	}
}

// capturesProxyError reports whether pair was answered with a proxy error
// response that is stored: its request was captured, so the response chain
// runs to capture the response too.
func (pair *Pair) capturesProxyError() bool {
	return pair.control == controlRespond && pair.proxyError != "" && pair.requestCaptured
}

// refuseRequest answers a request whose length cannot be framed safely with a
// 400 proxy error response giving cause, and stores the pair
// (docs/adr/2026-11-17-ambiguous-request-framing-is-refused-with-a-400.md).
// No request modifier runs: the request never goes upstream, so there is
// nothing to scope, hold or edit. The pair is only set up and captured, so
// the refusal is stored whatever the scope, as a tester studying smuggling
// wants to see it.
func (proxy *Proxy) refuseRequest(pair *Pair, cause error) {
	// The same context sync a chain modifier gets, so the response side
	// loads what was set up here.
	req := pair.Request
	defer storePair(pair, loadPair(pair, req, nil), req)
	if err := SetupRequestModifier(proxy, pair); err != nil {
		log.Printf("setting up a refused request: %v", err)
	}
	if pair.Dropped() {
		// A second claim of the same launch.
		return
	}
	proxy.respondWithProxyError(pair, http.StatusBadRequest, cause)
	if pair.ID != uuid.Nil {
		if err := WriteRequestModifier(proxy, pair); err != nil {
			log.Printf("capturing a refused request: %v", err)
		}
	}
	proxy.logProxyError(pair, cause)
}

// failRequestChain ends a request whose Go request modifier returned err. A
// dropped pair stays dropped, and the cause is logged. Any other is answered
// with a 502 proxy error response, and its request is captured when the chain
// had set the pair up but not yet captured it, so the pair is stored with the
// response and the request already holds metadata.proxy_error.
func (proxy *Proxy) failRequestChain(pair *Pair, err error) {
	cause := fmt.Errorf("running the request chain: %w", err)
	if pair.Dropped() {
		if logErr := proxy.WriteLog("ERROR", cause.Error(), pairLogOptions(pair)...); logErr != nil {
			log.Printf("writing the request chain's log: %v", logErr)
		}
		return
	}
	proxy.respondWithProxyError(pair, http.StatusBadGateway, cause)
	if pair.ID != uuid.Nil && !pair.Skipped && !pair.requestCaptured {
		if err := WriteRequestModifier(proxy, pair); err != nil {
			log.Printf("capturing the request of a proxy error response: %v", err)
		}
	}
	proxy.logProxyError(pair, cause)
}

// failResponseChain ends a response whose Go response modifier returned err.
// The origin's response, which the chain may have half-edited, never reaches
// the client: the pair is answered with a 502 proxy error response, stored
// with it unless the chain had already captured the origin's, and the cause
// is logged.
func (proxy *Proxy) failResponseChain(pair *Pair, err error) {
	for _, upstream := range []*io.ReadWriteCloser{&pair.webSocketUpstream, &pair.upgradeUpstream} {
		if *upstream != nil {
			(*upstream).Close()
			*upstream = nil
		}
	}
	cause := fmt.Errorf("running the response chain: %w", err)
	proxy.respondWithProxyError(pair, http.StatusBadGateway, cause)
	if pair.ID != uuid.Nil && pair.requestCaptured && !pair.responseCaptured && !pair.Skipped {
		if err := WriteResponseModifier(proxy, pair); err != nil {
			log.Printf("capturing a proxy error response: %v", err)
		}
	}
	proxy.logProxyError(pair, cause)
}
