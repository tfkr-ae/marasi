package marasi

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// webSocketRemovedExtensionsKey holds the WebSocket extensions removed from an upgrade offer.
type webSocketRemovedExtensionsKey struct{}

// webSocketNegotiation is the extension negotiation the upstream completed.
type webSocketNegotiation struct {
	// accept is the upstream's Sec-WebSocket-Extensions response, sent to the client unchanged.
	accept []string
	// deflate is the negotiated permessage-deflate configuration, or nil.
	deflate *domain.WebSocketDeflate
}

// WebSocketOffersModifier removes every WebSocket extension offer except
// permessage-deflate from an upgrade request and records the removed names.
// It runs after Extensions and Checkpoint so their edits are filtered too.
func WebSocketOffersModifier(proxy *Proxy, pair *Pair) error {
	if !marasiws.IsUpgradeRequest(pair.Request) {
		return nil
	}

	pair.removedWebSocketExtensions = marasiws.FilterExtensionOffers(pair.Request.Header)
	return nil
}

// negotiateWebSocketExtensions checks the upstream's extension response
// against the offer it received and returns the negotiation to mirror to the client.
func negotiateWebSocketExtensions(pair *Pair) (webSocketNegotiation, error) {
	accept := pair.Response.Header.Values(marasiws.ExtensionsHeader)
	deflate, err := marasiws.NegotiateDeflate(
		pair.Request.Header.Values(marasiws.ExtensionsHeader),
		accept,
	)
	if err != nil {
		return webSocketNegotiation{}, err
	}
	return webSocketNegotiation{accept: slices.Clone(accept), deflate: deflate}, nil
}

// requestLogOptions ties a proxy log to the request, when it has an ID.
func requestLogOptions(req *http.Request) []func(*domain.Log) error {
	if requestID, ok := core.RequestIDFromContext(req.Context()); ok {
		return []func(*domain.Log) error{core.LogWithReqResID(requestID)}
	}
	return nil
}

// pairLogOptions ties a proxy log to the pair, when it has an ID.
func pairLogOptions(pair *Pair) []func(*domain.Log) error {
	if pair.ID != uuid.Nil {
		return []func(*domain.Log) error{core.LogWithReqResID(pair.ID)}
	}
	return nil
}

// failWebSocketHandshake writes a proxy log for a handshake the proxy cannot
// complete, marks the upgrade request's websocket.state failed so it is not
// mistaken for a handshake still in flight, and drops the pair, which closes
// the client connection.
func failWebSocketHandshake(proxy *Proxy, pair *Pair, cause error) {
	_ = proxy.WriteLog("ERROR", fmt.Sprintf("WebSocket handshake failed: %v", cause), pairLogOptions(pair)...)
	if pair.ID != uuid.Nil {
		proxy.endWebSocketHandshake(pair.ID, "failed")
	}
	pair.webSocketFailed = true
	pair.Drop()
}

// WebSocketNegotiationModifier sets the client's extension response to
// exactly what the upstream accepted, undoing any edit made after the
// upstream answered. It runs after Extensions and Checkpoint and before
// WriteResponseModifier, so the stored response is the one the client gets.
func WebSocketNegotiationModifier(proxy *Proxy, pair *Pair) error {
	res := pair.Response
	if !marasiws.IsUpgradeResponse(res) {
		return nil
	}
	negotiation := pair.webSocketNegotiation
	if negotiation == nil {
		return nil
	}

	if !slices.Equal(res.Header.Values(marasiws.ExtensionsHeader), negotiation.accept) {
		_ = proxy.WriteLog(
			"WARN",
			fmt.Sprintf(
				"Ignoring an edit to %s on the WebSocket handshake response: the client gets %q, as the upstream accepted",
				marasiws.ExtensionsHeader,
				negotiation.accept,
			),
			pairLogOptions(pair)...,
		)
	}
	res.Header.Del(marasiws.ExtensionsHeader)
	for _, value := range negotiation.accept {
		res.Header.Add(marasiws.ExtensionsHeader, value)
	}
	return nil
}

// webSocketNegotiated returns the negotiated deflate configuration of an
// upgrade, or nil, and the extension offers removed from its request.
func webSocketNegotiated(pair *Pair) (*domain.WebSocketDeflate, []string) {
	removed := pair.removedWebSocketExtensions
	if pair.webSocketNegotiation == nil {
		return nil, removed
	}
	return pair.webSocketNegotiation.deflate, removed
}
