package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebSocketListCommand(t *testing.T) {
	const (
		newID    = "01938032-1b17-7243-b035-e6a9f4645904"
		oldID    = "0193802f-f0e7-73d9-a764-06d21e367809"
		longPath = "/12345678901234567890123456789012345678901234567890"
		body     = `{"items":[{"id":"` + newID + `","request_id":"pair-new","state":"error","transport":"ws","host":"example.com","path":"` + longPath + `"},{"id":"` + oldID + `","request_id":"pair-old","state":"closed","transport":"wss","host":"other.test","path":"/short"}],"next_cursor":"` + oldID + `"}` + "\n"
	)
	t.Run("should request a page and print rows in API order with a truncated path", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "websocket", "list", "--limit", "2", "--cursor", newID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/websocket" || got.RawQuery != "cursor="+newID+"&limit=2" || got.Body != "" {
			t.Fatalf("\nwanted:\nGET /websocket?cursor=%s&limit=2 without a body\ngot:\n%s %s?%s body %q", newID, got.Method, got.Path, got.RawQuery, got.Body)
		}
		want := newID + "  pair-new  error   ws   example.com  /123456789012345678901234567890123456...\n" +
			oldID + "  pair-old  closed  wss  other.test   /short\n"
		if stdout != want || stderr != "next_cursor="+oldID+"\n" {
			t.Fatalf("\nwanted:\nstdout %q\nstderr %q\ngot:\nstdout %q\nstderr %q", want, "next_cursor="+oldID+"\n", stdout, stderr)
		}
	})

	t.Run("should send limit 200 by default and print nothing for an empty list", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`+"\n")
		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "websocket", "list")
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty stdout and stderr with no error\ngot:\nstdout %q\nstderr %q\nerror %v", stdout, stderr, err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/websocket" || got.RawQuery != "limit=200" {
			t.Fatalf("\nwanted:\nGET /websocket?limit=200\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	for _, position := range []string{"before", "after"} {
		t.Run("should pass the API body through with --json "+position+" the subcommand", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			args := []string{"--config-dir", configDir, "--instance", "work"}
			if position == "before" {
				args = append(args, "--json")
			}
			args = append(args, "websocket", "list")
			if position == "after" {
				args = append(args, "--json")
			}
			stdout, stderr, err := runMarasi(buildMarasi(t), args...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q and empty stderr with no error\ngot:\nstdout %q\nstderr %q\nerror %v", body, stdout, stderr, err)
			}
		})
	}

	t.Run("should name the missing instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "websocket", "list")
		if err == nil || !strings.Contains(err.Error(), "instance work is not running") || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("\nwanted:\nmissing instance error on stderr\ngot:\nstdout %q\nstderr %q\nerror %v", stdout, stderr, err)
		}
	})

	t.Run("should use the listing operation for API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"bad_request"}`)
		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "websocket", "list", "--json", "--limit", "0")
		assertJSONCommandError(t, stdout, stderr, err, "listing websocket connections: bad_request")
	})

	t.Run("should reject extra arguments and unsupported flags before dialing", func(t *testing.T) {
		for _, args := range [][]string{{"list", "extra"}, {"list", "--project", "elsewhere"}, {"list", "-l", "2"}} {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work", "websocket"}, args...)
			_, _, err := executeRoot(t, commandArgs...)
			if err == nil || len(sent.requests()) != 0 {
				t.Fatalf("\nwanted:\nargument error without API request\ngot:\nerror %v requests %+v", err, sent.requests())
			}
		}
	})
}

