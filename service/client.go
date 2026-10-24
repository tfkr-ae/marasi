package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrTokenRejected reports that a control listener's network address
// answered 401: the control token sent with the request is missing or wrong.
var ErrTokenRejected = errors.New("control token rejected")

// Endpoint is where a Client reaches a service instance's control listener.
// Callers build requests for http://marasi and leave the transport to the
// endpoint, so a new kind of endpoint changes only this file.
type Endpoint struct {
	socketPath string   // the control listener's socket on this machine
	remote     *url.URL // the control listener's network address, nil for a socket
	token      string   // control token sent to remote as a bearer token
}

// SocketEndpoint is the control listener at socketPath on this machine.
func SocketEndpoint(socketPath string) Endpoint {
	return Endpoint{socketPath: socketPath}
}

// RemoteEndpoint is the control listener at the network address rawURL,
// reached with token. rawURL is an http or https URL; a path in it prefixes
// every request path, for a reverse proxy that serves the address under one.
func RemoteEndpoint(rawURL, token string) (Endpoint, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Endpoint{}, fmt.Errorf("parsing remote URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Endpoint{}, fmt.Errorf("remote URL %q must start with http:// or https://", rawURL)
	}
	if parsed.Host == "" {
		return Endpoint{}, fmt.Errorf("remote URL %q has no host", rawURL)
	}
	// The control token is the only credential (docs/adr/2026-10-24-the-control-listener-network-address-is-guarded-by-a-token-alone.md),
	// and a query would be sent with every request, so neither belongs in the URL.
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Endpoint{}, fmt.Errorf("remote URL %q must not have a user, query, or fragment", rawURL)
	}
	if token == "" {
		return Endpoint{}, errors.New("control token is empty")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""
	return Endpoint{remote: parsed, token: token}, nil
}

// Remote reports whether the endpoint is a network address rather than a
// socket on this machine.
func (e Endpoint) Remote() bool {
	return e.remote != nil
}

// String names the endpoint in messages: the socket path or the remote URL.
func (e Endpoint) String() string {
	if e.remote != nil {
		return e.remote.String()
	}
	return e.socketPath
}

// dial opens a connection to the endpoint's socket.
func (e Endpoint) dial(ctx context.Context) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", e.socketPath)
}

// Accepts reports whether the endpoint accepts a connection now: its socket,
// or the host and port of its network address. A stopped instance's endpoint
// refuses, so a caller polls it to learn when the instance is gone.
func (e Endpoint) Accepts(ctx context.Context) bool {
	network, address := "unix", e.socketPath
	if e.remote != nil {
		port := e.remote.Port()
		if port == "" {
			port = "80"
			if e.remote.Scheme == "https" {
				port = "443"
			}
		}
		network, address = "tcp", net.JoinHostPort(e.remote.Hostname(), port)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// remoteTransport sends each request to a network address: it rewrites the
// http://marasi URL to the address, so the Host header names the address the
// instance checks against, and adds the bearer token.
type remoteTransport struct {
	remote *url.URL
	token  string
	next   http.RoundTripper
}

func (t remoteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	outbound := request.Clone(request.Context())
	target := *t.remote
	target.Path = t.remote.Path + request.URL.Path
	target.RawPath = t.remote.EscapedPath() + request.URL.EscapedPath()
	target.RawQuery = request.URL.RawQuery
	outbound.URL = &target
	outbound.Host = ""
	outbound.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(outbound)
}

// Client calls a service instance's HTTP API at its endpoint.
type Client struct {
	http      *http.Client    // client that reaches the endpoint
	transport *http.Transport // owned by the client; idle conns closed by Close
	endpoint  Endpoint
}

// NewEndpointClient creates a client for the service instance at endpoint.
func NewEndpointClient(endpoint Endpoint) *Client {
	transport := &http.Transport{}
	var roundTripper http.RoundTripper = transport
	if endpoint.Remote() {
		roundTripper = remoteTransport{remote: endpoint.remote, token: endpoint.token, next: transport}
	} else {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return endpoint.dial(ctx)
		}
	}
	return &Client{
		http: &http.Client{
			Transport: roundTripper,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		transport: transport,
		endpoint:  endpoint,
	}
}

// NewClient creates a client for the service instance at socketPath.
func NewClient(socketPath string) *Client {
	return NewEndpointClient(SocketEndpoint(socketPath))
}

// Do sends an HTTP request to the service instance. Redirects are not
// followed. A 401 from a network address is returned as ErrTokenRejected,
// since every caller would otherwise read it as a refusal of the command.
func (c *Client) Do(request *http.Request) (*http.Response, error) {
	response, err := c.http.Do(request)
	if err != nil || response.StatusCode != http.StatusUnauthorized || !c.endpoint.Remote() {
		return response, err
	}
	response.Body.Close()
	return nil, fmt.Errorf("%w by %s", ErrTokenRejected, c.endpoint)
}

// Close closes idle connections owned by the client.
func (c *Client) Close() {
	c.transport.CloseIdleConnections()
}
