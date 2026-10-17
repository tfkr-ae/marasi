package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLedgerCommandsLifecycle drives every bulk, read, job, client setting
// and launchpad unlink command against a running service and real captured
// traffic.
func TestLedgerCommandsLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("service process fixture uses Unix control sockets")
	}
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
	proxyAddress := startNamedInstance(t, binary, configDir, "work", "--project-name", "ledger-cli")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Header().Set("Content-Type", "image/png")
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}
		w.Write([]byte("proxied " + r.URL.Path))
	}))
	defer origin.Close()
	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}

	marasi := func(args ...string) (string, string, error) {
		return runMarasi(binary, append([]string{"--config-dir", configDir, "--instance", "work"}, args...)...)
	}
	must := func(args ...string) (string, string) {
		t.Helper()
		stdout, stderr, err := marasi(args...)
		if err != nil {
			t.Fatalf("%v: stdout %q, stderr %q, error %v", args, stdout, stderr, err)
		}
		return stdout, stderr
	}
	mustJSON := func(target any, args ...string) {
		t.Helper()
		stdout, _ := must(append(args, "--json")...)
		if err := json.Unmarshal([]byte(stdout), target); err != nil {
			t.Fatalf("%v: decoding %q: %v", args, stdout, err)
		}
	}
	waitFor := func(what string, done func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !done() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	for _, path := range []string{"/api/one", "/api/two", "/logo.png"} {
		getViaProxy(t, proxyAddress, origin.URL+path)
	}
	waitFor("three captured pairs", func() bool {
		stdout, _, err := marasi("traffic", "count")
		return err == nil && stdout == "3\n"
	})
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"items"`
		AsOf string `json:"as_of"`
	}
	mustJSON(&page, "traffic", "list")
	if len(page.Items) != 3 || page.AsOf == "" {
		t.Fatalf("wanted 3 pairs and an as_of, got %+v", page)
	}
	one, two := page.Items[0].ID, page.Items[1].ID
	if page.Items[0].Path != "/api/one" {
		t.Fatalf("wanted /api/one first, got %+v", page.Items)
	}
	apiQuery := `path = "/api/*"`

	t.Run("count and types", func(t *testing.T) {
		if stdout, _ := must("traffic", "count", "-q", apiQuery, "--as-of", page.AsOf); stdout != "2\n" {
			t.Fatalf("count: got %q", stdout)
		}
		if stdout, _ := must("traffic", "count", "--exclude", "image/*"); stdout != "2\n" {
			t.Fatalf("count with exclude: got %q", stdout)
		}
		if stdout, _ := must("traffic", "count", "--after", page.AsOf); stdout != "0\n" {
			t.Fatalf("count after as_of: got %q", stdout)
		}
		var count struct {
			Count int `json:"count"`
		}
		mustJSON(&count, "traffic", "count", "-q", apiQuery)
		if count.Count != 2 {
			t.Fatalf("JSON count: got %d", count.Count)
		}
		var types struct {
			Items []struct {
				ContentType string `json:"content_type"`
				Count       int    `json:"count"`
			} `json:"items"`
		}
		waitFor("content types", func() bool {
			stdout, _, err := marasi("traffic", "types", "--json")
			return err == nil && json.Unmarshal([]byte(stdout), &types) == nil && len(types.Items) == 2
		})
		stdout, _ := must("traffic", "types")
		if !strings.Contains(stdout, "text/plain") || !strings.Contains(stdout, "image/png") {
			t.Fatalf("types: got %q", stdout)
		}
	})

	t.Run("validate", func(t *testing.T) {
		if stdout, stderr := must("traffic", "validate", "-q", apiQuery, "--exclude", "image/*", "--order-by", "length desc"); stdout != "" || stderr != "query is valid\n" {
			t.Fatalf("valid: got %q %q", stdout, stderr)
		}
		if stdout, _ := must("traffic", "validate", "-q", apiQuery, "--json"); stdout != `{"valid":true}`+"\n" {
			t.Fatalf("valid JSON: got %q", stdout)
		}
		for _, args := range [][]string{
			{"-q", `hots = "a"`},
			{"--order-by", "colour"},
			{"--exclude", "not a type"},
		} {
			stdout, stderr, err := marasi(append([]string{"traffic", "validate"}, args...)...)
			if err == nil || stdout != "" || !strings.Contains(stderr, "validating query: invalid_query: ") {
				t.Fatalf("invalid %v: got %q %q %v", args, stdout, stderr, err)
			}
		}
		_, stderr, _ := marasi("traffic", "validate", "-q", `hots = "a"`)
		if !strings.Contains(stderr, "at position 1") {
			t.Fatalf("wanted the position, got %q", stderr)
		}
		stdout, stderr, err := marasi("traffic", "validate", "-q", `hots = "a"`, "--json")
		if err == nil || stderr != "" || !strings.HasPrefix(stdout, `{"error":"validating query: invalid_query: `) {
			t.Fatalf("invalid JSON: got %q %q %v", stdout, stderr, err)
		}
	})

	t.Run("highlight", func(t *testing.T) {
		if _, stderr := must("traffic", "bulk", "highlight", "3", "--id", one); stderr != "1 pair highlighted\n" {
			t.Fatalf("highlight: got %q", stderr)
		}
		if stdout, _ := must("traffic", "count", "-q", "highlight = 3"); stdout != "1\n" {
			t.Fatalf("highlighted count: got %q", stdout)
		}
		var result struct {
			Count int `json:"count"`
		}
		mustJSON(&result, "traffic", "bulk", "highlight", "none", "-q", "highlight:*", "--as-of", page.AsOf)
		if result.Count != 1 {
			t.Fatalf("unhighlight JSON: got %d", result.Count)
		}
		if stdout, _ := must("traffic", "count", "-q", "highlight:*"); stdout != "0\n" {
			t.Fatalf("after clear: got %q", stdout)
		}
	})

	t.Run("metadata", func(t *testing.T) {
		if _, stderr := must("traffic", "bulk", "metadata", "set", "triage.state", `"seen"`, "-q", apiQuery, "--as-of", page.AsOf); stderr != "2 pairs updated\n" {
			t.Fatalf("metadata set: got %q", stderr)
		}
		if stdout, _ := must("traffic", "count", "-q", `metadata.triage.state = "seen"`); stdout != "2\n" {
			t.Fatalf("metadata count: got %q", stdout)
		}
		var result struct {
			Count int `json:"count"`
		}
		mustJSON(&result, "traffic", "bulk", "metadata", "clear", "triage", "--id", one, "--id", two)
		if result.Count != 2 {
			t.Fatalf("metadata clear: got %d", result.Count)
		}
		if stdout, _ := must("traffic", "count", "-q", "metadata.triage:*"); stdout != "0\n" {
			t.Fatalf("after metadata clear: got %q", stdout)
		}
	})

	t.Run("notes", func(t *testing.T) {
		if _, stderr := must("traffic", "bulk", "note", "append", "bulk noted", "--id", one, "--id", two); stderr != "2 pairs noted\n" {
			t.Fatalf("note append: got %q", stderr)
		}
		if stdout, _ := must("notes", "get", one); stdout != "bulk noted\n" {
			t.Fatalf("note: got %q", stdout)
		}
		var result struct {
			Count int `json:"count"`
		}
		mustJSON(&result, "traffic", "bulk", "note", "clear", "-q", "", "--as-of", page.AsOf)
		if result.Count != 2 {
			t.Fatalf("note clear: got %d", result.Count)
		}
	})

	t.Run("links", func(t *testing.T) {
		var launchpad struct {
			ID string `json:"id"`
		}
		mustJSON(&launchpad, "launchpad", "create", "--name", "bulk")
		if _, stderr := must("traffic", "bulk", "link", "--launchpad", launchpad.ID, "-q", apiQuery, "--as-of", page.AsOf); stderr != "2 pairs linked\n" {
			t.Fatalf("link: got %q", stderr)
		}
		inLaunchpad := `launchpad = "` + launchpad.ID + `"`
		if stdout, _ := must("traffic", "count", "-q", inLaunchpad); stdout != "2\n" {
			t.Fatalf("linked count: got %q", stdout)
		}
		_, stderr := must("launchpad", "unlink", launchpad.ID, one)
		if stderr != "request "+one+" unlinked from launchpad "+launchpad.ID+" successfully\n" {
			t.Fatalf("launchpad unlink: got %q", stderr)
		}
		var unlinked struct {
			LaunchpadID string `json:"launchpad_id"`
			RequestID   string `json:"request_id"`
		}
		mustJSON(&unlinked, "launchpad", "unlink", launchpad.ID, two)
		if unlinked.RequestID != two || unlinked.LaunchpadID != launchpad.ID {
			t.Fatalf("launchpad unlink JSON: got %+v", unlinked)
		}
		if stdout, _ := must("traffic", "count", "-q", inLaunchpad); stdout != "0\n" {
			t.Fatalf("after unlink: got %q", stdout)
		}
		must("traffic", "bulk", "link", "--launchpad", launchpad.ID, "--id", one)
		var result struct {
			Count int `json:"count"`
		}
		mustJSON(&result, "traffic", "bulk", "unlink", "--launchpad", launchpad.ID, "--id", one)
		if result.Count != 1 {
			t.Fatalf("bulk unlink: got %d", result.Count)
		}
	})

	t.Run("jobs and sitemap", func(t *testing.T) {
		var jobs struct {
			Items []backgroundJob `json:"items"`
		}
		sitemapDone := func() bool {
			stdout, _, err := marasi("jobs", "list", "--json")
			if err != nil || json.Unmarshal([]byte(stdout), &jobs) != nil {
				return false
			}
			for _, job := range jobs.Items {
				if job.Name == "sitemap" {
					return job.Complete
				}
			}
			return false
		}
		waitFor("the sitemap job", sitemapDone)
		stdout, _ := must("jobs", "list")
		if !strings.Contains(stdout, "sitemap") || !strings.Contains(stdout, "complete") {
			t.Fatalf("jobs list: got %q", stdout)
		}
		originName := "http://" + originURL.Host
		stdout, _ = must("traffic", "sitemap")
		// The origin is the only row, between the column names above and below it.
		if rows := strings.Split(stdout, "\n"); len(rows) != 4 || rows[1] != originName+"  3      0    +" {
			t.Fatalf("sitemap origins: got %q", stdout)
		}
		var sitemap struct {
			Items []struct {
				Name  string `json:"name"`
				Pairs int    `json:"pairs"`
			} `json:"items"`
		}
		mustJSON(&sitemap, "traffic", "sitemap", "--node", originName, "-q", apiQuery, "--as-of", page.AsOf)
		if len(sitemap.Items) != 1 || sitemap.Items[0].Pairs != 2 {
			t.Fatalf("sitemap node in query mode: got %+v", sitemap.Items)
		}

		if _, stderr := must("jobs", "rebuild", "sitemap"); stderr != "job sitemap rebuild started\n" {
			t.Fatalf("rebuild: got %q", stderr)
		}
		waitFor("the rebuilt sitemap job", sitemapDone)
		var rebuilt backgroundJob
		mustJSON(&rebuilt, "jobs", "rebuild", "sitemap")
		if rebuilt.Name != "sitemap" {
			t.Fatalf("rebuild JSON: got %+v", rebuilt)
		}
		stdout, stderr, err := marasi("jobs", "rebuild", "nonexistent")
		if err == nil || stdout != "" || !strings.Contains(stderr, "rebuilding job: not_found") {
			t.Fatalf("unknown job: got %q %q %v", stdout, stderr, err)
		}
	})

	t.Run("settings", func(t *testing.T) {
		key := "ledger/view one"
		if _, stderr := must("settings", "set", "cli-test", key, `{"columns": ["host"]}`); stderr != "setting cli-test "+key+" set\n" {
			t.Fatalf("set: got %q", stderr)
		}
		if stdout, _ := must("settings", "get", "cli-test", key); stdout != `{"columns":["host"]}`+"\n" {
			t.Fatalf("get: got %q", stdout)
		}
		var setting clientSetting
		mustJSON(&setting, "settings", "get", "cli-test", key)
		if setting.Key != key || setting.Client != "cli-test" {
			t.Fatalf("get JSON: got %+v", setting)
		}
		must("settings", "set", "cli-test", "other", "1")
		if stdout, _ := must("settings", "list", "cli-test", "--prefix", "ledger/"); stdout != "KEY              VALUE\n"+key+`  {"columns":["host"]}`+"\nKEY              VALUE\n" {
			t.Fatalf("list: got %q", stdout)
		}
		var list struct {
			Items []clientSetting `json:"items"`
		}
		mustJSON(&list, "settings", "list", "cli-test")
		if len(list.Items) != 2 {
			t.Fatalf("list JSON: got %+v", list.Items)
		}
		if _, stderr := must("settings", "delete", "cli-test", key); stderr != "setting cli-test "+key+" deleted\n" {
			t.Fatalf("delete: got %q", stderr)
		}
		stdout, stderr, err := marasi("settings", "get", "cli-test", key)
		if err == nil || stdout != "" || !strings.Contains(stderr, "getting client setting: not_found") {
			t.Fatalf("deleted get: got %q %q %v", stdout, stderr, err)
		}
		stdout, stderr, err = marasi("settings", "set", "marasi", "k", "1", "--json")
		if err == nil || stderr != "" || stdout != `{"error":"setting client setting: reserved_client"}`+"\n" {
			t.Fatalf("reserved client: got %q %q %v", stdout, stderr, err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if _, stderr := must("traffic", "bulk", "delete", "--id", one); stderr != "1 pair deleted\n" {
			t.Fatalf("delete: got %q", stderr)
		}
		var result struct {
			Count int `json:"count"`
		}
		mustJSON(&result, "traffic", "bulk", "delete", "-q", "", "--as-of", page.AsOf)
		if result.Count != 2 {
			t.Fatalf("delete JSON: got %d", result.Count)
		}
		if stdout, _ := must("traffic", "count"); stdout != "0\n" {
			t.Fatalf("after delete: got %q", stdout)
		}
	})
}
