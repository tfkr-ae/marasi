package compass

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScope_HostRulesMatchTheDestination(t *testing.T) {
	// A request sent to example.com with a Host a tester changed.
	spoofed := func() *http.Request {
		req := httptest.NewRequest("GET", "https://example.com/reset", nil)
		req.Host = "evil.invalid"
		return req
	}
	scope := NewScope(false)
	if err := scope.AddRule(`^example\.com$`, "host", false); err != nil {
		t.Fatalf("adding rule: %v", err)
	}

	t.Run("should keep a request in scope when only its Host names somewhere else", func(t *testing.T) {
		if !scope.Matches(spoofed()) {
			t.Fatal("wanted the request to example.com in scope")
		}
		if inScope, rule := scope.MatchesWithRule(spoofed()); !inScope || rule == nil {
			t.Fatalf("wanted MatchesWithRule to find the example.com rule\ngot: %v, %v", inScope, rule)
		}
	})

	t.Run("should judge a response by its request's destination", func(t *testing.T) {
		if !scope.Matches(&http.Response{Request: spoofed()}) {
			t.Fatal("wanted the response from example.com in scope")
		}
	})

	t.Run("should fall back to the Host when the URL names no host", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/reset", nil)
		req.URL.Host, req.Host = "", "example.com"
		if !scope.Matches(req) {
			t.Fatal("wanted the request in scope by its Host")
		}
	})
}
