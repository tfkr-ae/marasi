package marasi

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/martian"
	"github.com/google/martian/mitm"
	"github.com/google/martian/proxyutil"
	"github.com/tfkr-ae/marasi/listener"
)

// This is the only file in marasi that imports Martian (repolint enforces
// it). Martian serves the listener and runs one request at a time through
// the base pipeline's request and response steps; everything Marasi does to
// a pair lives outside this file and never sees a Martian type.

// martianEngine is the Martian proxy that serves Marasi's listener.
type martianEngine struct {
	proxy     *martian.Proxy
	closeOnce sync.Once
	closeDone chan struct{}
}

func newMartianEngine() *martianEngine {
	return &martianEngine{proxy: martian.NewProxy()}
}

// serve serves connections from l until the engine closes.
func (engine *martianEngine) serve(l net.Listener) error {
	return engine.proxy.Serve(listener.NewClosingListener(l, engine.proxy.Closing))
}

// startClose stops the engine from taking new connections. It returns once
// the engine reports it is closing; waitClosed waits for the rest.
func (engine *martianEngine) startClose() {
	engine.closeOnce.Do(func() {
		engine.closeDone = make(chan struct{})
		go func() {
			engine.proxy.Close()
			close(engine.closeDone)
		}()
	})
	for !engine.proxy.Closing() {
		time.Sleep(time.Millisecond)
	}
}

// waitClosed waits until the engine's Close returns. Martian increments its
// handler wait count inside each handler's goroutine, so it can return before
// a just-accepted handler starts: the proxy joins accepted sockets itself.
func (engine *martianEngine) waitClosed() {
	<-engine.closeDone
}

// setMITM makes the engine intercept tunnels, issuing leaf certificates from
// the CA certificate and key. It returns the TLS config it serves clients.
func (engine *martianEngine) setMITM(certificate *x509.Certificate, privateKey any) (*tls.Config, error) {
	config, err := mitm.NewConfig(certificate, privateKey)
	if err != nil {
		return nil, fmt.Errorf("creating new mitm config: %w", err)
	}
	engine.proxy.SetMITM(config)
	return config.TLS(), nil
}

// setPipeline makes request and response the steps Martian runs for every
// request and response. Martian runs request on a CONNECT too, and response
// on the 200 it makes to open the tunnel.
func (engine *martianEngine) setPipeline(
	request func(req *http.Request, client clientHijacker) error,
	response func(res *http.Response) error,
) {
	engine.proxy.SetRequestModifier(martian.RequestModifierFunc(func(req *http.Request) error {
		// Martian keeps one session per connection and finds a request's
		// context by its address, which is why the pipeline edits requests
		// in place.
		return request(req, martian.NewContext(req).Session())
	}))
	engine.proxy.SetResponseModifier(martian.ResponseModifierFunc(response))
}

// setRoundTripper makes base Martian's round tripper, behind the step that
// returns a response from Respond. Everything that sets Martian's round
// tripper goes through it, so Respond works whatever the transport.
func (proxy *Proxy) setRoundTripper(base http.RoundTripper) {
	proxy.engine.proxy.SetRoundTripper(&respondRoundTripper{proxy: proxy, base: base})
}

// respondRoundTripper returns the response a request modifier answered a
// pair with through Respond, in place of a round trip, and the empty 200
// that a dropped request has always got. Every other request goes to base.
// A round trip that gets no response is answered with a proxy error
// response, never with an error: Martian would answer that with an empty 502
// carrying a Warning header.
type respondRoundTripper struct {
	proxy *Proxy
	base  http.RoundTripper
}

// RoundTrip satisfies http.RoundTripper.
func (rt *respondRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	pair := requestPair(req)
	if pair != nil && pair.control == controlRespond {
		return pair.Response, nil
	}
	if pair != nil && pair.Dropped() {
		return proxyutil.NewResponse(http.StatusOK, nil, req), nil
	}
	res, err := rt.base.RoundTrip(req)
	if err == nil {
		return res, nil
	}
	if pair == nil {
		res := plainTextResponse(req, http.StatusBadGateway, err.Error())
		res.Close = true
		return res, nil
	}
	rt.proxy.respondWithProxyError(pair, http.StatusBadGateway, err)
	rt.proxy.logProxyError(pair, err)
	return pair.Response, nil
}
