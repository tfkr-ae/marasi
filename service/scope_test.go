package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
)

func TestScopeCheck(t *testing.T) {
	t.Run("should check the live scope and return the parsed URL and matching rule", func(t *testing.T) {
		scope := compass.NewScope(false)
		if err := scope.AddRule(`example\.com:8443`, "host", false); err != nil {
			t.Fatal(err)
		}
		proxy := &marasi.Proxy{
			Scope: scope,
			Extensions: []*extensions.Runtime{{Data: &domain.Extension{
				Name:    "compass",
				Enabled: true,
			}}},
		}
		server := newTestServer(proxy, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		request := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(`{"url":"  https://example.com:8443/path?q=1#frag  "}`))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d %s", http.StatusOK, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := `{"in_scope":true,"tested_url":"https://example.com:8443/path?q=1#frag","rule":{"pattern":"example\\.com:8443","match_type":"host"},"compass_enabled":true}` + "\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if len(scope.IncludeRules) != 1 || len(scope.ExcludeRules) != 0 || scope.DefaultAllow {
			t.Fatalf("\nwanted:\nunchanged scope rules and default allow\ngot:\nincludes=%d excludes=%d default_allow=%v", len(scope.IncludeRules), len(scope.ExcludeRules), scope.DefaultAllow)
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})
}

func TestScopeCheckConcurrentLuaEdits(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
	}{
		{
			name: "rule additions and removals",
			script: `for i=1,2000 do
    s:add_rule('race'..i, 'HOST')
    s:add_rule('-race'..i, 'url')
    s:remove_rule('race'..i, 'host')
    s:remove_rule('-race'..i, 'URL')
end`,
		},
		{
			name: "default policy changes and clears",
			script: `for i=1,2000 do
    s:set_default_allow(i % 2 == 0)
    s:add_rule('example', 'host')
    s:add_rule('-example', 'url')
    s:clear_rules()
end`,
		},
	} {
		t.Run("should allow scope reads during Lua "+test.name, func(t *testing.T) {
			scope := compass.NewScope(false)
			proxy := &marasi.Proxy{Scope: scope}
			writer := &extensions.Runtime{Data: &domain.Extension{
				Name:       "scope-writer",
				LuaContent: "function mutate() local s=marasi:scope(); " + test.script + " end",
			}}
			reader := &extensions.Runtime{Data: &domain.Extension{
				Name: "scope-reader",
				LuaContent: `function read()
    local s=marasi:scope()
    for i=1,2000 do
        assert(type(tostring(s)) == 'string')
        s:matches_string('example.com', 'HOST')
    end
end`,
			}}
			for _, runtime := range []*extensions.Runtime{writer, reader} {
				if err := runtime.PrepareState(proxy, nil); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(newTestServer(proxy, func() {}))
			defer server.Close()
			start := make(chan struct{})
			var workers sync.WaitGroup
			for runtime, function := range map[*extensions.Runtime]string{writer: "mutate", reader: "read"} {
				workers.Go(func() {
					<-start
					if err := runtime.CallFunction(function); err != nil {
						t.Errorf("calling Lua %s: %v", function, err)
					}
				})
			}
			workers.Go(func() {
				<-start
				request := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
				for i := 0; i < 2000; i++ {
					scope.Matches(request)
					scope.Matches(&http.Response{})
					scope.Matches(nil)
					scope.MatchesString("example.com", "invalid")
					includes, excludes, _ := scope.Snapshot()
					// Mutating a snapshot must not mutate the live rule maps.
					clear(includes)
					clear(excludes)
				}
			})
			close(start)
			// Join even when an HTTP assertion fails, before the test server closes.
			defer workers.Wait()
			for i := 0; i < 2000; i++ {
				response, err := server.Client().Post(server.URL+"/scope/check", "application/json", strings.NewReader(`{"url":"https://example.com"}`))
				if err != nil {
					t.Fatal(err)
				}
				var got scopeCheckResponse
				err = json.NewDecoder(response.Body).Decode(&got)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK || got.TestedURL != "https://example.com" || got.CompassEnabled {
					t.Fatalf("\nwanted:\n200 scope result\ngot:\n%d %+v error=%v", response.StatusCode, got, err)
				}
				if test.name == "rule additions and removals" && (got.InScope || got.Rule != nil) {
					t.Fatalf("\nwanted:\ndefault deny without a matching rule\ngot:\n%+v", got)
				}
				if got.Rule != nil && (got.Rule.Pattern != "example" || got.InScope != (got.Rule.MatchType == "host")) {
					t.Fatalf("\nwanted:\nmatching include or exclude from the deciding walk\ngot:\n%+v", got)
				}
			}
			workers.Wait()
			includes, excludes, defaultAllow := scope.Snapshot()
			wantDefault := test.name == "default policy changes and clears"
			if len(includes) != 0 || len(excludes) != 0 || defaultAllow != wantDefault {
				t.Fatalf("\nwanted:\nempty rule maps and default_allow=%v\ngot:\nincludes=%v excludes=%v default_allow=%v", wantDefault, includes, excludes, defaultAllow)
			}
			if scope.Matches(nil) != wantDefault || scope.Matches(&http.Response{}) != wantDefault || scope.MatchesString("example.com", "invalid") != wantDefault {
				t.Fatalf("\nwanted:\ndefault_allow=%v for unsupported inputs\ngot:\ninconsistent default results", wantDefault)
			}
		})
	}
}

