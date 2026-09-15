package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestTrafficListCommand(t *testing.T) {
	t.Run("should print aligned rows for the newest page", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"0193802f-f0e7-73d9-a764-06d21e367809","scheme":"https","method":"GET","host":"example.com","path":"/a","status":"200 OK","status_code":200,"content_type":"application/json","length":"12","metadata":{"foo":"bar"},"requested_at":"2026-01-02T03:04:05Z","responded_at":"2026-01-02T03:04:06Z"},{"id":"01938032-1b17-7243-b035-e6a9f4645904","scheme":"https","method":"POST","host":"example.com","path":"/login","status":"401 Unauthorized","status_code":401,"content_type":"text/plain","length":"45","metadata":{},"requested_at":"2026-01-02T03:04:04Z","responded_at":"2026-01-02T03:04:05Z"}],"next_cursor":null}`)

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "limit=200" {
			t.Fatalf("\nwanted:\nGET /traffic?limit=200\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
		want := "0193802f-f0e7-73d9-a764-06d21e367809  GET   example.com  /a      200  12\n01938032-1b17-7243-b035-e6a9f4645904  POST  example.com  /login  401  45\n"
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

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--cursor", "not-a-uuid")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		got := sent.snapshot()
		if got.RawQuery != "cursor=not-a-uuid&limit=200" {
			t.Fatalf("\nwanted:\ncursor=not-a-uuid&limit=200\ngot:\n%s", got.RawQuery)
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

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list", "--json", "--limit", "0")
		got := sent.snapshot()
		if got.RawQuery != "limit=0" {
			t.Fatalf("\nwanted:\nlimit=0\ngot:\n%s", got.RawQuery)
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

	t.Run("should send --cursor as the cursor query parameter", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--cursor", "0193802f-f0e7-73d9-a764-06d21e367809")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "cursor=0193802f-f0e7-73d9-a764-06d21e367809&limit=200" {
			t.Fatalf("\nwanted:\nGET /traffic?cursor=0193802f-f0e7-73d9-a764-06d21e367809&limit=200\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
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

	t.Run("should send --path as the path query parameter", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--path", "/api")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "limit=200&path=%2Fapi" {
			t.Fatalf("\nwanted:\nGET /traffic?limit=200&path=%%2Fapi\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should send --status-code as the status_code query parameter", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--status-code", "404")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "limit=200&status_code=404" {
			t.Fatalf("\nwanted:\nGET /traffic?limit=200&status_code=404\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should send --method as the method query parameter", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--method", "POST")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "limit=200&method=POST" {
			t.Fatalf("\nwanted:\nGET /traffic?limit=200&method=POST\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should send --host as the host query parameter", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--host", "example.com")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/traffic" || got.RawQuery != "host=example.com&limit=200" {
			t.Fatalf("\nwanted:\nGET /traffic?host=example.com&limit=200\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
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
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9hCg==","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"N/A","status_code":-1,"content_type":"","length":"0","raw":null,"responded_at":"0001-01-01T00:00:00Z"}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "id: 0193802f-f0e7-73d9-a764-06d21e367809\nscheme: https\nmethod: GET\nhost: example.com\npath: /a\nrequested_at: 2026-01-02T03:04:05Z\nstatus: N/A\nstatus_code: -1\ncontent_type: \nlength: 0\nresponded_at: 0001-01-01T00:00:00Z\nGET /a\nno response yet\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should start no response yet on a new line", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9h","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"N/A","status_code":-1,"content_type":"","length":"0","raw":null,"responded_at":"0001-01-01T00:00:00Z"}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "id: 0193802f-f0e7-73d9-a764-06d21e367809\nscheme: https\nmethod: GET\nhost: example.com\npath: /a\nrequested_at: 2026-01-02T03:04:05Z\nstatus: N/A\nstatus_code: -1\ncontent_type: \nlength: 0\nresponded_at: 0001-01-01T00:00:00Z\nGET /a\nno response yet\n"
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

type cannedControlRequest struct {
	mu       sync.Mutex
	Method   string
	Path     string
	RawQuery string
	Body     string
}

func (got *cannedControlRequest) snapshot() cannedControlRequest {
	got.mu.Lock()
	defer got.mu.Unlock()
	return cannedControlRequest{
		Method:   got.Method,
		Path:     got.Path,
		RawQuery: got.RawQuery,
		Body:     got.Body,
	}
}

func (got *cannedControlRequest) reset() {
	got.mu.Lock()
	defer got.mu.Unlock()
	got.Method, got.Path, got.RawQuery, got.Body = "", "", "", ""
}

func startCannedControlAPI(t *testing.T, configDir, name string, status int, body string) *cannedControlRequest {
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
		got.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
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
	oldHost, oldMethod, oldStatusCode, oldPath, oldLimit, oldCursor := trafficListHost, trafficListMethod, trafficListStatusCode, trafficListPath, trafficListLimit, trafficListCursor
	oldConfigDir, oldInstance, oldInstancePath := configDir, instance, instancePath
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	t.Cleanup(func() {
		jsonOutput = oldJSON
		trafficListHost, trafficListMethod, trafficListStatusCode, trafficListPath, trafficListLimit, trafficListCursor = oldHost, oldMethod, oldStatusCode, oldPath, oldLimit, oldCursor
		configDir, instance, instancePath = oldConfigDir, oldInstance, oldInstancePath
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetContext(nil)
	})
	err = executeCommand(args)
	return outBuf.String(), errBuf.String(), err
}
