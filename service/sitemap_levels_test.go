package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// sitemapLevelsBody is a POST /traffic/sitemap/levels body.
type sitemapLevelsBody struct {
	Levels []struct {
		Node  string `json:"node"`
		Items []struct {
			Name           string `json:"name"`
			Segment        string `json:"segment"`
			Pairs          int64  `json:"pairs"`
			Errors         int64  `json:"errors"`
			HasChildren    bool   `json:"has_children"`
			Matching       *int64 `json:"matching"`
			MatchingErrors *int64 `json:"matching_errors"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
		Missing    bool    `json:"missing"`
	} `json:"levels"`
	Sitemap struct {
		Complete bool `json:"complete"`
	} `json:"sitemap"`
}

func (p *trafficListProject) sitemapLevels(t *testing.T, body string) (*httptest.ResponseRecorder, sitemapLevelsBody) {
	t.Helper()
	response := httptest.NewRecorder()
	p.server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/traffic/sitemap/levels", strings.NewReader(body)))
	var decoded sitemapLevelsBody
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
	}
	return response, decoded
}

func formatSitemapLevels(body sitemapLevelsBody) string {
	var lines []string
	for _, level := range body.Levels {
		header := "# " + level.Node
		if level.Missing {
			header += " missing"
		}
		lines = append(lines, header)
		for _, item := range level.Items {
			matching := "-"
			if item.Matching != nil && item.MatchingErrors != nil {
				matching = fmt.Sprintf("%d/%d", *item.Matching, *item.MatchingErrors)
			}
			line := fmt.Sprintf("%s %s %d/%d %s", item.Name, item.Segment, item.Pairs, item.Errors, matching)
			if item.HasChildren {
				line += "+"
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func TestTrafficSitemapLevels(t *testing.T) {
	p := setupTrafficListProject(t)
	p.capture(t, "api.x.com", "/users/42", "application/json", 200)
	p.capture(t, "api.x.com", "/users/43?full=1", "application/json", 502)
	p.capture(t, "api.x.com", "/logo.png", "image/png", 500)
	p.capture(t, "cdn.x.com", "/app.js", "application/javascript", 200)
	_, list := p.get(t, "/traffic")
	asOf := list.AsOf.String()
	p.capture(t, "api.x.com", "/users/44", "application/json", 503)

	t.Run("should read several levels in one call, with all-traffic and matching counts", func(t *testing.T) {
		response, body := p.sitemapLevels(t, `{"nodes": ["", "https://api.x.com", "https://nowhere.test"], "scope": "all", "exclusion": ["image/*"],
			"selection": {"query": "status_code >= 500", "as_of": "`+asOf+`"}}`)
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		want := strings.Join([]string{
			"# ",
			"https://api.x.com https://api.x.com 3/2 2/2+",
			"https://cdn.x.com https://cdn.x.com 1/0 0/0+",
			"# https://api.x.com",
			"https://api.x.com/users users 3/2 1/1+",
			"# https://nowhere.test missing",
		}, "\n")
		if got := formatSitemapLevels(body); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if !body.Sitemap.Complete {
			t.Fatalf("\nwanted:\na complete sitemap\ngot:\n%s", response.Body)
		}
	})

	t.Run("should list only the matching children in the matching scope", func(t *testing.T) {
		response, body := p.sitemapLevels(t, `{"nodes": ["https://api.x.com"], "scope": "matching",
			"selection": {"query": "status_code >= 500", "exclusion": ["image/*"], "as_of": "`+asOf+`"}}`)
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		want := "# https://api.x.com\nhttps://api.x.com/users users 3/2 1/1+"
		if got := formatSitemapLevels(body); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should page a level from its cursor", func(t *testing.T) {
		_, first := p.sitemapLevels(t, `{"nodes": [""], "limit": 1}`)
		if len(first.Levels) != 1 || first.Levels[0].NextCursor == nil {
			t.Fatalf("\nwanted:\none level with a next cursor\ngot:\n%+v", first)
		}
		cursor, _ := json.Marshal(*first.Levels[0].NextCursor)
		_, second := p.sitemapLevels(t, `{"nodes": [""], "limit": 1, "cursors": {"": `+string(cursor)+`}}`)
		if got, want := formatSitemapLevels(second), "# \nhttps://cdn.x.com https://cdn.x.com 1/0 -+"; got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	for _, tt := range []struct {
		name  string
		body  string
		code  int
		error string
	}{
		{"a malformed body", `{"nodes": [""]`, http.StatusBadRequest, "bad_request"},
		{"an unknown field", `{"nodes": [""], "node": ""}`, http.StatusBadRequest, "bad_request"},
		{"no nodes", `{"nodes": []}`, http.StatusBadRequest, "bad_request"},
		{"an unknown scope", `{"nodes": [""], "scope": "some"}`, http.StatusBadRequest, "bad_request"},
		{"the matching scope without a selection", `{"nodes": [""], "scope": "matching"}`, http.StatusBadRequest, "bad_request"},
		{"a selection without as_of", `{"nodes": [""], "selection": {"query": "a"}}`, http.StatusBadRequest, "bad_request"},
		{"a limit over 500", `{"nodes": [""], "limit": 501}`, http.StatusBadRequest, "bad_request"},
		{"an invalid exclusion", `{"nodes": [""], "exclusion": ["im*ge/png"]}`, http.StatusBadRequest, "invalid_query"},
		{"an invalid query", `{"nodes": [""], "selection": {"query": "host = ", "as_of": "` + uuid.Nil.String() + `"}}`, http.StatusBadRequest, "invalid_query"},
		{"an invalid cursor", `{"nodes": [""], "cursors": {"": "nope"}}`, http.StatusBadRequest, "invalid_cursor"},
	} {
		t.Run("should reject "+tt.name, func(t *testing.T) {
			response, _ := p.sitemapLevels(t, tt.body)
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &body)
			if response.Code != tt.code || body.Error != tt.error {
				t.Fatalf("\nwanted:\n%d %s\ngot:\n%d %s", tt.code, tt.error, response.Code, response.Body)
			}
		})
	}
}
