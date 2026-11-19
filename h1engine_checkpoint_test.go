package marasi

import (
	"net/http/httptest"
	"testing"
)

func TestRebuildWireRequest_Destination(t *testing.T) {
	// A request held inside a tunnel to tunnel.example:443.
	original := httptest.NewRequest("GET", "https://tunnel.example:443/held", nil)
	original.Host = "tunnel.example:443"

	t.Run("should keep the held request's destination when the edit changes only its Host", func(t *testing.T) {
		view, _, err := rebuildWireRequest([]byte("GET /held HTTP/1.1\r\nHost: evil.invalid\r\n\r\n"), original, &wireRequest{})
		if err != nil {
			t.Fatalf("rebuilding: %v", err)
		}
		if view.URL.Host != "tunnel.example:443" || view.Host != "evil.invalid" {
			t.Fatalf("wanted it sent to tunnel.example:443 with Host evil.invalid\ngot: sent to %q with Host %q", view.URL.Host, view.Host)
		}
	})

	t.Run("should send the edit where its request line names when the edit names a target", func(t *testing.T) {
		view, _, err := rebuildWireRequest([]byte("GET https://other.example/held HTTP/1.1\r\nHost: other.example\r\n\r\n"), original, &wireRequest{})
		if err != nil {
			t.Fatalf("rebuilding: %v", err)
		}
		if view.URL.Host != "other.example" {
			t.Fatalf("wanted it sent to other.example\ngot: %q", view.URL.Host)
		}
	})
}

func TestWithoutDefaultPort(t *testing.T) {
	for _, tc := range []struct{ authority, scheme, want string }{
		{"example.com:443", "https", "example.com"},
		{"example.com:80", "http", "example.com"},
		{"example.com:8443", "https", "example.com:8443"},
		{"example.com:443", "http", "example.com:443"},
		{"[::1]:443", "https", "[::1]"},
		{"", "https", ""},
	} {
		t.Run("should give "+tc.want+" for "+tc.scheme+" "+tc.authority, func(t *testing.T) {
			if got := withoutDefaultPort(tc.authority, tc.scheme); got != tc.want {
				t.Fatalf("wanted %q\ngot: %q", tc.want, got)
			}
		})
	}
}
