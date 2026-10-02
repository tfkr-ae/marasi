package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckpointCommands(t *testing.T) {
	binary := buildMarasi(t)
	httpID := "0193802f-f0e7-73d9-a764-06d21e367809"
	wsID := "01938032-1b17-7243-b035-e6a9f4645904"
	httpDump := []byte("GET /a HTTP/1.1\r\nHost: example.com\r\n\r\n")
	wsPayload := []byte("hello")
	httpItem := `{"id":"` + httpID + `","type":"request","raw":"` + base64.StdEncoding.EncodeToString(httpDump) + `"}` + "\n"
	wsItem := `{"id":"` + wsID + `","type":"websocket","payload":"` + base64.StdEncoding.EncodeToString(wsPayload) + `","opcode":1,"direction":"client","connection_id":"0193802f-f0e7-73d9-a764-06d21e36780a","request_id":"0193802f-f0e7-73d9-a764-06d21e36780b"}` + "\n"
	listBody := `{"items":[{"id":"` + httpID + `","type":"request","raw":"` + base64.StdEncoding.EncodeToString(httpDump) + `"},{"id":"` + wsID + `","type":"websocket","payload":"` + base64.StdEncoding.EncodeToString(wsPayload) + `","opcode":1,"direction":"client","connection_id":"0193802f-f0e7-73d9-a764-06d21e36780a","request_id":"0193802f-f0e7-73d9-a764-06d21e36780b"}],"intercept":true,"websocket_intercept":false}` + "\n"

	t.Run("should list checkpoint items as id type lines", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "list", http.StatusOK, listBody)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "list", "checkpoint", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/checkpoint" || got.RawQuery != "" || got.Body != "" {
			t.Fatalf("\nwanted:\nGET /checkpoint empty body\ngot:\n%s %s?%s body %q", got.Method, got.Path, got.RawQuery, got.Body)
		}
		wantStdout := httpID + " request\n" + wsID + " websocket\n"
		if stdout != wantStdout || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", wantStdout, stdout, stderr)
		}
	})

	t.Run("should leave an empty list silent", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "empty", http.StatusOK, `{"items":[],"intercept":false,"websocket_intercept":false}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "empty", "checkpoint", "list")
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty streams, nil error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should send --kind as a query and reject other values before the request", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "kind-http", http.StatusOK, listBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "kind-http", "checkpoint", "list", "--kind", "http")
		got := sent.snapshot()
		if err != nil || stdout != httpID+" request\n"+wsID+" websocket\n" || stderr != "" || got.Method != http.MethodGet || got.Path != "/checkpoint" || got.RawQuery != "kind=http" {
			t.Fatalf("\nwanted:\nGET /checkpoint?kind=http\ngot:\n%s %s?%s, stdout %q, stderr %q, error %v", got.Method, got.Path, got.RawQuery, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "kind-ws", http.StatusOK, listBody)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "kind-ws", "checkpoint", "list", "--kind", "websocket")
		got = sent.snapshot()
		if err != nil || stderr != "" || got.RawQuery != "kind=websocket" {
			t.Fatalf("\nwanted:\nGET kind=websocket\ngot:\n%s %s?%s, stdout %q, stderr %q, error %v", got.Method, got.Path, got.RawQuery, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "kind-bad", http.StatusOK, listBody)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "kind-bad", "checkpoint", "list", "--kind", "other")
		got = sent.snapshot()
		if err == nil || stdout != "" || stderr == "" || got.Method != "" || got.Path != "" {
			t.Fatalf("\nwanted:\nCLI error before request\ngot:\n%s %s, stdout %q, stderr %q, error %v", got.Method, got.Path, stdout, stderr, err)
		}
	})

	t.Run("should write decoded bytes only for human get", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "get", http.StatusOK, httpItem)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "get", "checkpoint", "get", httpID)
		got := sent.snapshot()
		if err != nil || stdout != string(httpDump) || stderr != "" || got.Method != http.MethodGet || got.Path != "/checkpoint/"+httpID || got.Body != "" {
			t.Fatalf("\nwanted:\nGET item dump on stdout\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "get-ws", http.StatusOK, wsItem)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "get-ws", "checkpoint", "get", wsID)
		got = sent.snapshot()
		if err != nil || stdout != string(wsPayload) || stderr != "" || got.Path != "/checkpoint/"+wsID {
			t.Fatalf("\nwanted:\nwebsocket payload on stdout\ngot:\n%s %s, stdout %q, stderr %q, error %v", got.Method, got.Path, stdout, stderr, err)
		}
	})

	t.Run("should forward tty with no --file as {}", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "forward", http.StatusOK, listBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "forward", "checkpoint", "forward", httpID)
		got := sent.snapshot()
		if err != nil || stdout != "" || stderr != "checkpoint "+httpID+" forwarded\n" || got.Method != http.MethodPost || got.Path != "/checkpoint/"+httpID+"/forward" || got.Body != "{}" || got.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPOST {} and success on stderr\ngot:\n%s %s body %q type %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, got.ContentType, stdout, stderr, err)
		}
	})

	t.Run("should send intercept-response on tty forward", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "forward-ir", http.StatusOK, listBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "forward-ir", "checkpoint", "forward", httpID, "--intercept-response")
		got := sent.snapshot()
		wantBody := `{"intercept_response":true}`
		if err != nil || stdout != "" || stderr != "checkpoint "+httpID+" forwarded\n" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nbody %q\ngot:\nbody %q, stdout %q, stderr %q, error %v", wantBody, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should GET then POST file bytes as raw or payload", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		edited := []byte("GET /edited HTTP/1.1\r\nHost: example.com\r\n\r\n")
		file := filepath.Join(t.TempDir(), "raw.txt")
		if err := os.WriteFile(file, edited, 0o600); err != nil {
			t.Fatalf("writing file: %v", err)
		}
		sent := startCannedControlAPI(t, configDir, "file", http.StatusOK, httpItem)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "file", "checkpoint", "forward", httpID, "--file", file)
		history := sent.requests()
		wantBody := `{"raw":"` + base64.StdEncoding.EncodeToString(edited) + `"}`
		if err != nil || stdout != "" || stderr != "checkpoint "+httpID+" forwarded\n" || len(history) != 2 {
			t.Fatalf("\nwanted:\nGET then POST, success on stderr\ngot:\n%#v, stdout %q, stderr %q, error %v", history, stdout, stderr, err)
		}
		if history[0].Method != http.MethodGet || history[0].Path != "/checkpoint/"+httpID || history[0].Body != "" {
			t.Fatalf("\nwanted:\nGET /checkpoint/%s\ngot:\n%s %s body %q", httpID, history[0].Method, history[0].Path, history[0].Body)
		}
		if history[1].Method != http.MethodPost || history[1].Path != "/checkpoint/"+httpID+"/forward" || history[1].Body != wantBody {
			t.Fatalf("\nwanted:\nPOST body %q\ngot:\n%s %s body %q", wantBody, history[1].Method, history[1].Path, history[1].Body)
		}

		wsFile := filepath.Join(t.TempDir(), "frame.bin")
		if err := os.WriteFile(wsFile, []byte("edited"), 0o600); err != nil {
			t.Fatalf("writing websocket file: %v", err)
		}
		sent = startCannedControlAPI(t, configDir, "file-ws", http.StatusOK, wsItem)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "file-ws", "checkpoint", "forward", wsID, "--file", wsFile)
		history = sent.requests()
		wantBody = `{"payload":"` + base64.StdEncoding.EncodeToString([]byte("edited")) + `"}`
		if err != nil || stdout != "" || stderr != "checkpoint "+wsID+" forwarded\n" || len(history) != 2 || history[1].Body != wantBody {
			t.Fatalf("\nwanted:\nwebsocket payload body %q\ngot:\n%#v, stdout %q, stderr %q, error %v", wantBody, history, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "file-ir", http.StatusOK, httpItem)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "file-ir", "checkpoint", "forward", httpID, "--file", file, "--intercept-response")
		history = sent.requests()
		wantBody = `{"raw":"` + base64.StdEncoding.EncodeToString(edited) + `","intercept_response":true}`
		if err != nil || stdout != "" || stderr != "checkpoint "+httpID+" forwarded\n" || len(history) != 2 || history[1].Body != wantBody {
			t.Fatalf("\nwanted:\nbody %q\ngot:\n%#v, stdout %q, stderr %q, error %v", wantBody, history, stdout, stderr, err)
		}
	})

	t.Run("should forward piped stdin as edited bytes", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		edited := []byte("GET /stdin HTTP/1.1\r\nHost: example.com\r\n\r\n")
		sent := startCannedControlAPI(t, configDir, "stdin", http.StatusOK, httpItem)
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "stdin", "checkpoint", "forward", httpID)
		command.Stdin = bytes.NewReader(edited)
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		history := sent.requests()
		wantBody := `{"raw":"` + base64.StdEncoding.EncodeToString(edited) + `"}`
		if err != nil || stdout.String() != "" || stderr.String() != "checkpoint "+httpID+" forwarded\n" || len(history) != 2 || history[1].Body != wantBody {
			t.Fatalf("\nwanted:\nstdin body %q\ngot:\n%#v, stdout %q, stderr %q, error %v", wantBody, history, stdout.String(), stderr.String(), err)
		}
	})

	t.Run("should drop with no body", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "drop", http.StatusOK, listBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "drop", "checkpoint", "drop", httpID)
		got := sent.snapshot()
		if err != nil || stdout != "" || stderr != "checkpoint "+httpID+" dropped\n" || got.Method != http.MethodPost || got.Path != "/checkpoint/"+httpID+"/drop" || got.Body != "" || got.ContentType != "" {
			t.Fatalf("\nwanted:\nPOST drop with no body\ngot:\n%s %s body %q type %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, got.ContentType, stdout, stderr, err)
		}
	})

	t.Run("should set intercept flags from on and off", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "intercept-on", http.StatusOK, listBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "intercept-on", "checkpoint", "intercept", "on")
		got := sent.snapshot()
		if err != nil || stdout != "" || stderr != "checkpoint intercept on\n" || got.Method != http.MethodPost || got.Path != "/checkpoint/intercept" || got.Body != `{"intercept":true}` {
			t.Fatalf("\nwanted:\nPOST intercept true\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "intercept-off", http.StatusOK, listBody)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "intercept-off", "checkpoint", "intercept", "off")
		got = sent.snapshot()
		if err != nil || stdout != "" || stderr != "checkpoint intercept off\n" || got.Body != `{"intercept":false}` {
			t.Fatalf("\nwanted:\nPOST intercept false\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "ws-on", http.StatusOK, listBody)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "ws-on", "checkpoint", "websocket-intercept", "on")
		got = sent.snapshot()
		if err != nil || stdout != "" || stderr != "checkpoint websocket-intercept on\n" || got.Path != "/checkpoint/websocket-intercept" || got.Body != `{"websocket_intercept":true}` {
			t.Fatalf("\nwanted:\nPOST websocket intercept true\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "ws-off", http.StatusOK, listBody)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "ws-off", "checkpoint", "websocket-intercept", "off")
		got = sent.snapshot()
		if err != nil || stdout != "" || stderr != "checkpoint websocket-intercept off\n" || got.Body != `{"websocket_intercept":false}` {
			t.Fatalf("\nwanted:\nPOST websocket intercept false\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "intercept-bad", http.StatusOK, listBody)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "intercept-bad", "checkpoint", "intercept", "true")
		got = sent.snapshot()
		if err == nil || stdout != "" || stderr == "" || got.Method != "" {
			t.Fatalf("\nwanted:\nCLI error before request for intercept true\ngot:\n%s %s, stdout %q, stderr %q, error %v", got.Method, got.Path, stdout, stderr, err)
		}

		sent = startCannedControlAPI(t, configDir, "ws-bad", http.StatusOK, listBody)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "ws-bad", "checkpoint", "websocket-intercept", "enable")
		got = sent.snapshot()
		if err == nil || stdout != "" || stderr == "" || got.Method != "" {
			t.Fatalf("\nwanted:\nCLI error before request for websocket-intercept enable\ngot:\n%s %s, stdout %q, stderr %q, error %v", got.Method, got.Path, stdout, stderr, err)
		}
	})

	t.Run("should pass successful responses through byte for byte in JSON mode", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "raw.txt")
		if err := os.WriteFile(file, httpDump, 0o600); err != nil {
			t.Fatalf("writing file: %v", err)
		}
		for _, args := range [][]string{
			{"--json", "checkpoint", "list"},
			{"checkpoint", "list", "--json"},
			{"--json", "checkpoint", "list", "--kind", "http"},
			{"checkpoint", "list", "--kind", "http", "--json"},
			{"--json", "checkpoint", "get", httpID},
			{"checkpoint", "get", httpID, "--json"},
			{"--json", "checkpoint", "forward", httpID},
			{"checkpoint", "forward", httpID, "--json"},
			{"--json", "checkpoint", "forward", httpID, "--intercept-response"},
			{"checkpoint", "forward", httpID, "--intercept-response", "--json"},
			{"--json", "checkpoint", "drop", httpID},
			{"checkpoint", "drop", httpID, "--json"},
			{"--json", "checkpoint", "intercept", "on"},
			{"checkpoint", "intercept", "on", "--json"},
			{"--json", "checkpoint", "intercept", "off"},
			{"checkpoint", "intercept", "off", "--json"},
			{"--json", "checkpoint", "websocket-intercept", "on"},
			{"checkpoint", "websocket-intercept", "on", "--json"},
			{"--json", "checkpoint", "websocket-intercept", "off"},
			{"checkpoint", "websocket-intercept", "off", "--json"},
		} {
			configDir := serviceConfigDir(t)
			body := " {\n  \"unexpected\": true\n} "
			startCannedControlAPI(t, configDir, "json", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "json"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\n%v wanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", args, body, stdout, stderr, err)
			}
		}

		configDir := serviceConfigDir(t)
		passthrough := " {\n  \"unexpected\": true\n} "
		startCannedControlAPIHandler(t, configDir, "json-file", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				io.WriteString(w, httpItem)
				return
			}
			io.WriteString(w, passthrough)
		})
		for _, args := range [][]string{
			{"--json", "checkpoint", "forward", httpID, "--file", file},
			{"checkpoint", "forward", httpID, "--file", file, "--json"},
		} {
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "json-file"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != passthrough || stderr != "" {
				t.Fatalf("\n%v wanted:\nPOST body passthrough, empty stderr\ngot:\nstdout %q, stderr %q, error %v", args, stdout, stderr, err)
			}
		}
	})

	t.Run("should enforce arguments", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"checkpoint", "list", "extra"},
			{"checkpoint", "get"},
			{"checkpoint", "get", httpID, "extra"},
			{"checkpoint", "forward"},
			{"checkpoint", "forward", httpID, "extra"},
			{"checkpoint", "drop"},
			{"checkpoint", "drop", httpID, "extra"},
			{"checkpoint", "intercept"},
			{"checkpoint", "intercept", "on", "extra"},
			{"checkpoint", "websocket-intercept"},
			{"checkpoint", "websocket-intercept", "off", "extra"},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should report missing instances and json API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "checkpoint", "list")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance missing is not running") {
			t.Fatalf("\nwanted:\nmissing instance on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "checkpoint", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		startCannedControlAPI(t, configDir, "list-api", http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "list-api", "checkpoint", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "listing checkpoint items: invalid_checkpoint_request")

		startCannedControlAPI(t, configDir, "get-api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "get-api", "checkpoint", "get", httpID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting checkpoint item: not_found")

		startCannedControlAPI(t, configDir, "forward-api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "forward-api", "checkpoint", "forward", httpID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "forwarding checkpoint item: not_found")

		startCannedControlAPI(t, configDir, "drop-api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "drop-api", "checkpoint", "drop", httpID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "dropping checkpoint item: not_found")

		startCannedControlAPI(t, configDir, "intercept-api", http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "intercept-api", "checkpoint", "intercept", "on", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "setting checkpoint intercept: invalid_checkpoint_request")

		startCannedControlAPI(t, configDir, "ws-api", http.StatusBadRequest, `{"error":"invalid_checkpoint_request"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "ws-api", "checkpoint", "websocket-intercept", "off", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "setting checkpoint websocket intercept: invalid_checkpoint_request")
	})
}

func TestCheckpointCommandLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("service process fixture uses Unix control sockets")
	}
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

	proxyAddress := startNamedInstance(t, binary, configDir, "work", "--project-name", "checkpoint-cli")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Edit") == "yes" {
			fmt.Fprint(w, "edited")
			return
		}
		fmt.Fprint(w, "origin:"+r.URL.Path)
	}))
	t.Cleanup(origin.Close)

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating stderr pipe: %v", err)
	}
	t.Cleanup(func() {
		stderrR.Close()
		stderrW.Close()
	})
	var eventsOut bytes.Buffer
	eventsCmd := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "events")
	eventsCmd.Stdout = &eventsOut
	eventsCmd.Stderr = stderrW
	if err := eventsCmd.Start(); err != nil {
		t.Fatalf("starting events: %v", err)
	}
	t.Cleanup(func() {
		if eventsCmd.ProcessState == nil {
			eventsCmd.Process.Kill()
			eventsCmd.Wait()
		}
	})
	connected := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stderrR).ReadString('\n')
		connected <- line
	}()
	select {
	case line := <-connected:
		if line != ": connected\n" {
			t.Fatalf("wanted connected comment, got %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("events command did not connect")
	}

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "list")
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("empty list: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "intercept", "on")
	if err != nil || stdout != "" || stderr != "checkpoint intercept on\n" {
		t.Fatalf("enabling intercept: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	first := startProxyGet(t, proxyAddress, origin.URL+"/held")
	id := waitForCheckpointID(t, binary, configDir, "request")

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "list")
	if err != nil || stdout != id+" request\n" || stderr != "" {
		t.Fatalf("list held request: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "get", id)
	if err != nil || stderr != "" || !strings.Contains(stdout, "/held") {
		t.Fatalf("get held request: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "list", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("list json: stderr %q, error %v", stderr, err)
	}
	var listed struct {
		Items []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
		Intercept          bool `json:"intercept"`
		WebsocketIntercept bool `json:"websocket_intercept"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("decoding list: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != id || listed.Items[0].Type != "request" || !listed.Intercept || listed.WebsocketIntercept {
		t.Fatalf("wanted one held request with intercept on, got %#v", listed)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "forward", id)
	if err != nil || stdout != "" || stderr != "checkpoint "+id+" forwarded\n" {
		t.Fatalf("forward original: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	responseID := waitForCheckpointID(t, binary, configDir, "response")
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "forward", responseID)
	if err != nil || stdout != "" || stderr != "checkpoint "+responseID+" forwarded\n" {
		t.Fatalf("forward response: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	if body := waitProxyGet(t, first); body != "origin:/held" {
		t.Fatalf("wanted origin:/held, got %q", body)
	}

	second := startProxyGet(t, proxyAddress, origin.URL+"/edit")
	editID := waitForCheckpointID(t, binary, configDir, "request")
	dump, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "get", editID)
	if err != nil || stderr != "" {
		t.Fatalf("get dump for edit: stderr %q, error %v", stderr, err)
	}
	edited := strings.Replace(dump, "\r\n", "\r\nX-Edit: yes\r\n", 1)
	file := filepath.Join(t.TempDir(), "edit.txt")
	if err := os.WriteFile(file, []byte(edited), 0o600); err != nil {
		t.Fatalf("writing edited dump: %v", err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "forward", editID, "--file", file)
	if err != nil || stdout != "" || stderr != "checkpoint "+editID+" forwarded\n" {
		t.Fatalf("forward edited request: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	editResponseID := waitForCheckpointID(t, binary, configDir, "response")
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "forward", editResponseID)
	if err != nil || stdout != "" || stderr != "checkpoint "+editResponseID+" forwarded\n" {
		t.Fatalf("forward edited response: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	if body := waitProxyGet(t, second); body != "edited" {
		t.Fatalf("wanted edited origin body, got %q", body)
	}

	dropped := startProxyGet(t, proxyAddress, origin.URL+"/drop")
	dropID := waitForCheckpointID(t, binary, configDir, "request")
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "drop", dropID)
	if err != nil || stdout != "" || stderr != "checkpoint "+dropID+" dropped\n" {
		t.Fatalf("drop: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "list")
	if err != nil || strings.Contains(stdout, dropID) || stderr != "" {
		t.Fatalf("list after drop: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dropped proxy request")
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "intercept", "off")
	if err != nil || stdout != "" || stderr != "checkpoint intercept off\n" {
		t.Fatalf("disabling intercept: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "websocket-intercept", "on")
	if err != nil || stdout != "" || stderr != "checkpoint websocket-intercept on\n" {
		t.Fatalf("enabling websocket intercept: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "websocket-intercept", "off", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("disabling websocket intercept as JSON: stderr %q, error %v", stderr, err)
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("decoding flag JSON: %v", err)
	}
	if listed.Intercept || listed.WebsocketIntercept || len(listed.Items) != 0 {
		t.Fatalf("wanted flags off and empty list, got %#v", listed)
	}

	if err := eventsCmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting events: %v", err)
	}
	if err := eventsCmd.Wait(); err != nil {
		t.Fatalf("events exit: %v", err)
	}
	events := eventsOut.String()
	for _, name := range []string{"checkpoint.held", "checkpoint.forwarded", "checkpoint.dropped", "checkpoint.updated"} {
		if !strings.Contains(events, name+" ") {
			t.Fatalf("wanted event %s in %q", name, events)
		}
	}
}

type proxyGetResult struct {
	body string
	err  error
}

func startProxyGet(t *testing.T, proxyAddress, target string) <-chan proxyGetResult {
	t.Helper()
	done := make(chan proxyGetResult, 1)
	go func() {
		body, err := proxyGet(t, proxyAddress, target)
		done <- proxyGetResult{body: body, err: err}
	}()
	return done
}

func waitProxyGet(t *testing.T, done <-chan proxyGetResult) string {
	t.Helper()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("proxy request: %v", result.err)
		}
		return result.body
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for proxy request")
		return ""
	}
}

func proxyGet(t *testing.T, proxyAddress, target string) (string, error) {
	t.Helper()
	proxyURL, err := url.Parse("http://" + proxyAddress)
	if err != nil {
		return "", err
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Get(target)
	if err != nil {
		return "", err
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	return string(body), nil
}

func waitForCheckpointID(t *testing.T, binary, configDir, wantType string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "list")
		if err == nil && stderr == "" {
			for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
				id, kind, ok := strings.Cut(line, " ")
				if ok && kind == wantType && id != "" {
					return id
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for checkpoint %s", wantType)
	return ""
}
