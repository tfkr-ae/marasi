package service

import (
	"context"
	"net"
	"net/http"
)

// Client calls a service instance's HTTP API over its Unix socket.
type Client struct {
	http      *http.Client
	transport *http.Transport
}

// NewClient creates a client for the service instance at socketPath.
func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
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

// Do sends an HTTP request to the service instance.
func (c *Client) Do(request *http.Request) (*http.Response, error) {
	return c.http.Do(request)
}

// Close closes idle connections owned by the client.
func (c *Client) Close() {
	c.transport.CloseIdleConnections()
}
