package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
)

// searchNames decodes a GET /launchpad/search body into fixture names per
// launchpad name, entries joined in order.
func (p *launchpadQueryProject) searchNames(t *testing.T, body string) map[string]string {
	t.Helper()
	var result map[string][]uuid.UUID
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	got := map[string]string{}
	for launchpadID, ids := range result {
		launchpad := "?" + launchpadID
		for name, id := range p.launchpads {
			if id.String() == launchpadID {
				launchpad = name
			}
		}
		names := make([]string, 0, len(ids))
		for _, id := range ids {
			name := "?" + id.String()
			for candidate, candidateID := range p.ids {
				if candidateID == id {
					name = candidate
				}
			}
			names = append(names, name)
		}
		got[launchpad] = strings.Join(names, ",")
	}
	return got
}

func TestLaunchpadSearch(t *testing.T) {
	p := setupLaunchpadQueryProject(t)

	t.Run("should group matches by launchpad, linked originals and shared pairs included", func(t *testing.T) {
		for _, test := range []struct {
			q    string
			want map[string]string
		}{
			// orig is linked with no launchpad_id metadata; shared is linked
			// to both launchpads.
			{q: `"token"`, want: map[string]string{"one": "orig,launched,shared", "two": "shared,other"}},
			{q: `response_body:"shared"`, want: map[string]string{"one": "shared", "two": "shared"}},
			{q: `status_code >= 500`, want: map[string]string{"one": "launched", "two": "other"}},
			{q: `response_body:"session"`, want: map[string]string{"one": "orig"}},
			{q: ``, want: map[string]string{"one": "orig,launched,shared,hostile", "two": "shared,other"}},
		} {
			code, body := p.get("/launchpad/search", test.q)
			if code != http.StatusOK {
				t.Fatalf("\n%s wanted:\n200\ngot:\n%d %s", test.q, code, body)
			}
			got := p.searchNames(t, body)
			if len(got) != len(test.want) {
				t.Fatalf("\n%s wanted:\n%v\ngot:\n%v", test.q, test.want, got)
			}
			for launchpad, want := range test.want {
				if got[launchpad] != want {
					t.Fatalf("\n%s wanted:\n%v\ngot:\n%v", test.q, test.want, got)
				}
			}
		}
	})

	t.Run("should return an empty object when nothing matches", func(t *testing.T) {
		for _, q := range []string{`response_body:"nothing-matches-this"`, `response_body:"free"`} {
			code, body := p.get("/launchpad/search", q)
			if code != http.StatusOK || body != "{}\n" {
				t.Fatalf("\n%s wanted:\n200 {}\ngot:\n%d %s", q, code, body)
			}
		}
	})

	t.Run("should return 400 invalid_query positioned in the researcher's text", func(t *testing.T) {
		for _, test := range []struct{ q, want string }{
			{q: `status_code = 200 AND stauts = 5`, want: "{\"error\":\"invalid_query\",\"message\":\"unknown field \\\"stauts\\\"\",\"position\":23}\n"},
		} {
			code, body := p.get("/launchpad/search", test.q)
			if code != http.StatusBadRequest || body != test.want {
				t.Fatalf("\nwanted:\n400 %s\ngot:\n%d %s", test.want, code, body)
			}
			// The same text gets the same answer from GET /traffic/query.
			check := requestControlAPI(p.server, http.MethodGet, "/traffic/query?q="+strings.ReplaceAll(test.q, " ", "+"), "")
			if check.Code != http.StatusBadRequest || check.Body.String() != test.want {
				t.Fatalf("\nwanted:\n/traffic/query to agree\ngot:\n%d %s", check.Code, check.Body.String())
			}
		}
	})

	t.Run("should reject an unbalanced parenthesis instead of pairing it", func(t *testing.T) {
		code, body := p.get("/launchpad/search", `"token")`)
		if code != http.StatusBadRequest || !strings.HasPrefix(body, `{"error":"invalid_query"`) {
			t.Fatalf("\nwanted:\n400 invalid_query\ngot:\n%d %s", code, body)
		}
	})

	t.Run("should return 404 without an open project", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{}, func() {})
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodGet, "/launchpad/search?q=x", ""), http.StatusNotFound, "not_found")
	})
}
