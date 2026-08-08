package websocket

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"testing"
)

func TestHandshake_IsUpgradeRequest(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{
			name: "nil request",
			want: false,
		},
		{
			name: "websocket upgrade",
			req: &http.Request{Header: http.Header{
				"Connection": []string{"Upgrade"},
				"Upgrade":    []string{"websocket"},
			}},
			want: true,
		},
		{
			name: "case insensitive values",
			req: &http.Request{Header: http.Header{
				"Connection": []string{"uPgRaDe"},
				"Upgrade":    []string{"WebSocket"},
			}},
			want: true,
		},
		{
			name: "connection contains multiple tokens",
			req: &http.Request{Header: http.Header{
				"Connection": []string{"keep-alive, Upgrade"},
				"Upgrade":    []string{"websocket"},
			}},
			want: true,
		},
		{
			name: "connection contains repeated values",
			req: &http.Request{Header: http.Header{
				"Connection": []string{"keep-alive", "Upgrade"},
				"Upgrade":    []string{"websocket"},
			}},
			want: true,
		},
		{
			name: "missing connection header",
			req: &http.Request{Header: http.Header{
				"Upgrade": []string{"websocket"},
			}},
			want: false,
		},
		{
			name: "connection does not contain upgrade",
			req: &http.Request{Header: http.Header{
				"Connection": []string{"keep-alive"},
				"Upgrade":    []string{"websocket"},
			}},
			want: false,
		},
		{
			name: "missing upgrade header",
			req: &http.Request{Header: http.Header{
				"Connection": []string{"Upgrade"},
			}},
			want: false,
		},
		{
			name: "upgrade is not websocket",
			req: &http.Request{Header: http.Header{
				"Connection": []string{"Upgrade"},
				"Upgrade":    []string{"h2c"},
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsUpgradeRequest(tt.req)
			if got != tt.want {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}

func TestHandshake_IsUpgradeResponse(t *testing.T) {
	upgradeRequest := &http.Request{Header: http.Header{
		"Connection": []string{"Upgrade"},
		"Upgrade":    []string{"websocket"},
	}}
	nonUpgradeRequest := &http.Request{Header: http.Header{}}

	tests := []struct {
		name string
		res  *http.Response
		want bool
	}{
		{
			name: "nil response",
			want: false,
		},
		{
			name: "nil request",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
			},
			want: false,
		},
		{
			name: "websocket upgrade",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Request:    upgradeRequest,
				Header: http.Header{
					"Connection": []string{"Upgrade"},
					"Upgrade":    []string{"websocket"},
				},
			},
			want: true,
		},
		{
			name: "case insensitive values with multiple tokens",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Request:    upgradeRequest,
				Header: http.Header{
					"Connection": []string{"keep-alive, uPgRaDe"},
					"Upgrade":    []string{"WebSocket"},
				},
			},
			want: true,
		},
		{
			name: "status is not switching protocols",
			res: &http.Response{
				StatusCode: http.StatusOK,
				Request:    upgradeRequest,
				Header: http.Header{
					"Connection": []string{"Upgrade"},
					"Upgrade":    []string{"websocket"},
				},
			},
			want: false,
		},
		{
			name: "request is not an upgrade",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Request:    nonUpgradeRequest,
				Header: http.Header{
					"Connection": []string{"Upgrade"},
					"Upgrade":    []string{"websocket"},
				},
			},
			want: false,
		},
		{
			name: "missing connection header",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Request:    upgradeRequest,
				Header: http.Header{
					"Upgrade": []string{"websocket"},
				},
			},
			want: false,
		},
		{
			name: "connection does not contain upgrade",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Request:    upgradeRequest,
				Header: http.Header{
					"Connection": []string{"keep-alive"},
					"Upgrade":    []string{"websocket"},
				},
			},
			want: false,
		},
		{
			name: "missing upgrade header",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Request:    upgradeRequest,
				Header: http.Header{
					"Connection": []string{"Upgrade"},
				},
			},
			want: false,
		},
		{
			name: "upgrade is not websocket",
			res: &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Request:    upgradeRequest,
				Header: http.Header{
					"Connection": []string{"Upgrade"},
					"Upgrade":    []string{"h2c"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsUpgradeResponse(tt.res)
			if got != tt.want {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}

func TestHandshake_TransportFromRequest(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		want string
	}{
		{
			name: "nil request",
			want: "ws",
		},
		{
			name: "nil URL",
			req:  &http.Request{},
			want: "ws",
		},
		{
			name: "HTTP scheme",
			req: &http.Request{
				URL: &url.URL{Scheme: "http"},
			},
			want: "ws",
		},
		{
			name: "WS scheme",
			req: &http.Request{
				URL: &url.URL{Scheme: "ws"},
			},
			want: "ws",
		},
		{
			name: "HTTPS scheme",
			req: &http.Request{
				URL: &url.URL{Scheme: "https"},
			},
			want: "wss",
		},
		{
			name: "WSS scheme",
			req: &http.Request{
				URL: &url.URL{Scheme: "wss"},
			},
			want: "wss",
		},
		{
			name: "case insensitive secure scheme",
			req: &http.Request{
				URL: &url.URL{Scheme: "WSS"},
			},
			want: "wss",
		},
		{
			name: "TLS connection",
			req: &http.Request{
				URL: &url.URL{Scheme: "http"},
				TLS: &tls.ConnectionState{},
			},
			want: "wss",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TransportFromRequest(tt.req)
			if got != tt.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", tt.want, got)
			}
		})
	}
}
