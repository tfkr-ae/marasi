package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrafficContentTypes(t *testing.T) {
	t.Run("should count pairs per content type with the sitemap's completeness", func(t *testing.T) {
		p := setupTrafficListProject(t, "text/html", "application/json", "text/html", "")
		p.capture(t, "api.x.com", "/users/42", "application/json", 200)
		p.capture(t, "api.x.com", "/logo.png", "image/png", 200)

		response := httptest.NewRecorder()
		p.server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/traffic/content-types", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		var got, want any
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		_ = json.Unmarshal([]byte(`{
			"items": [
				{"content_type": "application/json", "count": 2},
				{"content_type": "text/html", "count": 2},
				{"content_type": "", "count": 1},
				{"content_type": "image/png", "count": 1}
			],
			"sitemap": {"complete": true}
		}`), &want)
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, want); gotJSON != wantJSON {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantJSON, gotJSON)
		}
	})

	t.Run("should list no content types in an empty project", func(t *testing.T) {
		p := setupTrafficListProject(t)

		response := httptest.NewRecorder()
		p.server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/traffic/content-types", nil))
		if want := `{"items":[],"sitemap":{"complete":true}}`; response.Code != http.StatusOK || response.Body.String() != want+"\n" {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