func TestScopeCheckValidation(t *testing.T) {
	t.Run("should validate the body and URL before checking for a missing scope", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		tests := []struct {
			name   string
			body   string
			status int
			want   string
		}{
			{name: "non-object body", body: `[]`, status: http.StatusBadRequest, want: "invalid_scope_request"},
			{name: "invalid JSON", body: `{`, status: http.StatusBadRequest, want: "invalid_scope_request"},
			{name: "extra JSON after object", body: `{"url":"https://example.com"} {}`, status: http.StatusBadRequest, want: "invalid_scope_request"},
			{name: "missing URL", body: `{}`, status: http.StatusBadRequest, want: "invalid_scope_request"},
			{name: "null URL", body: `{"url":null}`, status: http.StatusBadRequest, want: "invalid_scope_request"},
			{name: "non-string URL", body: `{"url":17}`, status: http.StatusBadRequest, want: "invalid_scope_request"},
			{name: "unknown field", body: `{"url":"https://example.com","extra":true}`, status: http.StatusBadRequest, want: "invalid_scope_request"},
			{name: "empty URL", body: `{"url":""}`, status: http.StatusBadRequest, want: "bad_request"},
			{name: "whitespace URL", body: `{"url":" \t "}`, status: http.StatusBadRequest, want: "bad_request"},
			{name: "unparseable URL", body: `{"url":"https://%zz"}`, status: http.StatusBadRequest, want: "bad_request"},
			{name: "valid URL and missing scope", body: `{"url":"https://example.com"}`, status: http.StatusNotFound, want: "not_found"},
		}
		for _, test := range tests {
			t.Run("should reject "+test.name, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(test.body))
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)

				want := `{"error":"` + test.want + `"}` + "\n"
				if response.Code != test.status || response.Body.String() != want {
					t.Fatalf("\nwanted:\n%d %s\ngot:\n%d %s", test.status, want, response.Code, response.Body.String())
				}
			})
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should return method not allowed and not found for unknown routes", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{}, func() {})
		for _, test := range []struct {
			method string
			path   string
			status int
		}{
			{method: http.MethodGet, path: "/scope/check", status: http.StatusMethodNotAllowed},
			{method: http.MethodPost, path: "/scope/unknown", status: http.StatusNotFound},
		} {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("\n%s %s wanted:\n%d\ngot:\n%d", test.method, test.path, test.status, response.Code)
			}
		}
	})
}

