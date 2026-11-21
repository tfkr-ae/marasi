package marasi

import (
	"context"
	"errors"

	"golang.org/x/net/http2"
)

// h2c by prior knowledge (docs/adr/2026-11-19-marasi-gives-the-client-the-origins-protocol.md).
// In cleartext there is no ALPN, so Marasi copies the client: a client that
// opens with the HTTP/2 preface, on the proxy listener or inside a plain
// CONNECT tunnel, is served by the HTTP/2 engine, and its origin is spoken to
// by prior knowledge too. Upgrade: h2c stays an opaque relay (upgrade.go).

// protocolH2C is h1Conn.protocol for a client that spoke HTTP/2 by prior
// knowledge: RFC 9113's identifier for HTTP/2 over cleartext TCP.
const protocolH2C = "h2c"

// errH2CClosed is the cause when a stream asks for the origin leg after the
// client connection ended.
var errH2CClosed = errors.New("the client connection ended")

// startsPriorKnowledge reports whether the client's next bytes are the
// HTTP/2 connection preface. It reads no further than they match, so a short
// HTTP/1.1 request is never waited on, and it consumes nothing: the engine
// reads the preface itself.
func (c *h1Conn) startsPriorKnowledge() bool {
	for n := 1; n <= len(http2.ClientPreface); n++ {
		peeked, err := c.br.Peek(n)
		if err != nil || string(peeked) != http2.ClientPreface[:n] {
			return false
		}
	}
	return true
}

// dialPriorKnowledgeOrigin dials target, a host:port, in cleartext on the
// current route, sent to its waypoint when it has one. Its connection is the
// client connection's origin connection, opened as HTTP/2 by prior knowledge.
func (c *h1Conn) dialPriorKnowledgeOrigin(ctx context.Context, target string) *handshakeDial {
	proxy := c.engine.proxy
	addr := target
	if override, ok := proxy.waypointOverride(addr); ok {
		addr = override
	}
	ctx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()
	conn, err := proxy.dialOrigin(ctx, "http", addr)
	if err != nil {
		return &handshakeDial{target: target, addr: addr, err: err, route: proxy.CurrentRoute()}
	}
	return &handshakeDial{target: target, addr: addr, conn: conn, route: conn.route}
}

// cleartext reports whether the client speaks h2c, so its requests are http
// and its origin is spoken to without TLS.
func (h *h2Conn) cleartext() bool {
	return h.c.protocol == protocolH2C
}

// scheme is the scheme of the connection's requests, and the port an origin
// named without one listens on.
func (h *h2Conn) scheme() (scheme, defaultPort string) {
	if h.cleartext() {
		return "http", "80"
	}
	return "https", "443"
}

// openOriginFor opens the origin leg for a request to addr, when the client
// connection has none yet: h2c sent straight to the listener names its
// origin only by its first request's authority. Every other client
// connection's origin leg was dialled before it was served.
func (h *h2Conn) openOriginFor(ctx context.Context, addr string) {
	if !h.lazyOrigin {
		return
	}
	h.dialMu.Lock()
	defer h.dialMu.Unlock()
	if h.originDial.Load() != nil {
		return
	}
	h.mu.Lock()
	ended := h.ended
	h.mu.Unlock()
	if ended {
		h.originAddr, h.originErr = addr, errH2CClosed
		h.originDial.Store(&handshakeDial{target: addr, addr: addr, err: errH2CClosed, route: h.engine.proxy.CurrentRoute()})
		return
	}
	dialled := h.c.dialPriorKnowledgeOrigin(ctx, addr)
	h.originAddr, h.originErr = dialled.addr, dialled.err
	h.originDial.Store(dialled)
	h.openOrigin()
	origin := h.originLeg()
	if origin == nil {
		return
	}
	h.mu.Lock()
	if h.ended {
		h.mu.Unlock()
		origin.close()
		return
	}
	set, limit := h.maxStreamsSet, h.maxStreams
	h.mu.Unlock()
	if set {
		// The client's first SETTINGS went out before there was an origin
		// to take the limit from.
		h.w.do(func(f *http2.Framer) error {
			return f.WriteSettings(http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: limit})
		})
	}
}
