package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// sitemapBody is a GET /traffic/sitemap body.
type sitemapBody struct {
	Items      []domain.SitemapChild `json:"items"`
	NextCursor *string               `json:"next_cursor"`
	Sitemap    struct {
		Complete bool `json:"complete"`
	} `json:"sitemap"`
}

// capture writes one responded pair to the project.
func (p *trafficListProject) capture(t *testing.T, host, path, contentType string, status int) {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	now := time.Now()
	if err := p.repo.InsertRequest(&domain.ProxyRequest{ID: id, Scheme: "https", Method: "GET", Host: host, Path: path, Raw: []byte("GET / HTTP/1.1\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: now}); err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	if err := p.repo.InsertResponse(&domain.ProxyResponse{ID: id, Status: "x", StatusCode: status, ContentType: contentType, Raw: []byte{}, Metadata: map[string]any{}, RespondedAt: now}); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
}

func (p *trafficListProject) sitemap(t *testing.T, query string) (*httptest.ResponseRecorder, sitemapBody) {
	t.Helper()
	response := httptest.NewRecorder()
	p.server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/traffic/sitemap"+query, nil))
	var body sitemapBody
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
	}
	return response, body
}

func formatSitemapItems(items []domain.SitemapChild) string {
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = item.Name + " " + item.Segment + " " + strings.Repeat("+", int(item.Pairs)) + strings.Repeat("!", int(item.Errors))
		if item.HasChildren {
			parts[i] += " children"
		}
	}
	return strings.Join(parts, "\n")
}

func TestTrafficSitemap(t *testing.T) {
	p := setupTrafficListProject(t)
	p.capture(t, "api.x.com", "/users/42", "application/json", 200)
	p.capture(t, "api.x.com", "/users/43?full=1", "application/json", 502)
	p.capture(t, "api.x.com", "/logo.png", "image/png", 200)
	p.capture(t, "cdn.x.com", "/app.js", "application/javascript", 200)

	t.Run("should list the origins with their counts and the sitemap's completeness", func(t *testing.T) {
		response, body := p.sitemap(t, "")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		want := "https://api.x.com https://api.x.com +++! children\nhttps://cdn.x.com https://cdn.x.com + children"
		if got := formatSitemapItems(body.Items); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if !body.Sitemap.Complete || body.NextCursor != nil {
			t.Fatalf("\nwanted:\na complete sitemap and no next page\ngot:\n%s", response.Body)
		}
	})

	t.Run("should list a node's children without the excluded types", func(t *testing.T) {
		response, body := p.sitemap(t, "?node="+url.QueryEscape("https://api.x.com")+"&exclude=image/*")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		want := "https://api.x.com/users users ++! children"
		if got := formatSitemapItems(body.Items); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should page with a cursor", func(t *testing.T) {
		_, first := p.sitemap(t, "?limit=1")
		if first.NextCursor == nil || len(first.Items) != 1 {
			t.Fatalf("\nwanted:\none origin and a next cursor\ngot:\n%+v", first)
		}
		_, second := p.sitemap(t, "?limit=1&cursor="+url.QueryEscape(*first.NextCursor))
		if got := formatSitemapItems(second.Items); got != "https://cdn.x.com https://cdn.x.com + children" || second.NextCursor != nil {
			t.Fatalf("\nwanted:\nthe second origin and no next cursor\ngot:\n%s %v", got, second.NextCursor)
		}
	})

	for _, tt := range []struct {
		name  string
		query string
		code  int
		error string
	}{
		{"an unknown node", "?node=" + url.QueryEscape("https://nowhere.com"), http.StatusNotFound, "not_found"},
		{"an invalid exclusion", "?exclude=im*ge/png", http.StatusBadRequest, "invalid_query"},
		{"an invalid cursor", "?cursor=nope", http.StatusBadRequest, "invalid_cursor"},
		{"a limit over 500", "?limit=501", http.StatusBadRequest, "bad_request"},
		{"a limit of 0", "?limit=0", http.StatusBadRequest, "bad_request"},
	} {
		t.Run("should reject "+tt.name, func(t *testing.T) {
			response, _ := p.sitemap(t, tt.query)
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &body)
			if response.Code != tt.code || body.Error != tt.error {
				t.Fatalf("\nwanted:\n%d %s\ngot:\n%d %s", tt.code, tt.error, response.Code, response.Body)
			}
		})
	}

	t.Run("should narrow the traffic list to a node's subtree", func(t *testing.T) {
		response, body := p.get(t, "/traffic?q="+url.QueryEscape(`sitemap = "https://api.x.com/users"`))
		if response.Code != http.StatusOK || len(body.Items) != 2 {
			t.Fatalf("\nwanted:\nthe two user pairs\ngot:\n%d %s", response.Code, response.Body)
		}
	})
}

func TestSitemapEvents(t *testing.T) {
	t.Run("should name the leaf node on traffic.request", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		request := domain.ProxyRequest{ID: uuid.Must(uuid.NewV7()), Scheme: "https", Method: "GET", Host: "API.x.com", Path: "/users/42?x=1", Metadata: map[string]any{}}
		request.SitemapNode = domain.SitemapLeaf(request.Scheme, request.Host, request.Path)
		if err := server.HandleRequest(request); err != nil {
			t.Fatal(err)
		}
		var body struct {
			SitemapNode string `json:"sitemap_node"`
		}
		event := <-subscriber.events
		if err := json.Unmarshal(event.data, &body); err != nil {
			t.Fatal(err)
		}
		if want := "https://api.x.com/users/{int}"; body.SitemapNode != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%s", want, event.data)
		}
	})

	t.Run("should announce sitemap.changed when the sitemap is rebuilt", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "rebuild.marasi"))
		server, lifecycle, subscriber := newIndexServer(t)
		openProjectPath(t, lifecycle, path)

		response := requestListener(t, server, http.MethodPost, "/jobs/sitemap/rebuild", "")
		if response.Code != http.StatusAccepted {
			t.Fatalf("\nwanted:\n202\ngot:\n%d %s", response.Code, response.Body.String())
		}
		if got := eventNamed(subscriber, "sitemap.changed", 5*time.Second); got != `{"reason":"rebuild"}` {
			t.Fatalf("\nwanted:\nsitemap.changed {\"reason\":\"rebuild\"}\ngot:\n%q", got)
		}
	})

	t.Run("should not announce sitemap.changed when another job is rebuilt", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "rebuild.marasi"))
		server, lifecycle, subscriber := newIndexServer(t)
		openProjectPath(t, lifecycle, path)

		requestListener(t, server, http.MethodPost, "/jobs/traffic_index/rebuild", "")
		if got := eventNamed(subscriber, "sitemap.changed", 300*time.Millisecond); got != "" {
			t.Fatalf("\nwanted:\nno sitemap.changed\ngot:\n%q", got)
		}
	})
}
