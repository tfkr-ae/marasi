package marasi

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// webSocketRemovedExtensionsKey holds the WebSocket extensions removed from an upgrade offer.
type webSocketRemovedExtensionsKey struct{}

// webSocketNegotiationKey holds the webSocketNegotiation of an upgrade response.
type webSocketNegotiationKey struct{}

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
func WebSocketOffersModifier(proxy *Proxy, req *http.Request) error {
	if !marasiws.IsUpgradeRequest(req) {
		return nil
	}

	removed := marasiws.FilterExtensionOffers(req.Header)
	*req = *req.WithContext(context.WithValue(req.Context(), webSocketRemovedExtensionsKey{}, removed))
	return nil
}

// negotiateWebSocketExtensions checks the upstream's extension response
// against the offer it received and returns the negotiation to mirror to the client.
func negotiateWebSocketExtensions(res *http.Response) (webSocketNegotiation, error) {
	accept := res.Header.Values(marasiws.ExtensionsHeader)
	deflate, err := marasiws.NegotiateDeflate(
		res.Request.Header.Values(marasiws.ExtensionsHeader),
		accept,
	)
	if err != nil {
		return webSocketNegotiation{}, err
	}
	return webSocketNegotiation{accept: slices.Clone(accept), deflate: deflate}, nil
}

// failWebSocketHandshake writes a proxy log for a handshake the proxy cannot
// complete and returns an error that closes the client connection.
func failWebSocketHandshake(proxy *Proxy, res *http.Response, cause error) error {
	var options []func(*domain.Log) error
	if requestID, ok := core.RequestIDFromContext(res.Request.Context()); ok {
		options = append(options, core.LogWithReqResID(requestID))
	}
	_ = proxy.WriteLog("ERROR", fmt.Sprintf("WebSocket handshake failed: %v", cause), options...)
	return fmt.Errorf("%w: %w", ErrDropped, cause)
}

// mirrorWebSocketNegotiation sets the client's extension response to exactly
// what the upstream accepted, undoing any edit made after the upstream answered.
// It returns the negotiated deflate configuration and the removed offers.
func mirrorWebSocketNegotiation(proxy *Proxy, res *http.Response) (*domain.WebSocketDeflate, []string) {
	removed, _ := res.Request.Context().Value(webSocketRemovedExtensionsKey{}).([]string)
	negotiation, ok := res.Request.Context().Value(webSocketNegotiationKey{}).(webSocketNegotiation)
	if !ok {
		return nil, removed
	}

	if !slices.Equal(res.Header.Values(marasiws.ExtensionsHeader), negotiation.accept) {
		var options []func(*domain.Log) error
		if requestID, ok := core.RequestIDFromContext(res.Request.Context()); ok {
			options = append(options, core.LogWithReqResID(requestID))
		}
		_ = proxy.WriteLog(
			"WARN",
			fmt.Sprintf(
				"Ignoring an edit to %s on the WebSocket handshake response: the client gets %q, as the upstream accepted",
				marasiws.ExtensionsHeader,
				negotiation.accept,
			),
			options...,
		)
	}
	res.Header.Del(marasiws.ExtensionsHeader)
	for _, value := range negotiation.accept {
		res.Header.Add(marasiws.ExtensionsHeader, value)
	}
	return negotiation.deflate, removed
}
