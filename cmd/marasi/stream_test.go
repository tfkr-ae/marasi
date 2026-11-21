package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestStreamCommands(t *testing.T) {
	const (
		pairID   = "0190f0c4-7b6e-7000-8000-000000000001"
		firstID  = "0190f0c4-7b6e-7000-8000-000000000002"
		secondID = "0190f0c4-7b6e-7000-8000-000000000003"
	)
	page := `{"items":[` +
		`{"id":"` + firstID + `","pair_id":"` + pairID + `","direction":"request","framing":"grpc","flags":0,"payload":"aGVsbG8=","compressed":false,"created_at":"2026-11-20T10:00:00Z","metadata":{}},` +
		`{"id":"` + secondID + `","pair_id":"` + pairID + `","direction":"response","framing":"grpc","flags":1,"payload":"CAE=","compressed":true,"created_at":"2026-11-20T10:00:01Z","metadata":{"too_large":true,"size_seen":70000000}}` +
		`],"next_cursor":"` + secondID + `"}` + "\n"

	t.Run("should page a pair's stream messages oldest first, with a preview of each", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, page)
		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "stream", "messages", pairID, "--limit", "2", "--cursor", firstID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v %s", err, stderr)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/stream/"+pairID+"/message" || got.RawQuery != "cursor="+firstID+"&limit=2" {
			t.Fatalf("\nwanted:\nGET /stream/%s/message?cursor=%s&limit=2\ngot:\n%s %s?%s", pairID, firstID, got.Method, got.Path, got.RawQuery)
		}
		header := "ID                                    DIRECTION  FRAMING  FLAGS  PREVIEW\n"
		want := header +
			firstID + "  request    grpc     0      hello\n" +
			secondID + "  response   grpc     1      too large: 70000000 bytes\n" +
			header
		if stdout != want || stderr != "next_cursor="+secondID+"\n" {
			t.Fatalf("\nwanted:\nstdout %q\nstderr %q\ngot:\nstdout %q\nstderr %q", want, "next_cursor="+secondID+"\n", stdout, stderr)
		}
	})

	t.Run("should pass the page through with --json", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, page)
		stdout, _, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "--json", "stream", "messages", pairID)
		if err != nil || stdout != page {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q %v", page, stdout, err)
		}
	})

	t.Run("should get one stream message by the pair's and its own ID", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		message := `{"id":"` + secondID + `","pair_id":"` + pairID + `","direction":"response","framing":"grpc","flags":1,"payload":"CAE=","compressed":true,"created_at":"2026-11-20T10:00:01Z","metadata":{"edited":true}}` + "\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, message)
		stdout, _, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "stream", "get", pairID, secondID)
		if err != nil {
			t.Fatal(err)
		}
		if got := sent.snapshot(); got.Path != "/stream/"+pairID+"/message/"+secondID {
			t.Fatalf("\nwanted:\nGET /stream/%s/message/%s\ngot:\n%s", pairID, secondID, got.Path)
		}
		want := "id: " + secondID + "\npair_id: " + pairID + "\ndirection: response\nframing: grpc\nflags: 1\ncompressed: true\ncreated_at: 2026-11-20T10:00:01Z\nmetadata: {\"edited\":true}\npayload: binary 2 bytes (--json gives them base64)\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, stdout)
		}
	})

	t.Run("should refuse an ID that is not a UUID before dialling, and name the operation on API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusNotFound, `{"error":"not_found"}`)
		if _, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "stream", "get", pairID, "nope"); err == nil || !strings.Contains(stderr, `invalid uuid "nope"`) {
			t.Fatalf("\nwanted:\ninvalid uuid\ngot:\n%q %v", stderr, err)
		}
		if got := sent.snapshot(); got.Path != "" {
			t.Fatalf("\nwanted:\nno request\ngot:\n%s", got.Path)
		}
		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "stream", "messages", pairID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "listing stream messages: not_found")
	})
}
