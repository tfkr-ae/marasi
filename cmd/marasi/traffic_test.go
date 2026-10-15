package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTrafficListCommand(t *testing.T) {
	t.Run("should print the newest page oldest first", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","scheme":"https","method":"POST","host":"example.com","path":"/login","status":"401 Unauthorized","status_code":401,"content_type":"text/plain","length":"45","metadata":{},"requested_at":"2026-01-02T03:04:04Z","responded_at":"2026-01-02T03:04:05Z"},{"id":"0193802f-f0e7-73d9-a764-06d21e367809","scheme":"https","method":"GET","host":"example.com","path":"/a","status":"200 OK","status_code":200,"content_type":"application/json","length":"12","metadata":{"foo":"bar"},"requested_at":"2026-01-02T03:04:05Z","responded_at":"2026-01-02T03:04:06Z"}],"next_cursor":null}`)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "limit=200" {
			t.Fatalf("\nwanted:\nGET /traffic?limit=200\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
		want := "01938032-1b17-7243-b035-e6a9f4645904  POST  example.com  /login  401  45\n0193802f-f0e7-73d9-a764-06d21e367809  GET   example.com  /a      200  12\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should truncate long paths before aligning the row", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"0193802f-f0e7-73d9-a764-06d21e367809","method":"GET","host":"example.com","path":"/12345678901234567890123456789012345678901234567890","status_code":200,"length":"12"},{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"POST","host":"example.com","path":"/short","status_code":404,"length":"45"}],"next_cursor":null}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "0193802f-f0e7-73d9-a764-06d21e367809  GET   example.com  /123456789012345678901234567890123456...  200  12\n01938032-1b17-7243-b035-e6a9f4645904  POST  example.com  /short                                    404  45\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should truncate long paths without splitting utf-8 characters", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"0193802f-f0e7-73d9-a764-06d21e367809","method":"GET","host":"example.com","path":"/12345678901234567890123456789012345😀XYZQ","status_code":200,"length":"12"}],"next_cursor":null}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "0193802f-f0e7-73d9-a764-06d21e367809  GET  example.com  /12345678901234567890123456789012345😀...  200  12\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if !utf8.ValidString(stdout) {
			t.Fatalf("\nwanted:\nvalid utf-8\ngot:\n%s", stdout)
		}
	})

	t.Run("should write next_cursor to stderr when another page exists", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12"}],"next_cursor":"0193802f-f0e7-73d9-a764-06d21e367809"}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "01938032-1b17-7243-b035-e6a9f4645904  GET  example.com  /a  200  12\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if stderr != "next_cursor=0193802f-f0e7-73d9-a764-06d21e367809\n" {
			t.Fatalf("\nwanted:\nnext_cursor=0193802f-f0e7-73d9-a764-06d21e367809\ngot:\n%s", stderr)
		}
	})

	t.Run("should write prev_cursor and as_of to stderr after next_cursor", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":"bmV4dA","prev_cursor":"cHJldg","as_of":"01938032-1b17-7243-b035-e6a9f4645904","index":{"complete":true}}`)

		_, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "next_cursor=bmV4dA\nprev_cursor=cHJldg\nas_of=01938032-1b17-7243-b035-e6a9f4645904\n"
		if stderr != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stderr)
		}
	})

	t.Run("should print the control API list body with --json", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12"}],"next_cursor":"0193802f-f0e7-73d9-a764-06d21e367809"}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "--json", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != body {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", body, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should warn on stderr that text results may be incomplete while the index builds", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12"}],"next_cursor":"0193802f-f0e7-73d9-a764-06d21e367809","index":{"complete":false}}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "01938032-1b17-7243-b035-e6a9f4645904  GET  example.com  /a  200  12\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		wantStderr := "next_cursor=0193802f-f0e7-73d9-a764-06d21e367809\nnotice: the traffic index is still building; text results may be incomplete\n"
		if stderr != wantStderr {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantStderr, stderr)
		}
	})

	t.Run("should print no notice when the index is complete", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null,"index":{"complete":true}}`)

		_, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should print the list body unchanged with --json while the index builds", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"items":[],"next_cursor":null,"index":{"complete":false}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "--json", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != body || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %s and empty stderr\ngot:\nstdout %s and stderr %s", body, stdout, stderr)
		}
	})

	t.Run("should print no rows for an empty page", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno rows\ngot:\n%s", stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should fail and name the instance when the control listener is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "work") {
			t.Fatalf("\nwanted:\nerror naming work\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "work") {
			t.Fatalf("\nwanted:\nstderr naming work\ngot:\n%s", stderr)
		}
	})

	t.Run("should print a JSON error when the control listener is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")
	})

	t.Run("should print a short stderr message for a 400", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"error":"bad_request"}`
		sent := startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, body)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		got := sent.snapshot()
		if got.RawQuery != "limit=200" {
			t.Fatalf("\nwanted:\nlimit=200\ngot:\n%s", got.RawQuery)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "400") {
			t.Fatalf("\nwanted:\nstderr naming 400\ngot:\n%s", stderr)
		}
		if strings.Contains(stderr, body) {
			t.Fatalf("\nwanted:\nshort stderr without the HTTP body\ngot:\n%s", stderr)
		}
	})

	t.Run("should normalize a JSON control API error", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"error":"bad_request"}`
		sent := startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, body)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list", "--json")
		got := sent.snapshot()
		if got.RawQuery != "limit=200" {
			t.Fatalf("\nwanted:\nlimit=200\ngot:\n%s", got.RawQuery)
		}
		assertJSONCommandError(t, stdout, stderr, err, "listing traffic: bad_request")
	})

	for name, body := range map[string]string{
		"missing API error":    `{}`,
		"malformed API error":  `{`,
		"non-string API error": `{"error":42}`,
	} {
		t.Run("should use HTTP status for "+name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, body)

			stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list", "--json")
			assertJSONCommandError(t, stdout, stderr, err, "listing traffic: 400 Bad Request")
		})
	}

	t.Run("should send the opaque --cursor as the cursor query parameter", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--cursor", "eyJ2IjoxfQ")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "cursor=eyJ2IjoxfQ&limit=200" {
			t.Fatalf("\nwanted:\nGET /traffic?cursor=eyJ2IjoxfQ&limit=200\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should send --exclude, --as-of and --backward", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list",
			"--exclude", "image/*", "--exclude", "text/html", "--as-of", "01938032-1b17-7243-b035-e6a9f4645904", "--backward", "--cursor", "abc")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		want := "as_of=01938032-1b17-7243-b035-e6a9f4645904&cursor=abc&direction=backward&exclude=image%2F%2A&exclude=text%2Fhtml&limit=200"
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != want {
			t.Fatalf("\nwanted:\nGET /traffic?%s\ngot:\n%s %s?%s", want, got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should reject a malformed --as-of without a request", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--as-of", "nope")
		if err == nil || err.Error() != `invalid uuid "nope"` {
			t.Fatalf("\nwanted:\ninvalid uuid \"nope\"\ngot:\n%v", err)
		}
		if sent.snapshot().Path != "" {
			t.Fatalf("\nwanted:\nno request\ngot:\n%s", sent.snapshot().Path)
		}
	})

	t.Run("should send --limit as the limit query parameter", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--limit", "10")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "limit=10" {
			t.Fatalf("\nwanted:\nGET /traffic?limit=10\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	for _, flag := range []string{"--query", "-q"} {
		t.Run("should send "+flag+" as the q query parameter", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

			_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", flag, `host = "*.example.com" AND status_code >= 500`)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			want := "limit=200&q=host+%3D+%22%2A.example.com%22+AND+status_code+%3E%3D+500"
			if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != want {
				t.Fatalf("\nwanted:\nGET /traffic?%s\ngot:\n%s %s?%s", want, got.Method, got.Path, got.RawQuery)
			}
		})
	}

	for _, flag := range []string{"--host", "--method", "--status-code", "--path"} {
		t.Run("should reject the removed "+flag+" flag", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

			_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", flag, "x")
			if err == nil || !strings.Contains(err.Error(), "unknown flag: "+flag) {
				t.Fatalf("\nwanted:\nunknown flag: %s\ngot:\n%v", flag, err)
			}
			if sent.snapshot().Path != "" {
				t.Fatalf("\nwanted:\nno request\ngot:\n%s", sent.snapshot().Path)
			}
		})
	}

	t.Run("should print a query error with its position on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"invalid_query","message":"unknown field \"stauts\"","position":1}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "-q", "stauts = 5")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		want := `listing traffic: invalid_query: unknown field "stauts" at position 1`
		if !strings.Contains(stderr, want) {
			t.Fatalf("\nwanted stderr containing:\n%s\ngot:\n%s", want, stderr)
		}
	})

	t.Run("should normalize a JSON query error keeping the message and position", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"invalid_query","message":"unknown field \"stauts\"","position":1}`)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list", "-q", "stauts = 5", "--json")
		assertJSONCommandError(t, stdout, stderr, err, `listing traffic: invalid_query: unknown field "stauts" at position 1`)
	})

	t.Run("should document the query fields and the OR-before-AND rule in help", func(t *testing.T) {
		stdout, _, err := executeRoot(t, "traffic", "list", "--help")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		for _, want := range []string{"-q, --query", "host", "method", "scheme", "path", "content_type", "status_code", "requested_at", "responded_at", "metadata.<key>", "request_head", "request_body", "response_head", "response_body", "response_body:\"password\"", "note:\"idor candidate\"", "metadata:\"workshop\"", "launchpad = \"<launchpad id>\"", "launchpad != \"<launchpad id>\"", "launchpad:*", "including pairs in no launchpad", "finding = \"<finding id>\"", "test_case:*", "note:*", "NOT note:*", "at least 3 characters", "OR binds tighter than AND"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("\nwanted help containing:\n%s\ngot:\n%s", want, stdout)
			}
		}
	})

	t.Run("should reject --project", func(t *testing.T) {
		stdout, stderr, err := executeRoot(t, "--config-dir", serviceConfigDir(t), "traffic", "list", "--project", "scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "unknown flag: --project") {
			t.Fatalf("\nwanted:\nunknown flag: --project\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "unknown flag: --project") {
			t.Fatalf("\nwanted:\nstderr naming unknown flag\ngot:\n%s", stderr)
		}
	})
}

