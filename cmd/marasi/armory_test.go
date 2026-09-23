package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestArmoryCommands(t *testing.T) {
	binary := buildMarasi(t)
	templateID := "0193802f-f0e7-73d9-a764-06d21e367809"
	runID := "01938032-1b17-7243-b035-e6a9f4645904"
	rawFile := filepath.Join(t.TempDir(), "request.raw")
	if err := os.WriteFile(rawFile, []byte("GET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	template := `{"id":"` + templateID + `","name":"Login","description":"Try variants","raw_template":"GET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n"}` + "\n"
	run := `{"id":"` + runID + `","template_id":"` + templateID + `","template_snapshot":"GET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n","use_https":false,"wordlists":["users","passwords"],"status":"draft","attack_type":"tandem","max_concurrent":4,"created_at":"2026-01-02T03:04:05Z","started_at":null,"finished_at":null}` + "\n"

	for _, test := range []struct {
		name       string
		args       []string
		response   string
		method     string
		path       string
		query      string
		body       string
		wantStdout string
		wantStderr string
	}{
		{name: "template list", args: []string{"armory", "template", "list"}, response: `{"items":[{"id":"` + templateID + `","name":"Login","description":"Try variants"}]}` + "\n", method: http.MethodGet, path: "/armory/template", wantStdout: "id: " + templateID + "\nname: Login\n"},
		{name: "template get", args: []string{"armory", "template", "get", templateID}, response: template, method: http.MethodGet, path: "/armory/template/" + templateID, wantStdout: "id: " + templateID + "\nname: Login\ndescription: Try variants\nGET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n"},
		{name: "template create", args: []string{"armory", "template", "create", "--name", "Login", "--description", "Try variants", "--raw-file", rawFile}, response: template, method: http.MethodPost, path: "/armory/template", body: `{"name":"Login","description":"Try variants","raw_template":"GET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n"}`, wantStderr: "armory template " + templateID + " created successfully\n"},
		{name: "template update", args: []string{"armory", "template", "update", templateID, "--description", ""}, response: template, method: http.MethodPost, path: "/armory/template/" + templateID, body: `{"description":""}`, wantStderr: "armory template " + templateID + " updated successfully\n"},
		{name: "template delete", args: []string{"armory", "template", "delete", templateID}, response: `{"id":"` + templateID + `"}` + "\n", method: http.MethodDelete, path: "/armory/template/" + templateID, wantStderr: "armory template " + templateID + " deleted successfully\n"},
		{name: "run list", args: []string{"armory", "run", "list", "--template", templateID}, response: `{"items":[` + strings.TrimSpace(run) + `]}` + "\n", method: http.MethodGet, path: "/armory/template/" + templateID + "/run", wantStdout: armoryRunHuman(runID, templateID)},
		{name: "run get", args: []string{"armory", "run", "get", runID}, response: run, method: http.MethodGet, path: "/armory/run/" + runID, wantStdout: armoryRunHuman(runID, templateID)},
		{name: "run create", args: []string{"armory", "run", "create", "--template", templateID, "--attack-type", "tandem", "--wordlist", "users", "--wordlist", "passwords", "--http", "--max-concurrent", "4"}, response: run, method: http.MethodPost, path: "/armory/run", body: `{"template_id":"` + templateID + `","attack_type":"tandem","wordlists":["users","passwords"],"use_https":false,"max_concurrent":4}`, wantStderr: "armory run " + runID + " created successfully\n"},
		{name: "run validate", args: []string{"armory", "run", "validate", "--raw-file", rawFile, "--attack-type", "harpoon"}, response: "{\"status\":\"ok\"}\n", method: http.MethodPost, path: "/armory/run/validate", body: `{"raw_template":"GET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n","attack_type":"harpoon","wordlists":[]}`, wantStdout: "status: ok\n"},
		{name: "run start", args: []string{"armory", "run", "start", runID}, response: run, method: http.MethodPost, path: "/armory/run/" + runID + "/start", wantStderr: "armory run " + runID + " started successfully\n"},
		{name: "run cancel", args: []string{"armory", "run", "cancel", runID}, response: run, method: http.MethodPost, path: "/armory/run/" + runID + "/cancel", wantStderr: "armory run " + runID + " cancelled successfully\n"},
		{name: "run delete", args: []string{"armory", "run", "delete", runID}, response: `{"id":"` + runID + `"}` + "\n", method: http.MethodDelete, path: "/armory/run/" + runID, wantStderr: "armory run " + runID + " deleted successfully\n"},
		{name: "run traffic", args: []string{"armory", "run", "traffic", runID, "--limit", "10", "--cursor", templateID}, response: `{"items":[{"id":"` + templateID + `","method":"GET","host":"example.com","path":"/login","status_code":200,"length":"12"}],"next_cursor":"` + runID + `"}` + "\n", method: http.MethodGet, path: "/armory/run/" + runID + "/traffic", query: "cursor=" + templateID + "&limit=10", wantStdout: templateID + "  GET  example.com  /login  200  12\n"},
	} {
		t.Run("should encode "+test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			if got.Method != test.method || got.Path != test.path || got.RawQuery != test.query || got.Body != test.body {
				t.Fatalf("\nwanted:\n%s %s?%s body %q\ngot:\n%s %s?%s body %q", test.method, test.path, test.query, test.body, got.Method, got.Path, got.RawQuery, got.Body)
			}
			if test.body != "" && got.ContentType != "application/json" {
				t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got.ContentType)
			}
			if stdout != test.wantStdout {
				t.Fatalf("\nwanted stdout:\n%q\ngot:\n%q", test.wantStdout, stdout)
			}
			if test.name == "run traffic" {
				test.wantStderr = "next_cursor=" + runID + "\n"
			}
			if stderr != test.wantStderr {
				t.Fatalf("\nwanted:\nnext cursor on stderr\ngot:\n%s", stderr)
			}
		})
	}
}

