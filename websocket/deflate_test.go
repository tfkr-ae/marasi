package websocket

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

func TestDeflate_FilterExtensionOffers(t *testing.T) {
	tests := []struct {
		name        string
		header      []string
		wantHeader  []string
		wantRemoved []string
	}{
		{
			name: "no extension header",
		},
		{
			name:       "single deflate offer is kept verbatim",
			header:     []string{"permessage-deflate; client_max_window_bits"},
			wantHeader: []string{"permessage-deflate; client_max_window_bits"},
		},
		{
			name: "multiple deflate offers keep their order",
			header: []string{
				"permessage-deflate; server_max_window_bits=10; client_max_window_bits, permessage-deflate",
			},
			wantHeader: []string{
				"permessage-deflate; server_max_window_bits=10; client_max_window_bits, permessage-deflate",
			},
		},
		{
			name: "unknown extension is removed and recorded",
			header: []string{
				"x-webkit-deflate-frame, permessage-deflate; client_no_context_takeover",
			},
			wantHeader:  []string{"permessage-deflate; client_no_context_takeover"},
			wantRemoved: []string{"x-webkit-deflate-frame"},
		},
		{
			name: "offers across header lines and quoted commas",
			header: []string{
				`foo; note="a, b", permessage-deflate`,
				"PerMessage-Deflate; server_no_context_takeover",
				"bar",
			},
			wantHeader:  []string{"permessage-deflate, PerMessage-Deflate; server_no_context_takeover"},
			wantRemoved: []string{"foo", "bar"},
		},
		{
			name:        "only unknown extensions removes the header",
			header:      []string{"foo, bar; x=1, foo"},
			wantRemoved: []string{"foo", "bar"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for _, value := range tt.header {
				header.Add("Sec-WebSocket-Extensions", value)
			}

			removed := FilterExtensionOffers(header)

			if got := header.Values("Sec-WebSocket-Extensions"); !reflect.DeepEqual(got, tt.wantHeader) {
				t.Fatalf("\nwanted header:\n%q\ngot:\n%q", tt.wantHeader, got)
			}
			if !reflect.DeepEqual(removed, tt.wantRemoved) {
				t.Fatalf("\nwanted removed:\n%q\ngot:\n%q", tt.wantRemoved, removed)
			}
		})
	}
}