func TestTrafficGetCommand(t *testing.T) {
	t.Run("should print the control API detail body with --json", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"a note","metadata":{"foo":"bar"},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9hCg==","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"200 OK","status_code":200,"content_type":"application/json","length":"12","raw":"aGVsbG8K","responded_at":"2026-01-02T03:04:06Z"}}`
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "get", "--json", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic/"+id || got.RawQuery != "" {
			t.Fatalf("\nwanted:\nGET /traffic/%s\ngot:\n%s %s?%s", id, got.Method, got.Path, got.RawQuery)
		}
		if stdout != body {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", body, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should preserve the full path and raw values with --json", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/12345678901234567890123456789012345678901234567890","raw":"R0VUIC9hAA==","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"200 OK","status_code":200,"content_type":"application/octet-stream","length":"3","raw":"gIGC","responded_at":"2026-01-02T03:04:06Z"}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "get", "--json", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != body {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", body, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should print fields, note, metadata, and utf-8 raw", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"a note","metadata":{"foo":"bar"},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9hCg==","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"200 OK","status_code":200,"content_type":"application/json","length":"12","raw":"aGVsbG8K","responded_at":"2026-01-02T03:04:06Z"}}`
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic/"+id {
			t.Fatalf("\nwanted:\nGET /traffic/%s\ngot:\n%s %s", id, got.Method, got.Path)
		}
		want := "id: 0193802f-f0e7-73d9-a764-06d21e367809\nscheme: https\nmethod: GET\nhost: example.com\npath: /a\nrequested_at: 2026-01-02T03:04:05Z\nstatus: 200 OK\nstatus_code: 200\ncontent_type: application/json\nlength: 12\nresponded_at: 2026-01-02T03:04:06Z\nnote: a note\nmetadata: {\"foo\":\"bar\"}\nGET /a\nhello\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should start the response raw on a new line", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9h","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"400 Bad Request","status_code":400,"content_type":"text/plain","length":"12","raw":"SFRUUC8xLjEgNDAwIEJhZCBSZXF1ZXN0Cg==","responded_at":"2026-01-02T03:04:06Z"}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "id: 0193802f-f0e7-73d9-a764-06d21e367809\nscheme: https\nmethod: GET\nhost: example.com\npath: /a\nrequested_at: 2026-01-02T03:04:05Z\nstatus: 400 Bad Request\nstatus_code: 400\ncontent_type: text/plain\nlength: 12\nresponded_at: 2026-01-02T03:04:06Z\nGET /a\nHTTP/1.1 400 Bad Request\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should skip empty note and metadata", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9hCg==","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"200 OK","status_code":200,"content_type":"application/json","length":"12","raw":"aGVsbG8K","responded_at":"2026-01-02T03:04:06Z"}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if strings.Contains(stdout, "note:") {
			t.Fatalf("\nwanted:\nno note line\ngot:\n%s", stdout)
		}
		if strings.Contains(stdout, "metadata:") {
			t.Fatalf("\nwanted:\nno metadata line\ngot:\n%s", stdout)
		}
	})

	t.Run("should print a not-utf-8 line for invalid raw", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"/wAB","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"200 OK","status_code":200,"content_type":"application/octet-stream","length":"3","raw":"gIGC","responded_at":"2026-01-02T03:04:06Z"}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if !strings.Contains(stdout, "request raw: 3 bytes, not utf-8\n") {
			t.Fatalf("\nwanted:\nrequest raw: 3 bytes, not utf-8\ngot:\n%s", stdout)
		}
		if !strings.Contains(stdout, "response raw: 3 bytes, not utf-8\n") {
			t.Fatalf("\nwanted:\nresponse raw: 3 bytes, not utf-8\ngot:\n%s", stdout)
		}
	})

	t.Run("should say there is no response yet for an in-flight pair", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9hCg==","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"N/A","status_code":-1,"content_type":"","length":"0","raw":null,"responded_at":null}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "id: 0193802f-f0e7-73d9-a764-06d21e367809\nscheme: https\nmethod: GET\nhost: example.com\npath: /a\nrequested_at: 2026-01-02T03:04:05Z\nstatus: N/A\nstatus_code: -1\ncontent_type: \nlength: 0\nresponded_at: \nGET /a\nno response yet\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should start no response yet on a new line", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9h","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"N/A","status_code":-1,"content_type":"","length":"0","raw":null,"responded_at":null}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "id: 0193802f-f0e7-73d9-a764-06d21e367809\nscheme: https\nmethod: GET\nhost: example.com\npath: /a\nrequested_at: 2026-01-02T03:04:05Z\nstatus: N/A\nstatus_code: -1\ncontent_type: \nlength: 0\nresponded_at: \nGET /a\nno response yet\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should print a short stderr message for a 404", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "01938032-1b17-7243-b035-e6a9f4645904"
		body := `{"error":"not_found"}`
		sent := startCannedControlAPI(t, configDir, "work", http.StatusNotFound, body)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic/"+id {
			t.Fatalf("\nwanted:\nGET /traffic/%s\ngot:\n%s %s", id, got.Method, got.Path)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "404") {
			t.Fatalf("\nwanted:\nstderr naming 404\ngot:\n%s", stderr)
		}
		if strings.Contains(stderr, body) {
			t.Fatalf("\nwanted:\nshort stderr without the HTTP body\ngot:\n%s", stderr)
		}
	})

	t.Run("should normalize a JSON control API error", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "01938032-1b17-7243-b035-e6a9f4645904"
		body := `{"error":"not_found"}`
		startCannedControlAPI(t, configDir, "work", http.StatusNotFound, body)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "get", "--json", id)
		assertJSONCommandError(t, stdout, stderr, err, "getting traffic: not_found")
	})

	t.Run("should fail and name the instance when the control listener is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", "0193802f-f0e7-73d9-a764-06d21e367809")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "work") {
			t.Fatalf("\nwanted:\nerror naming work\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "work") {
			t.Fatalf("\nwanted:\nstderr naming work\ngot:\n%s", stderr)
		}
	})

	t.Run("should print a JSON error when the control listener is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "get", "--json", "0193802f-f0e7-73d9-a764-06d21e367809")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")
	})

	t.Run("should require exactly one uuid", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "accepts 1 arg(s), received 0") {
			t.Fatalf("\nwanted:\naccepts 1 arg(s), received 0\ngot:\n%v", err)
		}

		_, _, err = executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", "0193802f-f0e7-73d9-a764-06d21e367809", "extra")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "accepts 1 arg(s), received 2") {
			t.Fatalf("\nwanted:\naccepts 1 arg(s), received 2\ngot:\n%v", err)
		}
	})

	t.Run("should reject --project", func(t *testing.T) {
		stdout, stderr, err := executeRoot(t, "--config-dir", serviceConfigDir(t), "traffic", "get", "--project", "scratchpad", "0193802f-f0e7-73d9-a764-06d21e367809")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "unknown flag: --project") {
			t.Fatalf("\nwanted:\nunknown flag: --project\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "unknown flag: --project") {
			t.Fatalf("\nwanted:\nstderr naming unknown flag\ngot:\n%s", stderr)
		}
	})
}