func TestArmoryCommandOptions(t *testing.T) {
	binary := buildMarasi(t)
	templateID := "0193802f-f0e7-73d9-a764-06d21e367809"

	t.Run("should accept attack type regardless of case", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"id":"x"}`)
		if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "armory", "run", "create", "--template", templateID, "--attack-type", "Harpoon"); err != nil {
			t.Fatal(err)
		}
		if got := sent.snapshot().Body; got != `{"template_id":"`+templateID+`","attack_type":"harpoon","wordlists":[]}` {
			t.Fatalf("\nwanted:\ncanonical attack type\ngot:\n%s", got)
		}
	})

	t.Run("should omit server-defaulted run fields", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"id":"x"}`)
		_, _, _ = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "armory", "run", "create", "--template", templateID, "--attack-type", "harpoon")
		if got := sent.snapshot().Body; got != `{"template_id":"`+templateID+`","attack_type":"harpoon","wordlists":[]}` {
			t.Fatalf("\nwanted:\nomitted use_https and max_concurrent\ngot:\n%s", got)
		}
	})

	t.Run("should validate piped stdin", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, "{\"status\":\"ok\"}\n")
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "armory", "run", "validate", "--attack-type", "harpoon", "--json")
		command.Stdin = strings.NewReader("GET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n")
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		if err != nil || stdout.String() != "{\"status\":\"ok\"}\n" || stderr.String() != "" {
			t.Fatalf("\nwanted:\nJSON response on stdout\ngot:\nstdout %q, stderr %q, error %v", stdout.String(), stderr.String(), err)
		}
		want := `{"raw_template":"GET /@@ HTTP/1.1\r\nHost: example.com\r\n\r\n","attack_type":"harpoon","wordlists":[]}`
		if got := sent.snapshot().Body; got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should reject missing or invalid options", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"armory", "template", "create"},
			{"armory", "template", "update", templateID},
			{"armory", "run", "list"},
			{"armory", "run", "create", "--template", templateID},
			{"armory", "run", "create", "--template", templateID, "--attack-type", "invalid"},
			{"armory", "run", "validate", "--attack-type", "harpoon"},
			{"armory", "run", "create", "--template", templateID, "--attack-type", "harpoon", "--start"},
			{"armory", "template", "list", "--project", "scratchpad"},
			{"wordlist"},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err == nil || stdout != "" || stderr == "" {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should pass control bodies through and normalize errors in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := " {\n  \"items\": []\n} "
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "armory", "template", "list", "--json")
		if err != nil || stdout != body || stderr != "" {
			t.Fatalf("\nwanted:\nbyte-for-byte JSON body\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		missingDir := serviceConfigDir(t)
		stdout, stderr, err = runMarasi(binary, "--config-dir", missingDir, "--instance", "work", "armory", "template", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")

		errorDir := serviceConfigDir(t)
		startCannedControlAPI(t, errorDir, "work", http.StatusBadRequest, `{"error":"invalid_armory_request"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", errorDir, "--instance", "work", "armory", "template", "create", "--name", "Login", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "creating Armory template: invalid_armory_request")
	})
}

func armoryRunHuman(runID, templateID string) string {
	return "id: " + runID + "\ntemplate_id: " + templateID + "\nstatus: draft\nattack_type: tandem\nuse_https: false\nmax_concurrent: 4\nwordlists: users, passwords\ncreated_at: 2026-01-02T03:04:05Z\nstarted_at: \nfinished_at: \n"
}

func TestArmoryCommandLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("service process fixture uses Unix control sockets")
	}
	requestStarted := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		select {
		case <-requestStarted:
		default:
			close(requestStarted)
		}
		select {
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		case <-request.Context().Done():
		}
	}))
	defer target.Close()

	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	if err := os.MkdirAll(filepath.Join(configDir, "wordlists"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "wordlists", "users.txt"), []byte(strings.Repeat("alice\n", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	rawFile := filepath.Join(t.TempDir(), "request.raw")
	raw := "GET /@@fallback@@ HTTP/1.1\r\nHost: " + strings.TrimPrefix(target.URL, "http://") + "\r\n\r\n"
	if err := os.WriteFile(rawFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--project-name", "armory-cli", "--port", "0"); err != nil {
		t.Fatalf("starting service: %v", err)
	}

	templateBody, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "--json", "armory", "template", "create", "--name", "Lifecycle", "--raw-file", rawFile)
	if err != nil {
		t.Fatalf("creating template: %v", err)
	}
	var template struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(templateBody), &template); err != nil || template.ID == "" {
		t.Fatalf("decoding template response %q: %v", templateBody, err)
	}
	runBody, runStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "--json", "armory", "run", "create", "--template", template.ID, "--attack-type", "harpoon", "--wordlist", "users.txt", "--http")
	if err != nil {
		t.Fatalf("creating run: stdout %q, stderr %q, error %v", runBody, runStderr, err)
	}
	var run struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(runBody), &run); err != nil || run.ID == "" || run.Status != "draft" {
		t.Fatalf("decoding run response %q: %v", runBody, err)
	}
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "armory", "run", "start", run.ID); err != nil {
		t.Fatalf("starting run: %v", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Armory request")
	}
	var traffic, cursor string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		traffic, cursor, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "armory", "run", "traffic", run.ID)
		if err == nil && strings.Contains(traffic, "GET") {
			break
		}
	}
	if err != nil || !strings.Contains(traffic, "GET") || cursor != "" {
		t.Fatalf("listing run traffic: stdout %q, stderr %q, error %v", traffic, cursor, err)
	}
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "armory", "run", "cancel", run.ID); err != nil {
		t.Fatalf("cancelling run: %v", err)
	}
}