func TestScopeCheckURLAndRules(t *testing.T) {
	t.Run("should prefix missing schemes and keep explicit schemes", func(t *testing.T) {
		tests := []struct {
			name      string
			input     string
			testedURL string
			pattern   string
			matchType string
		}{
			{name: "bare host", input: "example.com", testedURL: "https://example.com", pattern: `example.com`, matchType: "host"},
			{name: "bare host with path, query, and fragment", input: "example.com/a?q=1#frag", testedURL: "https://example.com/a?q=1#frag", pattern: `^https://example\.com/a\?q=1#frag$`, matchType: "url"},
			{name: "explicit HTTP scheme", input: "http://example.com/a?q=1#frag", testedURL: "http://example.com/a?q=1#frag", pattern: `^http://example\.com/a\?q=1#frag$`, matchType: "url"},
		}
		for _, test := range tests {
			t.Run("should check "+test.name, func(t *testing.T) {
				scope := compass.NewScope(false)
				if err := scope.AddRule(test.pattern, test.matchType, false); err != nil {
					t.Fatal(err)
				}
				server := newTestServer(&marasi.Proxy{Scope: scope}, func() {})
				body, err := json.Marshal(struct {
					URL string `json:"url"`
				}{URL: test.input})
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(string(body)))
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)

				var got struct {
					InScope   bool   `json:"in_scope"`
					TestedURL string `json:"tested_url"`
					Rule      *struct {
						Pattern   string `json:"pattern"`
						MatchType string `json:"match_type"`
					} `json:"rule"`
					CompassEnabled bool `json:"compass_enabled"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatalf("decoding scope check response: %v", err)
				}
				if response.Code != http.StatusOK || !got.InScope || got.TestedURL != test.testedURL || got.Rule == nil || got.Rule.Pattern != test.pattern || got.Rule.MatchType != test.matchType || got.CompassEnabled {
					t.Fatalf("\nwanted:\n200 in_scope=true tested_url=%q rule=%q/%q compass_enabled=false\ngot:\n%d %+v", test.testedURL, test.pattern, test.matchType, response.Code, got)
				}
			})
		}
	})

	t.Run("should return a rule result for a parsed URL with an empty host", func(t *testing.T) {
		scope := compass.NewScope(false)
		if err := scope.AddRule(`^https:$`, "url", false); err != nil {
			t.Fatal(err)
		}
		server := newTestServer(&marasi.Proxy{Scope: scope}, func() {})
		request := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(`{"url":"https://"}`))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		want := `{"in_scope":true,"tested_url":"https:","rule":{"pattern":"^https:$","match_type":"url"},"compass_enabled":false}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
	})

	t.Run("should let matching excludes win over matching includes", func(t *testing.T) {
		scope := compass.NewScope(true)
		for _, rule := range []struct {
			pattern   string
			matchType string
			exclude   bool
		}{
			{pattern: "example", matchType: "host", exclude: true},
			{pattern: "example", matchType: "url", exclude: true},
			{pattern: "example", matchType: "host"},
		} {
			if err := scope.AddRule(rule.pattern, rule.matchType, rule.exclude); err != nil {
				t.Fatal(err)
			}
		}
		server := newTestServer(&marasi.Proxy{Scope: scope}, func() {})
		request := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(`{"url":"https://example.com"}`))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)

		var got struct {
			InScope bool `json:"in_scope"`
			Rule    *struct {
				Pattern   string `json:"pattern"`
				MatchType string `json:"match_type"`
			} `json:"rule"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding scope check response: %v", err)
		}
		if response.Code != http.StatusOK || got.InScope || got.Rule == nil || got.Rule.Pattern != "example" || (got.Rule.MatchType != "host" && got.Rule.MatchType != "url") {
			t.Fatalf("\nwanted:\n200 in_scope=false with either matching exclude\ngot:\n%d %+v", response.Code, got)
		}
	})

	t.Run("should return the default allow value with a null rule", func(t *testing.T) {
		for _, defaultAllow := range []bool{true, false} {
			t.Run("should return default allow "+strconv.FormatBool(defaultAllow), func(t *testing.T) {
				server := newTestServer(&marasi.Proxy{Scope: compass.NewScope(defaultAllow)}, func() {})
				request := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(`{"url":"https://example.com"}`))
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				want := `{"in_scope":` + strconv.FormatBool(defaultAllow) + `,"tested_url":"https://example.com","rule":null,"compass_enabled":false}` + "\n"
				if response.Code != http.StatusOK || response.Body.String() != want {
					t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
				}
			})
		}
	})
}

func TestScopeCheckCompassEnabled(t *testing.T) {
	for _, test := range []struct {
		name      string
		extension *extensions.Runtime
	}{
		{name: "disabled Compass", extension: &extensions.Runtime{Data: &domain.Extension{Name: "compass", Enabled: false}}},
		{name: "missing Compass"},
	} {
		t.Run("should return rules when "+test.name, func(t *testing.T) {
			scope := compass.NewScope(false)
			if err := scope.AddRule("example.com", "host", false); err != nil {
				t.Fatal(err)
			}
			proxy := &marasi.Proxy{Scope: scope}
			if test.extension != nil {
				proxy.Extensions = []*extensions.Runtime{test.extension}
			}
			server := newTestServer(proxy, func() {})
			request := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(`{"url":"https://example.com"}`))
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			want := `{"in_scope":true,"tested_url":"https://example.com","rule":{"pattern":"example.com","match_type":"host"},"compass_enabled":false}` + "\n"
			if response.Code != http.StatusOK || response.Body.String() != want {
				t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
			}
		})
	}
}