func TestLaunchpadCommands(t *testing.T) {
	binary := buildMarasi(t)
	id := "01938032-1b17-7243-b035-e6a9f4645904"
	requestID := "01938033-298e-73dc-b640-eb321b621154"

	for _, test := range []struct {
		name       string
		args       []string
		response   string
		method     string
		path       string
		body       string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "create",
			args:       []string{"launchpad", "create", "--name", "Login", "--description", "Try variants"},
			response:   `{"id":"` + id + `","name":"Login","description":"Try variants"}` + "\n",
			method:     http.MethodPost,
			path:       "/launchpad",
			body:       `{"name":"Login","description":"Try variants"}`,
			wantStderr: "launchpad " + id + " created successfully\n",
		},
		{
			name:       "list",
			args:       []string{"launchpad", "list"},
			response:   `{"items":[{"id":"` + id + `","name":"Login","description":"Try variants","entry_count":500,"last_status_code":404}]}` + "\n",
			method:     http.MethodGet,
			path:       "/launchpad",
			wantStdout: id + "  Login  Try variants  500  404\n",
		},
		{
			name:       "list empty and in-flight launchpads",
			args:       []string{"launchpad", "list"},
			response:   `{"items":[{"id":"` + id + `","name":"Empty","description":"","entry_count":0,"last_status_code":null},{"id":"` + requestID + `","name":"Sending","description":"","entry_count":2,"last_status_code":null}]}` + "\n",
			method:     http.MethodGet,
			path:       "/launchpad",
			wantStdout: id + "  Empty      0  -\n" + requestID + "  Sending    2  -\n",
		},
		{
			name:       "get",
			args:       []string{"launchpad", "get", id},
			response:   `{"id":"` + id + `","name":"Login","description":"Try variants","items":[]}` + "\n",
			method:     http.MethodGet,
			path:       "/launchpad/" + id,
			wantStdout: "id: " + id + "\nname: Login\ndescription: Try variants\n",
		},
		{
			name:       "update",
			args:       []string{"launchpad", "update", id, "--description", ""},
			response:   `{"id":"` + id + `","name":"Login","description":""}` + "\n",
			method:     http.MethodPost,
			path:       "/launchpad/" + id,
			body:       `{"description":""}`,
			wantStderr: "launchpad " + id + " updated successfully\n",
		},
		{
			name:       "link",
			args:       []string{"launchpad", "link", id, "--request", requestID},
			response:   `{"launchpad_id":"` + id + `","request_id":"` + requestID + `"}` + "\n",
			method:     http.MethodPost,
			path:       "/launchpad/" + id + "/link",
			body:       `{"request_id":"` + requestID + `"}`,
			wantStderr: "request " + requestID + " linked to launchpad " + id + " successfully\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			if got.Method != test.method || got.Path != test.path || got.Body != test.body {
				t.Fatalf("\nwanted:\n%s %s body %q\ngot:\n%s %s body %q", test.method, test.path, test.body, got.Method, got.Path, got.Body)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q", test.wantStdout, test.wantStderr, stdout, stderr)
			}
		})
	}

	t.Run("should pass control API success through in JSON mode before or after the subcommand", func(t *testing.T) {
		for _, args := range [][]string{{"--json", "launchpad", "list"}, {"launchpad", "list", "--json"}} {
			configDir := serviceConfigDir(t)
			body := " {\n  \"items\": []\n} "
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", body, stdout, stderr, err)
			}
		}
	})

	t.Run("should show linked members on get", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"id":"` + id + `","name":"Login","description":"Try variants","items":[{"id":"` + requestID + `","method":"POST","host":"example.com","path":"/login","status_code":200,"length":"2"}]}` + "\n"
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "get", id)
		want := "id: " + id + "\nname: Login\ndescription: Try variants\n" + requestID + "  POST  example.com  /login  200  2\n"
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	for _, flag := range []string{"--query", "-q"} {
		t.Run("should send get "+flag+" as the q query parameter", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"id":"`+id+`","name":"Login","description":"","items":[]}`)
			_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "launchpad", "get", id, flag, `response_body:"token" AND status_code >= 500`)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			want := "q=response_body%3A%22token%22+AND+status_code+%3E%3D+500"
			if got.Method != http.MethodGet || got.Path != "/launchpad/"+id || got.RawQuery != want {
				t.Fatalf("\nwanted:\nGET /launchpad/%s?%s\ngot:\n%s %s?%s", id, want, got.Method, got.Path, got.RawQuery)
			}
		})
	}

	t.Run("should send get --exclude as exclude query parameters", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"id":"`+id+`","name":"Login","description":"","items":[]}`)
		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "launchpad", "get", id, "-q", "status_code = 200", "--exclude", "image/*", "--exclude", "font/*")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		want := "exclude=image%2F%2A&exclude=font%2F%2A&q=status_code+%3D+200"
		if got.Method != http.MethodGet || got.Path != "/launchpad/"+id || got.RawQuery != want {
			t.Fatalf("\nwanted:\nGET /launchpad/%s?%s\ngot:\n%s %s?%s", id, want, got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should send no q query parameter on get without --query", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"id":"`+id+`","name":"Login","description":"","items":[]}`)
		if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "get", id); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got := sent.snapshot(); got.RawQuery != "" {
			t.Fatalf("\nwanted:\nno query\ngot:\n%s", got.RawQuery)
		}
	})

	t.Run("should print a get query error with its position", func(t *testing.T) {
		want := `getting launchpad: invalid_query: "nope" is not a launchpad id; use a launchpad UUID at position 13`
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"invalid_query","message":"\"nope\" is not a launchpad id; use a launchpad UUID","position":13}`)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "get", id, "-q", `launchpad = "nope"`)
		if err == nil || stdout != "" || !strings.Contains(stderr, want) {
			t.Fatalf("\nwanted:\nstderr containing %s\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "get", id, "-q", `launchpad = "nope"`, "--json")
		assertJSONCommandError(t, stdout, stderr, err, want)
	})

	t.Run("should pass link success through in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"launchpad_id":"` + id + `","request_id":"` + requestID + `"}` + "\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "link", id, "--request", requestID, "--json")
		got := sent.snapshot()
		if err != nil || stdout != body || stderr != "" || got.Body != `{"request_id":"`+requestID+`"}` {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, body with request id, nil error\ngot:\nstdout %q, stderr %q, body %q, error %v", body, stdout, stderr, got.Body, err)
		}
	})

	t.Run("should launch raw from a file", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		raw := []byte("POST /login HTTP/1.1\r\nHost: example.com\r\nContent-Length: 4\r\n\r\nbody")
		rawFile := filepath.Join(t.TempDir(), "request.raw")
		if err := os.WriteFile(rawFile, raw, 0o600); err != nil {
			t.Fatalf("writing raw request: %v", err)
		}
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, "{\"launch_id\":\"01938034-298e-73dc-b640-eb321b621154\",\"status\":\"pending\"}\n")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "launch", id, "--scheme", "http", "--raw-file", rawFile, "--detach")
		wantBody := `{"raw":"` + base64.StdEncoding.EncodeToString(raw) + `","scheme":"http"}`
		got := sent.snapshot()
		if err != nil || stdout != "" || stderr != "launch 01938034-298e-73dc-b640-eb321b621154 pending\n" || got.Method != http.MethodPost || got.Path != "/launchpad/"+id+"/launch" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nPOST launch body %q and success on stderr\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", wantBody, got.Method, got.Path, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should launch piped stdin with JSON passthrough", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		raw := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
		body := "{\"launch_id\":\"01938034-298e-73dc-b640-eb321b621154\",\"status\":\"pending\"}\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "launch", id, "--scheme", "https", "--json", "--detach")
		command.Stdin = bytes.NewReader(raw)
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		wantBody := `{"raw":"` + base64.StdEncoding.EncodeToString(raw) + `","scheme":"https"}`
		got := sent.snapshot()
		if err != nil || stdout.String() != body || stderr.String() != "" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, body %q\ngot:\nstdout %q, stderr %q, body %q, error %v", body, wantBody, stdout.String(), stderr.String(), got.Body, err)
		}
	})

	t.Run("should reject missing required create and update flags", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{{"launchpad", "create"}, {"launchpad", "create", "--name", ""}, {"launchpad", "update", id}, {"launchpad", "link", id}, {"launchpad", "launch", id}, {"launchpad", "launch", id, "--scheme", "ftp"}, {"launchpad", "launch", id, "--scheme", "http"}} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should report a missing instance and normalize JSON API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")

		startCannedControlAPI(t, configDir, "api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "api", "launchpad", "get", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting launchpad: not_found")
	})
}

