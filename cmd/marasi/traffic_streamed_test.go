package main

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestTrafficStreamedOutput(t *testing.T) {
	t.Run("should print a streamed pair's framing and ending per direction after the protocols", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"` + id + `","number":42,"note":"","metadata":{},"request":{"scheme":"https","method":"POST","host":"example.com","path":"/a","raw":"","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"","status_code":-1,"content_type":"","length":"","raw":"","responded_at":null},"upstream_proxy":null,"client_protocol":"h2","origin_protocol":"h2",` +
			`"streamed":{"request":{"framing":"grpc","ending":"complete"},"response":{"framing":"grpc","ending":null}}}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "origin_protocol: h2\nstreamed: request grpc (complete), response grpc (live)\nmethod: POST\n"
		if !strings.Contains(stdout, want) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should print a pair's joined raw request and how many messages it joins", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		raw := "POST /svc/M HTTP/2\r\n:authority: a\r\n\r\n\x00\x00\x00\x00\x01x"
		got := startCannedControlAPI(t, configDir, "work", http.StatusOK,
			`{"id":"`+id+`","raw":"`+base64.StdEncoding.EncodeToString([]byte(raw))+`","streamed":true,"messages":1,"omitted":2}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "request", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Method != http.MethodGet || got.Path != "/traffic/"+id+"/request/joined" {
			t.Fatalf("the request\nwanted:\nGET /traffic/%s/request/joined\ngot:\n%s %s", id, got.Method, got.Path)
		}
		if stdout != raw || stderr != "joined 1 request messages, 2 left out\n" {
			t.Fatalf("\nwanted:\n%q and the count on stderr\ngot:\n%q %q", raw, stdout, stderr)
		}
	})

	t.Run("should explain a pair whose request is still open", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusConflict, `{"error":"stream_request_open","message":"the streamed pair's request direction is still being carried; join it once the request ends"}`)
		_, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "request", "0193802f-f0e7-73d9-a764-06d21e367809")
		if err == nil || !strings.Contains(err.Error(), "stream_request_open") {
			t.Fatalf("\nwanted:\nstream_request_open\ngot:\n%v", err)
		}
	})

	t.Run("should document the streamed query fields in help", func(t *testing.T) {
		stdout, _, err := executeRoot(t, "traffic", "list", "--help")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		for _, want := range []string{"streamed:* matches streamed pairs", "request_framing, response_framing", `response_framing = "sse"`} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("\nwanted help to contain:\n%s\ngot:\n%s", want, stdout)
			}
		}
	})
}
