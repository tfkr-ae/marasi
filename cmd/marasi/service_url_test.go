package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestServiceURLParsing(t *testing.T) {
	binary := buildMarasi(t)
	const id = "0193802f-f0e7-73d9-a764-06d21e367809"

	t.Run("should reject input that cannot go in the service url", func(t *testing.T) {
		cases := []struct {
			args []string
			want string
		}{
			{[]string{"traffic", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"traffic", "metadata", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"traffic", "list", "--cursor", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"traffic", "list", "--limit", "0"}, `invalid limit "0"`},
			{[]string{"traffic", "list", "--limit", "abc"}, `invalid limit "abc"`},
			{[]string{"notes", "clear", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"notes", "list", "--cursor", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"logs", "--cursor", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"logs", "--limit", "501"}, `invalid limit "501"`},
			{[]string{"checkpoint", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"checkpoint", "drop", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"websocket", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"websocket", "messages", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"websocket", "close", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"traffic", "websocket", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"extension", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"finding", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"finding", "unlink", id, "--request", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"test-case", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"launchpad", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"armory", "template", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"armory", "run", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"armory", "run", "list", "--template", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"armory", "run", "traffic", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"artifact", "get", "not-a-uuid"}, `invalid uuid "not-a-uuid"`},
			{[]string{"artifact", "upload", "--test-case", "not-a-uuid", "--file", "proof.txt"}, `invalid uuid "not-a-uuid"`},
			{[]string{"wordlist", "remove", "../passwords.txt"}, `invalid wordlist name "../passwords.txt"`},
			{[]string{"report", "template", "remove", "../dropped.md"}, `invalid report template name "../dropped.md"`},
			{[]string{"chrome", "profile", "remove", "../pentest"}, `invalid chrome profile name "../pentest"`},
		}
		for _, test := range cases {
			t.Run(strings.Join(test.args, " "), func(t *testing.T) {
				configDir := serviceConfigDir(t)
				sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, "{}\n")
				commandArgs := append([]string{"--config-dir", configDir, "--instance", "work", "--json"}, test.args...)
				stdout, stderr, err := runMarasi(binary, commandArgs...)
				assertJSONCommandError(t, stdout, stderr, err, test.want)
				if sent.snapshot().Path != "" {
					t.Fatalf("\nwanted:\nno request\ngot:\n%s %s", sent.snapshot().Method, sent.snapshot().Path)
				}
			})
		}
	})

	t.Run("should put the canonical uuid in the traffic path", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusNotFound, `{"error":"not_found"}`)
		_, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "traffic", "get", strings.ToUpper(id))
		if err == nil {
			t.Fatal("\nwanted:\nmissing traffic\ngot:\nnil")
		}
		if sent.snapshot().Path != "/traffic/"+id {
			t.Fatalf("\nwanted:\n/traffic/%s\ngot:\n%s", id, sent.snapshot().Path)
		}
	})
}
