package main

import (
	"net/http"
	"testing"
)

func TestStreamGetTextForm(t *testing.T) {
	t.Parallel()

	binary := buildMarasi(t)
	pairID := "0193802f-f0e7-73d9-a764-06d21e367809"
	messageID := "01938032-1b17-7243-b035-e6a9f4645904"
	head := `{"id":"` + messageID + `","pair_id":"` + pairID + `","direction":"request","framing":"grpc","flags":0,"payload":"CJYB","compressed":false,"created_at":"2026-11-20T09:00:00Z","metadata":{}`
	wantHead := "id: " + messageID + "\npair_id: " + pairID + "\ndirection: request\nframing: grpc\nflags: 0\ncompressed: false\ncreated_at: 2026-11-20T09:00:00Z\nmetadata: {}\npayload: binary 3 bytes (--json gives them base64)\n"

	t.Run("should print a message's text form after its payload", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "text", http.StatusOK, head+`,"text":"1:VARINT 150\n"}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "text", "stream", "get", pairID, messageID)
		got := sent.snapshot()
		want := wantHead + "text:\n1:VARINT 150\n"
		if err != nil || stdout != want || stderr != "" || got.Path != "/stream/"+pairID+"/message/"+messageID {
			t.Fatalf("\nwanted:\nGET %s\n%s\ngot:\nGET %s\nstdout %q, stderr %q, error %v", "/stream/"+pairID+"/message/"+messageID, want, got.Path, stdout, stderr, err)
		}
	})

	t.Run("should end a text form without a newline with one", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "json", http.StatusOK, head+`,"text":"{\"a\":1}"}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "json", "stream", "get", pairID, messageID)
		want := wantHead + "text:\n{\"a\":1}\n"
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\n%q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should print no text line for a message without a text form", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "none", http.StatusOK, head+`,"text":null}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "none", "stream", "get", pairID, messageID)
		if err != nil || stdout != wantHead || stderr != "" {
			t.Fatalf("\nwanted:\n%q\ngot:\nstdout %q, stderr %q, error %v", wantHead, stdout, stderr, err)
		}
	})
}