func TestLaunchpadCancelCommand(t *testing.T) {
	binary := buildMarasi(t)
	launchID := "01938034-298e-73dc-b640-eb321b621154"
	cancelled := `{"launch_id":"` + launchID + `","launchpad_id":"01938032-1b17-7243-b035-e6a9f4645904","status":"cancelled"}` + "\n"

	t.Run("should cancel a launch by id", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, cancelled)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "cancel", launchID)

		got := sent.snapshot()
		if err != nil || stdout != "" || stderr != "launch "+launchID+" cancelled\n" || got.Method != http.MethodPost || got.Path != "/launch/"+launchID+"/cancel" {
			t.Fatalf("\nwanted:\nPOST /launch/%s/cancel and success on stderr\ngot:\n%s %s, stdout %q, stderr %q, error %v", launchID, got.Method, got.Path, stdout, stderr, err)
		}
	})

	t.Run("should pass the cancelled record through in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, cancelled)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "cancel", launchID, "--json")

		if err != nil || stdout != cancelled || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", cancelled, stdout, stderr, err)
		}
	})

	t.Run("should exit non-zero with the final status when the launch already finished", func(t *testing.T) {
		finished := `{"error":"launch_finished","launch_id":"` + launchID + `","launchpad_id":"01938032-1b17-7243-b035-e6a9f4645904","status":"failed","reason":"upstream_error"}` + "\n"
		want := "cancelling launch: launch_finished: launch " + launchID + " is failed (upstream_error)"

		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusConflict, finished)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "cancel", launchID)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || stdout != "" || !strings.Contains(stderr, want) {
			t.Fatalf("\nwanted:\nexit 1 with stderr containing %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "cancel", launchID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, want)
	})

	t.Run("should exit non-zero with not_found for an unknown launch", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusNotFound, `{"error":"not_found"}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "cancel", launchID)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || stdout != "" || !strings.Contains(stderr, "cancelling launch: not_found") {
			t.Fatalf("\nwanted:\nexit 1 with stderr containing %q\ngot:\nstdout %q, stderr %q, error %v", "cancelling launch: not_found", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "cancel", launchID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "cancelling launch: not_found")
	})

	t.Run("should reject a missing or malformed launch id", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{{"launchpad", "cancel"}, {"launchpad", "cancel", "not-a-uuid"}} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})
}

func TestLaunchpadPendingCommand(t *testing.T) {
	binary := buildMarasi(t)
	launchpadID := "01938032-1b17-7243-b035-e6a9f4645904"
	first := "01938034-298e-73dc-b640-eb321b621154"
	second := "01938035-298e-73dc-b640-eb321b621154"
	pending := `{"items":[{"launch_id":"` + first + `","launchpad_id":"` + launchpadID + `","status":"pending"},{"launch_id":"` + second + `","launchpad_id":"` + launchpadID + `","status":"pending"}]}` + "\n"

	t.Run("should list pending launches one per row", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, pending)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "pending")

		want := first + "  " + launchpadID + "  pending\n" + second + "  " + launchpadID + "  pending\n"
		got := sent.snapshot()
		if err != nil || stdout != want || stderr != "" || got.Method != http.MethodGet || got.Path != "/launch" {
			t.Fatalf("\nwanted:\nGET /launch and stdout %q\ngot:\n%s %s, stdout %q, stderr %q, error %v", want, got.Method, got.Path, stdout, stderr, err)
		}
	})

	t.Run("should print nothing when no launch is pending", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[]}`+"\n")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "pending")

		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty stdout and stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should pass the list through in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, pending)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "pending", "--json")

		if err != nil || stdout != pending || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", pending, stdout, stderr, err)
		}
	})

	t.Run("should reject arguments", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		if _, _, err := runMarasi(binary, "--config-dir", configDir, "launchpad", "pending", first); err == nil {
			t.Fatal("\nwanted:\ninvalid invocation\ngot:\naccepted an argument")
		}
	})
}

