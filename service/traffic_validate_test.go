package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/tfkr-ae/marasi"
)

func TestTrafficQueryValidateRoute(t *testing.T) {
	t.Run("should return 204 for a valid query and exclusion", func(t *testing.T) {
		p := setupTrafficListProject(t, "text/plain")

		response, _ := p.get(t, "/traffic/query?q="+url.QueryEscape(`host = "example.com"`)+"&exclude=image/*&exclude=text/css")

		if response.Code != http.StatusNoContent {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d %s", http.StatusNoContent, response.Code, response.Body)
		}
		if response.Body.Len() != 0 {
			t.Fatalf("\nwanted:\nan empty body\ngot:\n%s", response.Body)
		}
	})

	for _, query := range []string{
		"q=" + url.QueryEscape(`host = "a" AND stauts = 5`),
		"exclude=text/html&exclude=" + url.QueryEscape("*/json"),
	} {
		t.Run("should return the GET /traffic invalid_query body for "+query, func(t *testing.T) {
			p := setupTrafficListProject(t, "text/plain")
			listed, _ := p.get(t, "/traffic?"+query)

			response, _ := p.get(t, "/traffic/query?"+query)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusBadRequest, response.Code)
			}
			if got, want := response.Body.String(), listed.Body.String(); got != want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
			}
		})
	}

	t.Run("should validate with no project open", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{}, func() {})
		response := httptest.NewRecorder()

		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/traffic/query?q="+url.QueryEscape(`stauts = 5`), nil))

		want := `{"error":"invalid_query","message":"unknown field \"stauts\"","position":1}` + "\n"
		if response.Code != http.StatusBadRequest || response.Body.String() != want {
			t.Fatalf("\nwanted:\n400 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})
}
