package websocket

import (
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// IsUpgradeRequest reports whether req is an HTTP WebSocket upgrade request.
func IsUpgradeRequest(req *http.Request) bool {
	if req == nil {
		return false
	}

	return httpguts.HeaderValuesContainsToken(
		req.Header.Values("Connection"),
		"upgrade",
	) && httpguts.HeaderValuesContainsToken(
		req.Header.Values("Upgrade"),
		"websocket",
	)
}

// IsUpgradeResponse reports whether res is a successful WebSocket upgrade response.
func IsUpgradeResponse(res *http.Response) bool {
	if res == nil || res.Request == nil {
		return false
	}

	if res.StatusCode != http.StatusSwitchingProtocols {
		return false
	}

	if !IsUpgradeRequest(res.Request) {
		return false
	}

	if !httpguts.HeaderValuesContainsToken(
		res.Header.Values("Connection"),
		"upgrade",
	) {
		return false
	}

	return httpguts.HeaderValuesContainsToken(
		res.Header.Values("Upgrade"),
		"websocket",
	)
}

// TransportFromRequest returns "ws" or "wss" for the request's transport.
func TransportFromRequest(req *http.Request) string {
	if req == nil {
		return "ws"
	}

	if req.TLS != nil {
		return "wss"
	}

	if req.URL != nil {
		if strings.EqualFold(req.URL.Scheme, "https") ||
			strings.EqualFold(req.URL.Scheme, "wss") {
			return "wss"
		}
	}

	return "ws"
}
