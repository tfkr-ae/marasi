package main

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamTextEditFlags(t *testing.T) {
	const (
		streamID = "019a0000-0000-7000-8000-000000000003"
		wsID     = "019a0000-0000-7000-8000-000000000004"
		pairID   = "0190f0c4-7b6e-7000-8000-000000000001"
	)
	streamItem := `{"id":"` + streamID + `","type":"stream","payload":"CJYB","text":"1:VARINT 150\n","direction":"request","framing":"grpc","pair_id":"` + pairID + `","flags":0,"compressed":false,"metadata":{},"intercepted_at":"2026-11-20T10:00:00Z"}` + "\n"
	wsItem := `{"id":"` + wsID + `","type":"websocket","payload":"` + base64.StdEncoding.EncodeToString([]byte("x")) + `","opcode":1,"direction":"client","intercepted_at":"2026-10-31T09:01:30.5Z"}` + "\n"
	binary := buildMarasi(t)
	text := "1:LEN {\"mallory\"}\n"
	file := filepath.Join(t.TempDir(), "message.txt")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("should forward a stream item with --text as its text form", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, streamItem)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "forward", streamID, "--text", "--file", file)
		history := sent.requests()
		wantBody := `{"text":"1:LEN {\"mallory\"}\n"}`
		if err != nil || stdout != "" || stderr != "checkpoint "+streamID+" forwarded\n" || len(history) != 2 || history[1].Body != wantBody {
			t.Fatalf("\nwanted:\nPOST %s\ngot:\n%#v, stdout %q, stderr %q, error %v", wantBody, history, stdout, stderr, err)
		}
	})

	t.Run("should refuse --text for an item that is not a stream message, and with several UUIDs, before forwarding", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, wsItem)
		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "forward", wsID, "--text", "--file", file)
		if history := sent.requests(); err == nil || !strings.Contains(stderr, "--text") || len(history) != 1 {
			t.Fatalf("\nwanted:\nan error naming --text after the GET only\ngot:\n%#v, stderr %q, error %v", history, stderr, err)
		}
		_, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "forward", wsID, streamID, "--text")
		if err == nil || !strings.Contains(stderr, "--text") {
			t.Fatalf("\nwanted:\nan error naming --text\ngot:\nstderr %q, error %v", stderr, err)
		}
	})

	t.Run("should inject piped text with --text as its text form", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		injected := `{"id":"` + streamID + `","pair_id":"` + pairID + `","direction":"request","framing":"grpc","flags":0,"payload":"CgdtYWxsb3J5","compressed":false,"created_at":"2026-11-20T10:00:00Z","metadata":{"injected":true}}` + "\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, injected)
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "stream", "inject", pairID, "--direction", "request", "--text")
		command.Stdin = strings.NewReader(text)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil || stderr.String() != "stream message "+streamID+" injected\n" {
			t.Fatalf("\nwanted:\nthe message ID on stderr\ngot:\n%q %q %v", stdout.String(), stderr.String(), err)
		}
		if got := sent.snapshot(); got.Body != `{"direction":"request","text":"1:LEN {\"mallory\"}\n"}` {
			t.Fatalf("\nwanted:\nthe piped text as text\ngot:\n%+v", got)
		}
	})
}
