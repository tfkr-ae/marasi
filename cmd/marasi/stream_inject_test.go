package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamInjectAndCancelCommands(t *testing.T) {
	const (
		pairID    = "0190f0c4-7b6e-7000-8000-000000000001"
		messageID = "0190f0c4-7b6e-7000-8000-000000000002"
	)
	injected := `{"id":"` + messageID + `","pair_id":"` + pairID + `","direction":"request","framing":"grpc","flags":0,"payload":"AAH/","compressed":false,"created_at":"2026-11-20T10:00:00Z","metadata":{"injected":true}}` + "\n"
	binary := buildMarasi(t)
	inject := func(configDir string, flags ...string) []string {
		return append([]string{"--config-dir", configDir, "--instance", "work", "stream", "inject", pairID}, flags...)
	}

	t.Run("should base64-encode piped bytes, name the injected message on stderr, and pass the JSON through with --json", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, injected)
		command := exec.Command(binary, inject(configDir, "--direction", "request")...)
		command.Stdin = bytes.NewReader([]byte{0, 1, 255})
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil || stdout.String() != "" || stderr.String() != "stream message "+messageID+" injected\n" {
			t.Fatalf("\nwanted:\nthe message ID on stderr\ngot:\n%q %q %v", stdout.String(), stderr.String(), err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodPost || got.Path != "/stream/"+pairID+"/inject" || got.Body != `{"direction":"request","payload":"AAH/"}` || got.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPOST inject with the piped bytes\ngot:\n%+v", got)
		}

		file := filepath.Join(t.TempDir(), "message.bin")
		if err := os.WriteFile(file, []byte("from file"), 0o600); err != nil {
			t.Fatal(err)
		}
		command = exec.Command(binary, inject(configDir, "--direction", "response", "--file", file, "--compress", "never", "--json")...)
		command.Stdin = bytes.NewReader([]byte("from pipe"))
		stdout.Reset()
		stderr.Reset()
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil || stdout.String() != injected || stderr.String() != "" {
			t.Fatalf("\nwanted:\nthe API body on stdout\ngot:\n%q %q %v", stdout.String(), stderr.String(), err)
		}
		if got := sent.snapshot(); got.Body != `{"direction":"response","payload":"ZnJvbSBmaWxl","compress":"never"}` {
			t.Fatalf("\nwanted:\nthe file's bytes, compress never\ngot:\n%+v", got)
		}
	})

	t.Run("should refuse a missing or bad flag before dialling, and name the operation on API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusConflict, `{"error":"stream_not_open","message":"the direction ended: request"}`)
		for _, flags := range [][]string{{}, {"--direction", "client"}, {"--direction", "request", "--compress", "always"}} {
			if _, stderr, err := runMarasi(binary, inject(configDir, flags...)...); err == nil || !strings.Contains(stderr, "stream inject --") {
				t.Errorf("flags %v\nwanted:\na usage error\ngot:\n%q %v", flags, stderr, err)
			}
		}
		if got := sent.snapshot(); got.Path != "" {
			t.Fatalf("\nwanted:\nno request\ngot:\n%s", got.Path)
		}
		stdout, stderr, err := runMarasi(binary, append(inject(configDir, "--direction", "request", "--file", os.DevNull), "--json")...)
		assertJSONCommandError(t, stdout, stderr, err, "injecting stream message: stream_not_open")
	})

	t.Run("should cancel a pair, printing it on stderr or passing the JSON through", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"pair_id":"` + pairID + `","cancelled":true}` + "\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "stream", "cancel", pairID)
		if err != nil || stdout != "" || stderr != "stream "+pairID+" cancelled\n" {
			t.Fatalf("\nwanted:\nthe cancel on stderr\ngot:\n%q %q %v", stdout, stderr, err)
		}
		if got := sent.snapshot(); got.Method != http.MethodPost || got.Path != "/stream/"+pairID+"/cancel" {
			t.Fatalf("\nwanted:\nPOST cancel\ngot:\n%+v", got)
		}
		stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "stream", "cancel", pairID)
		if err != nil || stdout != body || stderr != "" {
			t.Fatalf("\nwanted:\nthe API body on stdout\ngot:\n%q %q %v", stdout, stderr, err)
		}
	})

	t.Run("should name the cancel operation on API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusConflict, `{"error":"stream_not_open","message":"the pair is not a streamed pair being carried"}`)
		stdout, stderr, err := runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "stream", "cancel", pairID)
		assertJSONCommandError(t, stdout, stderr, err, "cancelling streamed pair: stream_not_open")
	})
}