func TestLaunchpadStatusCommand(t *testing.T) {
	binary := buildMarasi(t)
	launchpadID := "01938032-1b17-7243-b035-e6a9f4645904"
	launchID := "01938034-298e-73dc-b640-eb321b621154"
	pending := `{"launch_id":"` + launchID + `","launchpad_id":"` + launchpadID + `","status":"pending"}` + "\n"
	failed := `{"launch_id":"` + launchID + `","launchpad_id":"` + launchpadID + `","status":"failed","reason":"upstream_error"}` + "\n"

	t.Run("should print a pending launch", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, pending)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "status", launchID)

		want := "launch_id: " + launchID + "\nlaunchpad_id: " + launchpadID + "\nstatus: pending\n"
		got := sent.snapshot()
		if err != nil || stdout != want || stderr != "" || got.Method != http.MethodGet || got.Path != "/launch/"+launchID {
			t.Fatalf("\nwanted:\nGET /launch/%s and stdout %q\ngot:\n%s %s, stdout %q, stderr %q, error %v", launchID, want, got.Method, got.Path, stdout, stderr, err)
		}
	})

	t.Run("should print a failed launch with its reason and exit zero", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, failed)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "status", launchID)

		want := "launch_id: " + launchID + "\nlaunchpad_id: " + launchpadID + "\nstatus: failed\nreason: upstream_error\n"
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should pass the record through in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, failed)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "status", launchID, "--json")

		if err != nil || stdout != failed || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", failed, stdout, stderr, err)
		}
	})

	t.Run("should exit non-zero with not_found for an unknown launch", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusNotFound, `{"error":"not_found"}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "status", launchID)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || stdout != "" || !strings.Contains(stderr, "reading launch: not_found") {
			t.Fatalf("\nwanted:\nexit 1 with stderr containing %q\ngot:\nstdout %q, stderr %q, error %v", "reading launch: not_found", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "launchpad", "status", launchID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "reading launch: not_found")
	})

	t.Run("should reject a missing or malformed launch id", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{{"launchpad", "status"}, {"launchpad", "status", "not-a-uuid"}} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})
}

// fakeLaunchService serves a launch start, then answers each read of the launch
// with the next record in reads, repeating the last one. Cancel answers with
// cancelStatus and cancelBody. readSeen is closed on the first read.
type fakeLaunchService struct {
	sent     *cannedControlRequest
	readSeen chan struct{}
}

func startFakeLaunchService(t *testing.T, configDir, launchpadID, launchID string, reads []string, cancelStatus int, cancelBody string) *fakeLaunchService {
	t.Helper()
	fake := &fakeLaunchService{readSeen: make(chan struct{})}
	var mu sync.Mutex
	readCount := 0
	fake.sent = startCannedControlAPIHandler(t, configDir, "work", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/launchpad/"+launchpadID+"/launch":
			io.WriteString(w, `{"launch_id":"`+launchID+`","status":"pending"}`+"\n")
		case r.Method == http.MethodGet && r.URL.Path == "/launch/"+launchID:
			mu.Lock()
			read := reads[min(readCount, len(reads)-1)]
			if readCount == 0 {
				close(fake.readSeen)
			}
			readCount++
			mu.Unlock()
			if read == "" {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":"not_found"}`+"\n")
				return
			}
			io.WriteString(w, read)
		case r.Method == http.MethodPost && r.URL.Path == "/launch/"+launchID+"/cancel":
			w.WriteHeader(cancelStatus)
			io.WriteString(w, cancelBody)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not_found"}`+"\n")
		}
	})
	return fake
}

