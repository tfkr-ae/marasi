package marasi

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/tfkr-ae/marasi/rawhttp"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// Launch sends a raw HTTP request through the proxy client.
// It is used for the launchpad functionality to replay and test requests.
//
// For normal HTTP, the response body is drained and closed before return.
// For WebSocket upgrades, Launch returns after the handshake and keeps the
// client body open until the peer closes, CloseWebSocket is used, or the
// proxy shuts down.
func (proxy *Proxy) Launch(raw string, launchpadID string, useHttps bool) error {
	updated, err := rawhttp.RecalculateContentLength([]byte(raw))
	if err != nil {
		return fmt.Errorf("recalculating content length : %w", err)
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(updated)))
	if err != nil {
		return fmt.Errorf("reading http request : %w", err)
	}

	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	if useHttps {
		scheme = "https"
	}
	host := req.Host
	if host == "" {
		return fmt.Errorf("host header not found or is empty")
	}

	req.RequestURI, req.URL.Scheme, req.URL.Host = "", scheme, host
	req.Header.Add("x-launchpad-id", launchpadID)

	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "")
	}

	if marasiws.IsUpgradeRequest(req) {
		req.Header.Set("x-marasi-metadata", `{"websocket.held_by":"launchpad"}`)
	}

	res, err := proxy.Client.Do(req)
	if err != nil {
		return fmt.Errorf("client doing request : %w", err)
	}

	if marasiws.IsUpgradeResponse(res) {
		proxy.holdLaunchpadWebSocket(res.Body)
		return nil
	}

	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

// holdLaunchpadWebSocket keeps a Launchpad WebSocket response body alive and drains it.
func (proxy *Proxy) holdLaunchpadWebSocket(body io.ReadCloser) {
	if body == nil {
		return
	}

	proxy.launchpadWSMu.Lock()
	if proxy.launchpadWS == nil {
		proxy.launchpadWS = make(map[io.Closer]struct{})
	}
	proxy.launchpadWS[body] = struct{}{}
	proxy.launchpadWSMu.Unlock()

	go func() {
		defer func() {
			proxy.launchpadWSMu.Lock()
			delete(proxy.launchpadWS, body)
			proxy.launchpadWSMu.Unlock()
			_ = body.Close()
		}()
		_, _ = io.Copy(io.Discard, body)
	}()
}

// closeLaunchpadWebSockets closes every held Launchpad WebSocket response body.
func (proxy *Proxy) closeLaunchpadWebSockets() {
	proxy.launchpadWSMu.Lock()
	bodies := make([]io.Closer, 0, len(proxy.launchpadWS))
	for body := range proxy.launchpadWS {
		bodies = append(bodies, body)
	}
	proxy.launchpadWS = make(map[io.Closer]struct{})
	proxy.launchpadWSMu.Unlock()

	for _, body := range bodies {
		_ = body.Close()
	}
}