func TestWebSocketGetCommands(t *testing.T) {
	const (
		connectionID = "0193802f-f0e7-73d9-a764-06d21e367809"
		requestID    = "0193802f-f0e7-73d9-a764-06d21e36780a"
		body         = `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","request_id":"0193802f-f0e7-73d9-a764-06d21e36780a","state":"open","transport":"wss","host":"example.com","path":"/chat?room=1","started_at":"2026-01-02T03:04:05Z","closed_at":null,"close_code":0,"close_reason":""}` + "\n"
		wantHuman    = "id: " + connectionID + "\n" +
			"request_id: " + requestID + "\n" +
			"state: open\n" +
			"transport: wss\n" +
			"host: example.com\n" +
			"path: /chat?room=1\n" +
			"started_at: 2026-01-02T03:04:05Z\n" +
			"closed_at: null\n" +
			"close_code: 0\n" +
			"close_reason: \n"
	)

	for _, test := range []struct {
		name string
		args []string
		path string
	}{
		{name: "websocket get", args: []string{"websocket", "get", connectionID}, path: "/websocket/" + connectionID},
		{name: "traffic websocket", args: []string{"traffic", "websocket", requestID}, path: "/traffic/" + requestID + "/websocket"},
	} {
		t.Run(test.name+" prints the connection fields", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := executeRoot(t, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			if got.Method != http.MethodGet || got.Path != test.path || got.Body != "" {
				t.Fatalf("\nwanted:\nGET %s with no body\ngot:\n%s %s with body %q", test.path, got.Method, got.Path, got.Body)
			}
			if stdout != wantHuman || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q\nstderr %q\ngot:\nstdout %q\nstderr %q", wantHuman, "", stdout, stderr)
			}
		})
	}

	for _, test := range []struct {
		name string
		args []string
		path string
	}{
		{name: "websocket get with --json before the command", args: []string{"--json", "websocket", "get", connectionID}, path: "/websocket/" + connectionID},
		{name: "traffic websocket with --json after the command", args: []string{"traffic", "websocket", requestID, "--json"}, path: "/traffic/" + requestID + "/websocket"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasi(buildMarasi(t), args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			if got.Method != http.MethodGet || got.Path != test.path {
				t.Fatalf("\nwanted:\nGET %s\ngot:\n%s %s", test.path, got.Method, got.Path)
			}
			if stdout != body || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q\nstderr empty\ngot:\nstdout %q\nstderr %q", body, stdout, stderr)
			}
		})
	}

	t.Run("should name the missing instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "websocket", "get", connectionID)
		if err == nil || !strings.Contains(err.Error(), "instance work is not running") {
			t.Fatalf("\nwanted:\nerror naming instance work\ngot:\n%v", err)
		}
		if stdout != "" || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("\nwanted:\nno stdout and instance named on stderr\ngot:\nstdout %q\nstderr %q", stdout, stderr)
		}
	})

	t.Run("should reject a request-id flag and extra traffic arguments before dialing", func(t *testing.T) {
		for _, args := range [][]string{
			{"websocket", "get", connectionID, "--request-id", requestID},
			{"traffic", "websocket", requestID, "extra"},
		} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				configDir := serviceConfigDir(t)
				sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
				commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
				stdout, stderr, err := executeRoot(t, commandArgs...)
				if err == nil {
					t.Fatal("\nwanted:\nargument error\ngot:\nnil")
				}
				if len(sent.requests()) != 0 {
					t.Fatalf("\nwanted:\nno control API request\ngot:\n%+v", sent.requests())
				}
				if stdout != "" || !strings.Contains(stderr, err.Error()) {
					t.Fatalf("\nwanted:\nno stdout and %q on stderr\ngot:\nstdout %q\nstderr %q", err, stdout, stderr)
				}
			})
		}
	})

	t.Run("should use operation-specific control API errors", func(t *testing.T) {
		for _, test := range []struct {
			name string
			args []string
			want string
		}{
			{name: "connection get", args: []string{"websocket", "get", connectionID}, want: "getting websocket connection: not_found"},
			{name: "traffic lookup", args: []string{"traffic", "websocket", requestID}, want: "getting traffic websocket: not_found"},
		} {
			t.Run(test.name, func(t *testing.T) {
				configDir := serviceConfigDir(t)
				startCannedControlAPI(t, configDir, "work", http.StatusNotFound, `{"error":"not_found"}`)
				args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
				stdout, stderr, err := executeRoot(t, args...)
				if err == nil || err.Error() != test.want {
					t.Fatalf("\nwanted:\n%q\ngot:\n%v", test.want, err)
				}
				if stdout != "" || !strings.Contains(stderr, test.want) {
					t.Fatalf("\nwanted:\nno stdout and %q on stderr\ngot:\nstdout %q\nstderr %q", test.want, stdout, stderr)
				}
			})
		}
	})
}