func TestDeflate_NegotiateDeflate(t *testing.T) {
	defaults := domain.WebSocketDeflate{ServerMaxWindowBits: 15, ClientMaxWindowBits: 15}
	with := func(change func(*domain.WebSocketDeflate)) *domain.WebSocketDeflate {
		params := defaults
		change(&params)
		return &params
	}

	accepted := []struct {
		name     string
		offers   []string
		response []string
		want     *domain.WebSocketDeflate
	}{
		{
			name:   "upstream declines by omitting the header",
			offers: []string{"permessage-deflate"},
			want:   nil,
		},
		{
			name: "no offer and no response",
			want: nil,
		},
		{
			name:     "plain offer and plain accept",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate"},
			want:     &defaults,
		},
		{
			name:     "server_no_context_takeover may be added by the upstream",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; server_no_context_takeover"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ServerNoContextTakeover = true }),
		},
		{
			name:     "server_no_context_takeover offered and accepted",
			offers:   []string{"permessage-deflate; server_no_context_takeover"},
			response: []string{"permessage-deflate; server_no_context_takeover"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ServerNoContextTakeover = true }),
		},
		{
			name:     "client_no_context_takeover may be added by the upstream",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; client_no_context_takeover"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ClientNoContextTakeover = true }),
		},
		{
			name:     "server_max_window_bits below the offered limit",
			offers:   []string{"permessage-deflate; server_max_window_bits=12"},
			response: []string{"permessage-deflate; server_max_window_bits=10"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ServerMaxWindowBits = 10 }),
		},
		{
			name:     "server_max_window_bits may be added by the upstream",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; server_max_window_bits=8"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ServerMaxWindowBits = 8 }),
		},
		{
			name:     "client_max_window_bits offered without a value",
			offers:   []string{"permessage-deflate; client_max_window_bits"},
			response: []string{"permessage-deflate; client_max_window_bits=9"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ClientMaxWindowBits = 9 }),
		},
		{
			name:     "client_max_window_bits offered with a value and accepted lower",
			offers:   []string{"permessage-deflate; client_max_window_bits=10"},
			response: []string{"permessage-deflate; client_max_window_bits=9"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ClientMaxWindowBits = 9 }),
		},
		{
			name:     "client_max_window_bits offered but not limited by the upstream",
			offers:   []string{"permessage-deflate; client_max_window_bits"},
			response: []string{"permessage-deflate"},
			want:     &defaults,
		},
		{
			name:     "quoted parameter value",
			offers:   []string{"permessage-deflate"},
			response: []string{`permessage-deflate; server_max_window_bits="11"`},
			want:     with(func(p *domain.WebSocketDeflate) { p.ServerMaxWindowBits = 11 }),
		},
		{
			name:     "every parameter at once",
			offers:   []string{"permessage-deflate; server_no_context_takeover; client_no_context_takeover; server_max_window_bits=10; client_max_window_bits=12"},
			response: []string{"permessage-deflate; server_no_context_takeover; client_no_context_takeover; server_max_window_bits=10; client_max_window_bits=12"},
			want: &domain.WebSocketDeflate{
				ServerNoContextTakeover: true,
				ClientNoContextTakeover: true,
				ServerMaxWindowBits:     10,
				ClientMaxWindowBits:     12,
			},
		},
		{
			name:     "multiple offers: the response matches the fallback offer",
			offers:   []string{"permessage-deflate; server_max_window_bits=10, permessage-deflate"},
			response: []string{"permessage-deflate; server_max_window_bits=12"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ServerMaxWindowBits = 12 }),
		},
		{
			name:     "multiple offers: client_max_window_bits only in the second offer",
			offers:   []string{"permessage-deflate", "permessage-deflate; client_max_window_bits"},
			response: []string{"permessage-deflate; client_max_window_bits=10"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ClientMaxWindowBits = 10 }),
		},
		{
			name:     "empty list elements are ignored",
			offers:   []string{"permessage-deflate"},
			response: []string{"", "permessage-deflate, "},
			want:     &defaults,
		},
		{
			name:     "names are case-insensitive",
			offers:   []string{"permessage-deflate"},
			response: []string{"PerMessage-Deflate; Server_No_Context_Takeover"},
			want:     with(func(p *domain.WebSocketDeflate) { p.ServerNoContextTakeover = true }),
		},
	}

	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NegotiateDeflate(tt.offers, tt.response)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", tt.want, got)
			}
		})
	}

	rejected := []struct {
		name     string
		offers   []string
		response []string
	}{
		{
			name:     "accept without an offer",
			response: []string{"permessage-deflate"},
		},
		{
			name:     "unknown extension in the response",
			offers:   []string{"permessage-deflate"},
			response: []string{"x-webkit-deflate-frame"},
		},
		{
			name:     "two accepted extensions",
			offers:   []string{"permessage-deflate, permessage-deflate"},
			response: []string{"permessage-deflate", "permessage-deflate"},
		},
		{
			name:     "unknown parameter",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; mystery"},
		},
		{
			name:     "duplicate parameter",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; server_no_context_takeover; server_no_context_takeover"},
		},
		{
			name:     "no_context_takeover with a value",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; client_no_context_takeover=1"},
		},
		{
			name:     "server_max_window_bits without a value",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; server_max_window_bits"},
		},
		{
			name:     "server_max_window_bits below 8",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; server_max_window_bits=7"},
		},
		{
			name:     "server_max_window_bits above 15",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; server_max_window_bits=16"},
		},
		{
			name:     "server_max_window_bits with a leading zero",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; server_max_window_bits=09"},
		},
		{
			name:     "server_max_window_bits above the offered limit",
			offers:   []string{"permessage-deflate; server_max_window_bits=10"},
			response: []string{"permessage-deflate; server_max_window_bits=12"},
		},
		{
			name:     "server_max_window_bits offered but missing from the response",
			offers:   []string{"permessage-deflate; server_max_window_bits=10"},
			response: []string{"permessage-deflate"},
		},
		{
			name:     "server_no_context_takeover offered but missing from the response",
			offers:   []string{"permessage-deflate; server_no_context_takeover"},
			response: []string{"permessage-deflate"},
		},
		{
			name:     "client_max_window_bits not offered",
			offers:   []string{"permessage-deflate"},
			response: []string{"permessage-deflate; client_max_window_bits=10"},
		},
		{
			name:     "client_max_window_bits without a value in the response",
			offers:   []string{"permessage-deflate; client_max_window_bits"},
			response: []string{"permessage-deflate; client_max_window_bits"},
		},
		{
			name:     "client_max_window_bits above the offered limit",
			offers:   []string{"permessage-deflate; client_max_window_bits=10"},
			response: []string{"permessage-deflate; client_max_window_bits=12"},
		},
	}

	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			got, err := NegotiateDeflate(tt.offers, tt.response)
			if !errors.Is(err, ErrDeflateNegotiation) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v (params %+v)", ErrDeflateNegotiation, err, got)
			}
		})
	}
}