// paths lists the method and path of every request the fake service received.
func (fake *fakeLaunchService) paths() []string {
	var paths []string
	for _, request := range fake.sent.requests() {
		paths = append(paths, request.Method+" "+request.Path)
	}
	return paths
}

func TestLaunchpadLaunchCommand(t *testing.T) {
	binary := buildMarasi(t)
	launchpadID := "01938032-1b17-7243-b035-e6a9f4645904"
	launchID := "01938034-298e-73dc-b640-eb321b621154"
	record := func(status, reason string) string {
		body := `{"launch_id":"` + launchID + `","launchpad_id":"` + launchpadID + `","status":"` + status + `"`
		if reason != "" {
			body += `,"reason":"` + reason + `"`
		}
		return body + "}\n"
	}
	pending := record("pending", "")
	raw := "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"
	launch := func(configDir string, extra ...string) *exec.Cmd {
		args := append([]string{"--config-dir", configDir, "--instance", "work", "launchpad", "launch", launchpadID, "--scheme", "http"}, extra...)
		command := exec.Command(binary, args...)
		command.Stdin = strings.NewReader(raw)
		return command
	}
	run := func(command *exec.Cmd) (string, string, error) {
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		return stdout.String(), stderr.String(), err
	}

	t.Run("should wait for the launch and print its final record", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		fake := startFakeLaunchService(t, configDir, launchpadID, launchID, []string{pending, pending, record("completed", "")}, http.StatusOK, "")

		stdout, stderr, err := run(launch(configDir))

		want := "launch " + launchID + " pending\nlaunch " + launchID + " completed\n"
		if err != nil || stdout != "" || stderr != want {
			t.Fatalf("\nwanted:\nstderr %q, empty stdout, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
		for _, path := range fake.paths() {
			if strings.HasSuffix(path, "/cancel") {
				t.Fatalf("\nwanted:\nno cancel\ngot:\n%v", fake.paths())
			}
		}
	})

	t.Run("should write only the final record in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		completed := record("completed", "")
		startFakeLaunchService(t, configDir, launchpadID, launchID, []string{pending, completed}, http.StatusOK, "")

		stdout, stderr, err := run(launch(configDir, "--json"))

		if err != nil || stdout != completed || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", completed, stdout, stderr, err)
		}
	})

	t.Run("should exit non-zero at once when the launch cannot be read", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		fake := startFakeLaunchService(t, configDir, launchpadID, launchID, []string{"", record("cancelled", "")}, http.StatusOK, "")

		stdout, stderr, err := run(launch(configDir, "--json"))

		assertJSONCommandError(t, stdout, stderr, err, "reading launch "+launchID+": not_found")
		if paths := fake.paths(); len(paths) != 2 {
			t.Fatalf("\nwanted:\none start and one read\ngot:\n%v", paths)
		}
	})

	t.Run("should name the launch when a waiting launch can no longer be read", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startFakeLaunchService(t, configDir, launchpadID, launchID, []string{pending, ""}, http.StatusOK, "")
		want := "reading launch " + launchID + ": 404 Not Found"

		stdout, stderr, err := run(launch(configDir))

		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || stdout != "" || !strings.Contains(stderr, want) {
			t.Fatalf("\nwanted:\nexit 1 with stderr containing %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should exit non-zero when the launch failed", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startFakeLaunchService(t, configDir, launchpadID, launchID, []string{pending, record("failed", "network_error")}, http.StatusOK, "")
		want := "launching launchpad request: launch " + launchID + " failed (network_error)"

		stdout, stderr, err := run(launch(configDir))
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || stdout != "" || !strings.Contains(stderr, want) {
			t.Fatalf("\nwanted:\nexit 1 with stderr containing %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}

		stdout, stderr, err = run(launch(configDir, "--json"))
		assertJSONCommandError(t, stdout, stderr, err, want)
	})

	interrupt := func(t *testing.T, fake *fakeLaunchService, command *exec.Cmd) (string, string, error) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatalf("starting launch command: %v", err)
		}
		select {
		case <-fake.readSeen:
		case <-time.After(5 * time.Second):
			command.Process.Kill()
			t.Fatal("launch command did not read its launch")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			command.Process.Kill()
			t.Fatalf("interrupting launch command: %v", err)
		}
		err := command.Wait()
		return stdout.String(), stderr.String(), err
	}

	for name, args := range map[string][]string{"human": nil, "JSON": {"--json"}} {
		t.Run("should cancel the launch on interrupt in "+name+" mode", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("os.Interrupt cannot be sent to a Windows process")
			}
			configDir := serviceConfigDir(t)
			cancelled := record("cancelled", "")
			fake := startFakeLaunchService(t, configDir, launchpadID, launchID, []string{pending}, http.StatusOK, cancelled)

			stdout, stderr, err := interrupt(t, fake, launch(configDir, args...))

			wantStdout, wantStderr := "", "launch "+launchID+" pending\nlaunch "+launchID+" cancelled\n"
			if len(args) > 0 {
				wantStdout, wantStderr = cancelled, ""
			}
			paths := fake.paths()
			if err != nil || stdout != wantStdout || stderr != wantStderr || paths[len(paths)-1] != "POST /launch/"+launchID+"/cancel" {
				t.Fatalf("\nwanted:\ncancel request, stdout %q, stderr %q, nil error\ngot:\nrequests %v, stdout %q, stderr %q, error %v", wantStdout, wantStderr, paths, stdout, stderr, err)
			}
		})
	}

	t.Run("should exit non-zero when the interrupted launch already finished", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.Interrupt cannot be sent to a Windows process")
		}
		configDir := serviceConfigDir(t)
		finished := `{"error":"launch_finished","launch_id":"` + launchID + `","launchpad_id":"` + launchpadID + `","status":"completed"}` + "\n"
		fake := startFakeLaunchService(t, configDir, launchpadID, launchID, []string{pending}, http.StatusConflict, finished)

		stdout, stderr, err := interrupt(t, fake, launch(configDir, "--json"))

		assertJSONCommandError(t, stdout, stderr, err, "cancelling launch: launch_finished: launch "+launchID+" is completed")
	})

	t.Run("should print the start response and return with --detach", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		fake := startFakeLaunchService(t, configDir, launchpadID, launchID, []string{pending}, http.StatusOK, "")

		stdout, stderr, err := run(launch(configDir, "--detach"))
		if err != nil || stdout != "" || stderr != "launch "+launchID+" pending\n" || len(fake.paths()) != 1 {
			t.Fatalf("\nwanted:\none start request and the pending line on stderr\ngot:\nrequests %v, stdout %q, stderr %q, error %v", fake.paths(), stdout, stderr, err)
		}

		stdout, stderr, err = run(launch(configDir, "--detach", "--json"))
		want := `{"launch_id":"` + launchID + `","status":"pending"}` + "\n"
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})
}

