package domain

import (
	"slices"
	"testing"
)

func TestSitemapPath(t *testing.T) {
	tests := []struct {
		name   string
		scheme string
		host   string
		path   string
		want   []string
	}{
		{"a decimal id collapses", "https", "api.x.com", "/users/42", []string{"https://api.x.com", "https://api.x.com/users", "https://api.x.com/users/{int}"}},
		{"a uuid collapses", "https", "api.x.com", "/orders/0193a1b2-7c3d-7e4f-8a9b-0c1d2e3f4a5b/items", []string{"https://api.x.com", "https://api.x.com/orders", "https://api.x.com/orders/{uuid}", "https://api.x.com/orders/{uuid}/items"}},
		{"an md5 collapses", "https", "cdn.x.com", "/assets/d41d8cd98f00b204e9800998ecf8427e", []string{"https://cdn.x.com", "https://cdn.x.com/assets", "https://cdn.x.com/assets/{hash}"}},
		{"a sha1 and a sha256 collapse", "https", "x.com", "/da39a3ee5e6b4b0d3255bfef95601890afd80709/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", []string{"https://x.com", "https://x.com/{hash}", "https://x.com/{hash}/{hash}"}},
		{"a version word stays", "https", "api.x.com", "/v1", []string{"https://api.x.com", "https://api.x.com/v1"}},
		{"a year is an int", "https", "x.com", "/2024", []string{"https://x.com", "https://x.com/{int}"}},
		{"a word with digits stays", "https", "x.com", "/getOrderHistory2024", []string{"https://x.com", "https://x.com/getOrderHistory2024"}},
		{"an escaped slash stays inside its segment", "https", "x.com", "/files/a%2Fb/raw", []string{"https://x.com", "https://x.com/files", "https://x.com/files/a%2Fb", "https://x.com/files/a%2Fb/raw"}},
		{"literal braces are escaped", "https", "x.com", "/{int}/a{b}", []string{"https://x.com", "https://x.com/%7Bint%7D", "https://x.com/%7Bint%7D/a%7Bb%7D"}},
		{"empty segments are dropped", "https", "x.com", "//a//b", []string{"https://x.com", "https://x.com/a", "https://x.com/a/b"}},
		{"a trailing slash is not a node", "https", "x.com", "/users/", []string{"https://x.com", "https://x.com/users"}},
		{"the root is the origin", "http", "x.com", "/", []string{"http://x.com"}},
		{"the query is dropped", "https", "x.com", "/search?q=1/2", []string{"https://x.com", "https://x.com/search"}},
		{"an origin keeps its port and is lowercased", "HTTPS", "API.X.com:8443", "/Users", []string{"https://api.x.com:8443", "https://api.x.com:8443/Users"}},
		{"a hex string of another length stays", "https", "x.com", "/abcdef0123", []string{"https://x.com", "https://x.com/abcdef0123"}},
		{"an escaped uuid collapses", "https", "x.com", "/%30193a1b2-7c3d-7e4f-8a9b-0c1d2e3f4a5b", []string{"https://x.com", "https://x.com/{uuid}"}},
		{"a stray percent is escaped", "https", "x.com", "/100%", []string{"https://x.com", "https://x.com/100%25"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := SitemapPath(tt.scheme, tt.host, tt.path)
			got := make([]string, len(nodes))
			for i, node := range nodes {
				got[i] = node.Name
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", tt.want, got)
			}
		})
	}
}

func TestSitemapPathSegments(t *testing.T) {
	nodes := SitemapPath("https", "x.com:8080", "/users/42")
	got := []string{nodes[0].Segment, nodes[1].Segment, nodes[2].Segment}
	want := []string{"https://x.com:8080", "users", "{int}"}
	if !slices.Equal(got, want) {
		t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
	}
}
