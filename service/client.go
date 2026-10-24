package service

import (
	"context"
	"net"
	"net/http"
)

// Endpoint is where a Client reaches a service instance's control listener.
// Callers build requests for http://marasi and leave the transport to the
// endpoint, so a new kind of endpoint changes only this file.
type Endpoint struct {
	socketPath string // the control listener's socket on this machine
}

// SocketEndpoint is the control listener at socketPath on this machine.
func SocketEndpoint(socketPath string) Endpoint {
	return Endpoint{socketPath: socketPath}
}

// dial opens a connection to the endpoint's control listener.
func (e Endpoint) dial(ctx context.Context) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", e.socketPath)
}

// Client calls a service instance's HTTP API at its endpoint.
type Client struct {
	http      *http.Client    // client that dials the endpoint
	transport *http.Transport // owned by the client; idle conns closed by Close
}

// NewEndpointClient creates a client for the service instance at endpoint.
func NewEndpointClient(endpoint Endpoint) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return endpoint.dial(ctx)
		},
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		transport: transport,
	}
}

// NewClient creates a client for the service instance at socketPath.
func NewClient(socketPath string) *Client {
	return NewEndpointClient(SocketEndpoint(socketPath))
}

// Do sends an HTTP request to the service instance. Redirects are not followed.
func (c *Client) Do(request *http.Request) (*http.Response, error) {
	return c.http.Do(request)
}

// Close closes idle connections owned by the client.
func (c *Client) Close() {
	c.transport.CloseIdleConnections()
}