// controlRequest is one request the canned control API received.
type controlRequest struct {
	Method      string
	Path        string
	RawQuery    string
	Body        string
	ContentType string
}

type cannedControlRequest struct {
	mu sync.Mutex
	controlRequest
	history []controlRequest
}

func (got *cannedControlRequest) snapshot() controlRequest {
	got.mu.Lock()
	defer got.mu.Unlock()
	return got.controlRequest
}

func (got *cannedControlRequest) requests() []controlRequest {
	got.mu.Lock()
	defer got.mu.Unlock()
	out := make([]controlRequest, len(got.history))
	copy(out, got.history)
	return out
}

func (got *cannedControlRequest) reset() {
	got.mu.Lock()
	defer got.mu.Unlock()
	got.controlRequest = controlRequest{}
	got.history = nil
}

func startCannedControlAPI(t *testing.T, configDir, name string, status int, body string) *cannedControlRequest {
	t.Helper()
	return startCannedControlAPIHandler(t, configDir, name, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func startCannedControlAPIHandler(t *testing.T, configDir, name string, handle http.HandlerFunc) *cannedControlRequest {
	t.Helper()
	socketPath, lockPath, err := instanceResourcePaths(configDir, name)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	listener, lock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	got := &cannedControlRequest{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.Method = r.Method
		got.Path = r.URL.Path
		got.RawQuery = r.URL.RawQuery
		got.Body = string(requestBody)
		got.ContentType = r.Header.Get("Content-Type")
		got.history = append(got.history, got.controlRequest)
		got.mu.Unlock()
		handle(w, r)
	})}
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Close()
		releaseInstance(listener, socketPath, lock)
	})
	return got
}

func executeRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	oldJSON := jsonOutput
	oldQuery, oldLimit, oldCursor := trafficListQueryText, trafficListLimit, trafficListCursor
	oldExclude, oldAsOf, oldBackward := trafficListExclude, trafficListAsOf, trafficListBackward
	oldLaunchpadQuery, oldLaunchpadExclude := launchpadGetQuery, launchpadGetExclude
	oldConfigDir, oldInstance, oldInstancePath := configDir, instance, instancePath
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	t.Cleanup(func() {
		jsonOutput = oldJSON
		trafficListQueryText, trafficListLimit, trafficListCursor = oldQuery, oldLimit, oldCursor
		trafficListExclude, trafficListAsOf, trafficListBackward = oldExclude, oldAsOf, oldBackward
		launchpadGetQuery, launchpadGetExclude = oldLaunchpadQuery, oldLaunchpadExclude
		configDir, instance, instancePath = oldConfigDir, oldInstance, oldInstancePath
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetContext(nil)
	})
	err = executeCommand(args)
	return outBuf.String(), errBuf.String(), err
}
