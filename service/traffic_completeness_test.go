package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestTrafficReadsReportCompleteness(t *testing.T) {
	p := setupTrafficListProject(t, "text/html")
	launchpadID, err := p.repo.CreateLaunchpad("completeness", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.repo.LinkRequestToLaunchpad(p.ids[0], launchpadID); err != nil {
		t.Fatal(err)
	}
	asOf := p.ids[0].String()
	sitemapQuery := url.QueryEscape(`sitemap = "https://example.com"`)

	tests := []struct {
		name string
		path string
		want map[string]any
	}{
		{"/traffic/count with the sitemap field", "/traffic/count?q=" + sitemapQuery,
			map[string]any{"index": map[string]any{"complete": true}, "sitemap": map[string]any{"complete": true}}},
		{"/traffic/count without the sitemap field", "/traffic/count",
			map[string]any{"index": map[string]any{"complete": true}}},
		{"query-mode /traffic/sitemap with a text term", "/traffic/sitemap?q=" + url.QueryEscape(`"example"`) + "&as_of=" + asOf,
			map[string]any{"index": map[string]any{"complete": true}, "sitemap": map[string]any{"complete": true}}},
		{"query-mode /traffic/sitemap without a text term", "/traffic/sitemap?q=" + url.QueryEscape(`method = "GET"`) + "&as_of=" + asOf,
			map[string]any{"sitemap": map[string]any{"complete": true}}},
		{"/launchpad/{id} with the sitemap field", "/launchpad/" + launchpadID.String() + "?q=" + sitemapQuery,
			map[string]any{"index": map[string]any{"complete": true}, "sitemap": map[string]any{"complete": true}}},
		{"/launchpad/{id} without the sitemap field", "/launchpad/" + launchpadID.String(),
			map[string]any{"index": map[string]any{"complete": true}}},
	}
	for _, tt := range tests {
		t.Run("should report completeness on "+tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			p.server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			got := map[string]any{}
			for _, key := range []string{"index", "sitemap"} {
				if value, ok := body[key]; ok {
					got[key] = value
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}
