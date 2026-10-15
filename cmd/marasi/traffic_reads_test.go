package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestTrafficReadCommands(t *testing.T) {
	binary := buildMarasi(t)
	sitemapBody := `{"items":[{"name":"https://a.test","segment":"https://a.test","pairs":12,"errors":1,"has_children":true},{"name":"http://b.test:8080","segment":"http://b.test:8080","pairs":3,"errors":0,"has_children":false}],"next_cursor":"opaque","sitemap":{"complete":false},"index":{"complete":false}}` + "\n"
	for _, test := range []struct {
		name   string
		args   []string
		status int
		body   string
		path   string
		query  url.Values
		stdout string
		stderr string
		json   string
	}{
		{
			name: "sitemap origins", args: []string{"traffic", "sitemap"},
			status: http.StatusOK, body: sitemapBody, path: "/traffic/sitemap", query: url.Values{"limit": {"100"}},
			stdout: "https://a.test      12  1  +\nhttp://b.test:8080  3   0  \n",
			stderr: "next_cursor=opaque\nnotice: the sitemap is still building; counts may be incomplete\nnotice: the traffic index is still building; text results may be incomplete\n",
		},
		{
			name: "sitemap node in query mode", args: []string{"traffic", "sitemap", "--node", "https://a.test", "-q", "status_code >= 500", "--as-of", bulkAsOf, "--exclude", "image/*", "--limit", "5", "--cursor", "opaque"},
			status: http.StatusOK, body: `{"items":[],"next_cursor":null,"sitemap":{"complete":true}}`, path: "/traffic/sitemap",
			query: url.Values{"node": {"https://a.test"}, "q": {"status_code >= 500"}, "as_of": {bulkAsOf}, "exclude": {"image/*"}, "limit": {"5"}, "cursor": {"opaque"}},
		},
		{
			name: "count", args: []string{"traffic", "count", "-q", "host = \"a.test\"", "--exclude", "font/*", "--as-of", bulkAsOf, "--after", bulkIDA},
			status: http.StatusOK, body: `{"count":7,"capped":false,"index":{"complete":true}}`, path: "/traffic/count",
			query:  url.Values{"q": {"host = \"a.test\""}, "exclude": {"font/*"}, "as_of": {bulkAsOf}, "after": {bulkIDA}},
			stdout: "7\n",
		},
		{
			name: "count capped while indexing", args: []string{"traffic", "count"},
			status: http.StatusOK, body: `{"count":100000,"capped":true,"index":{"complete":false}}`, path: "/traffic/count", query: url.Values{},
			stdout: "100000+\n", stderr: "notice: the traffic index is still building; text results may be incomplete\n",
		},
		{
			name: "types", args: []string{"traffic", "types"},
			status: http.StatusOK, body: `{"items":[{"content_type":"text/html","count":4},{"content_type":"","count":1}],"sitemap":{"complete":false}}`, path: "/traffic/content-types", query: url.Values{},
			stdout: "text/html  4\n-          1\n", stderr: "notice: the sitemap is still building; counts may be incomplete\n",
		},
		{
			name: "validate", args: []string{"traffic", "validate", "-q", "status_code >= 500", "--exclude", "image/*", "--order-by", "length desc"},
			status: http.StatusNoContent, path: "/traffic/query",
			query:  url.Values{"q": {"status_code >= 500"}, "exclude": {"image/*"}, "order_by": {"length desc"}},
			stderr: "query is valid\n", json: `{"valid":true}` + "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", test.status, test.body)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			got := sent.snapshot()
			if err != nil || stdout != test.stdout || stderr != test.stderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q, error %v", test.stdout, test.stderr, stdout, stderr, err)
			}
			gotQuery, _ := url.ParseQuery(got.RawQuery)
			if got.Method != http.MethodGet || got.Path != test.path || gotQuery.Encode() != test.query.Encode() {
				t.Fatalf("\nwanted:\nGET %s?%s\ngot:\n%s %s?%s", test.path, test.query.Encode(), got.Method, got.Path, got.RawQuery)
			}

			stdout, stderr, err = runMarasi(binary, append(args, "--json")...)
			want := test.body
			if test.json != "" {
				want = test.json
			}
			if err != nil || stdout != want || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
			}
		})
	}
}

func TestTrafficValidateRejectsInvalidQuery(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"invalid_query","message":"unknown field \"hots\"","position":1}`)
	args := []string{"--config-dir", configDir, "--instance", "work", "traffic", "validate", "-q", `hots = "a"`}
	stdout, stderr, err := runMarasi(binary, args...)
	want := `validating query: invalid_query: unknown field "hots" at position 1`
	if err == nil || stdout != "" || !strings.Contains(stderr, want) {
		t.Fatalf("\nwanted:\nexit 1 with %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, append(args, "--json")...)
	assertJSONCommandError(t, stdout, stderr, err, want)
}

func TestTrafficReadUsage(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{}`)
	for _, args := range [][]string{
		{"traffic", "sitemap", "-q", "a"},
		{"traffic", "sitemap", "--as-of", bulkAsOf},
		{"traffic", "sitemap", "--limit", "0"},
		{"traffic", "sitemap", "extra"},
		{"traffic", "count", "--as-of", "junk"},
		{"traffic", "count", "--after", "junk"},
		{"traffic", "count", "--as-of", ""},
		{"traffic", "count", "--after", ""},
		{"traffic", "sitemap", "-q", "a", "--as-of", ""},
		{"traffic", "types", "extra"},
		{"traffic", "validate", "extra"},
	} {
		commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
		if stdout, stderr, err := runMarasi(binary, commandArgs...); err == nil || stdout != "" {
			t.Fatalf("\nwanted:\nusage error for %v\ngot:\nstdout %q, stderr %q", args, stdout, stderr)
		}
		if got := sent.snapshot(); got.Method != "" {
			t.Fatalf("\nwanted:\nno request for %v\ngot:\n%s %s", args, got.Method, got.Path)
		}
	}
}
