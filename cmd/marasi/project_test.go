package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestProjectCommand(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should open a project by path on the selected instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "explicit.marasi"))
		body := fmt.Sprintf(`{"project":%q}`+"\n", path)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		wantBody, err := json.Marshal(struct {
			Path string `json:"path"`
		}{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if sent.Method != http.MethodPost || sent.Path != "/project/open" || sent.Body != string(wantBody) {
			t.Fatalf("\nwanted:\nPOST /project/open body %s\ngot:\n%s %s body %q", wantBody, sent.Method, sent.Path, sent.Body)
		}
		if stdout != "project: "+path+"\n" || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", "project: "+path+"\n", stdout, stderr)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", path, "--json")
		if err != nil || stdout != body || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", body, stdout, stderr, err)
		}
	})

	t.Run("should treat switch as an alias for open", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "alias.marasi"))
		body := fmt.Sprintf(`{"project":%q}`+"\n", path)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "switch", "--path", path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		wantBody, err := json.Marshal(struct {
			Path string `json:"path"`
		}{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if sent.Method != http.MethodPost || sent.Path != "/project/open" || sent.Body != string(wantBody) {
			t.Fatalf("\nwanted:\nPOST /project/open body %s\ngot:\n%s %s body %q", wantBody, sent.Method, sent.Path, sent.Body)
		}
		if stdout != "project: "+path+"\n" || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", "project: "+path+"\n", stdout, stderr)
		}
	})

	t.Run("should resolve a name under the selected config directory", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path, err := resolveNamedProjectPath(configDir, "named")
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"project":%q}`+"\n", path)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		for _, command := range []string{"open", "switch"} {
			for _, name := range []string{"named", "named.marasi"} {
				sent.Method, sent.Path, sent.Body = "", "", ""
				stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", command, "--name", name)
				if err != nil {
					t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
				}
				wantBody, err := json.Marshal(struct {
					Path string `json:"path"`
				}{Path: path})
				if err != nil {
					t.Fatal(err)
				}
				if sent.Method != http.MethodPost || sent.Path != "/project/open" || sent.Body != string(wantBody) {
					t.Fatalf("\nwanted:\nPOST /project/open body %s\ngot:\n%s %s body %q", wantBody, sent.Method, sent.Path, sent.Body)
				}
				if stdout != "project: "+path+"\n" || stderr != "" {
					t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", "project: "+path+"\n", stdout, stderr)
				}
			}
		}
	})

	t.Run("should resolve a relative path in the CLI process", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		parent, err := os.MkdirTemp(".", "project-cli-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(parent) })
		relativePath := filepath.Join(parent, "relative.marasi")
		if filepath.IsAbs(relativePath) {
			t.Fatal("setup: path should be relative")
		}
		path := canonicalProjectPath(t, relativePath)
		body := fmt.Sprintf(`{"project":%q}`+"\n", path)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", relativePath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		wantBody, err := json.Marshal(struct {
			Path string `json:"path"`
		}{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if sent.Body != string(wantBody) {
			t.Fatalf("\nwanted:\nbody %s\ngot:\n%s", wantBody, sent.Body)
		}
		if stdout != "project: "+path+"\n" || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", "project: "+path+"\n", stdout, stderr)
		}
	})

	t.Run("should pass a successful response through byte for byte in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "json.marasi"))
		body := " {\n  \"project\": \"passed-through\"\n} "
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		for _, command := range []string{"open", "switch"} {
			stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", command, "--path", path, "--json")
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", body, stdout, stderr, err)
			}
		}
	})

	t.Run("should reject selectors and arguments before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "unused.marasi"))
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"project":"unused"}`)
		for _, args := range [][]string{
			{"project", "open"},
			{"project", "switch"},
			{"project", "open", "extra"},
			{"project", "switch", "extra"},
			{"project", "open", "--path", path, "--name", "named"},
			{"project", "open", "--path", "missing-extension"},
			{"project", "open", "--path", filepath.Join(t.TempDir(), "missing", "gone.marasi")},
			{"project", "open", "--name", "../escape"},
			{"project", "open", "--name", "nested/name"},
			{"project", "switch", "--name", "scratchpad.marasi.marasi"},
		} {
			sent.Method, sent.Path, sent.Body = "", "", ""
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
			if sent.Method != "" {
				t.Fatalf("\nwanted:\nno control request for %v\ngot:\n%s %s", args, sent.Method, sent.Path)
			}
		}

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", "missing-extension")
		if err == nil || stdout != "" || !strings.Contains(stderr, "missing-extension") {
			t.Fatalf("\nwanted:\npath named in the error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--name", "../escape", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "../escape")
	})

	t.Run("should normalize unavailable instances and control errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "errors.marasi"))
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", path)
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("\nwanted:\nunreachable instance error with empty stdout\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", path, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")

		startCannedControlAPI(t, configDir, "owned", http.StatusConflict, `{"error":"project_already_open"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "owned", "project", "open", "--path", path)
		if err == nil || stdout != "" || !strings.Contains(stderr, "opening project: 409 Conflict") {
			t.Fatalf("\nwanted:\nproject ownership conflict on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "owned", "project", "switch", "--path", path, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "opening project: project_already_open")

		startCannedControlAPI(t, configDir, "busy", http.StatusConflict, `{"error":"project_busy"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "busy", "project", "open", "--path", path)
		if err == nil || stdout != "" || !strings.Contains(stderr, "opening project: 409 Conflict") {
			t.Fatalf("\nwanted:\nbusy project on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "busy", "project", "open", "--path", path, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "opening project: project_busy")

		startCannedControlAPI(t, configDir, "malformed", http.StatusBadGateway, `{`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "malformed", "project", "open", "--path", path, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "opening project: 502 Bad Gateway")
	})

	t.Run("should normalize cancellation", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "cancel.marasi"))
		started, release := startBlockingControlAPI(t, configDir, "work")
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", path, "--json")
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatalf("starting project open command: %v", err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			command.Process.Kill()
			t.Fatal("project open request did not reach the control API")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			command.Process.Kill()
			t.Fatalf("interrupting project open command: %v", err)
		}
		err := command.Wait()
		close(release)
		assertJSONCommandError(t, stdout.String(), stderr.String(), err, "context canceled")
	})

	t.Run("should cross-compile for Windows", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "marasi.exe")
		command := exec.Command("go", "build", "-o", output, ".")
		command.Env = append(os.Environ(), "GOWORK=off", "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
		if combined, err := command.CombinedOutput(); err != nil {
			t.Fatalf("\nwanted:\nwindows cross-compile\ngot:\n%s\n%v", combined, err)
		}
	})
}

func TestProjectCommandLifecycle(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() {
		runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop")
		runMarasi(binary, "--config-dir", configDir, "--instance", "other", "service", "stop")
	})

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		fmt.Fprint(w, "proxied "+request.URL.Path)
	}))
	t.Cleanup(origin.Close)

	workListener := startNamedInstance(t, binary, configDir, "work", "--project-name", "one")
	otherListener := startNamedInstance(t, binary, configDir, "other", "--project-name", "two")
	onePath, err := resolveNamedProjectPath(configDir, "one")
	if err != nil {
		t.Fatal(err)
	}
	twoPath, err := resolveNamedProjectPath(configDir, "two")
	if err != nil {
		t.Fatal(err)
	}
	threePath := canonicalProjectPath(t, filepath.Join(t.TempDir(), "three.marasi"))

	if got := getViaProxy(t, workListener, origin.URL+"/before"); got != "proxied /before" {
		t.Fatalf("\nwanted:\nproxied /before\ngot:\n%s", got)
	}

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", threePath)
	if err != nil || stdout != "project: "+threePath+"\n" || stderr != "" {
		t.Fatalf("\nwanted:\nopened %s\ngot:\nstdout %q, stderr %q, error %v", threePath, stdout, stderr, err)
	}
	switchStdout, switchStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "switch", "--path", threePath)
	if err != nil || switchStdout != stdout || switchStderr != "" {
		t.Fatalf("\nwanted:\nswitch no-op matching open\ngot:\nstdout %q, stderr %q, error %v", switchStdout, switchStderr, err)
	}

	statusStdout, statusStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
	if err != nil || statusStderr != "" || !strings.Contains(statusStdout, "project: "+threePath+"\n") {
		t.Fatalf("\nwanted:\nstatus reporting %s\ngot:\nstdout %q, stderr %q, error %v", threePath, statusStdout, statusStderr, err)
	}

	if got := getViaProxy(t, workListener, origin.URL+"/after"); got != "proxied /after" {
		t.Fatalf("\nwanted:\nproxied /after\ngot:\n%s", got)
	}
	trafficStdout, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--json")
	if err != nil || !strings.Contains(trafficStdout, "/after") || strings.Contains(trafficStdout, "/before") {
		t.Fatalf("\nwanted:\nnew project traffic /after only\ngot:\n%s\n%v", trafficStdout, err)
	}

	summaries := projectTraffic(t, onePath)
	if len(summaries) != 1 || summaries[0].Path != "/before" {
		t.Fatalf("\nwanted:\nold project path /before\ngot:\n%v", summaries)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--path", twoPath)
	if err == nil || stdout != "" || !strings.Contains(stderr, "opening project: 409 Conflict") {
		t.Fatalf("\nwanted:\nproject ownership conflict opening owned project\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "other", "project", "switch", "--path", threePath, "--json")
	assertJSONCommandError(t, stdout, stderr, err, "opening project: project_already_open")
	if got := getViaProxy(t, otherListener, origin.URL+"/other"); got != "proxied /other" {
		t.Fatalf("\nwanted:\nproxied /other\ngot:\n%s", got)
	}

	fourPath, err := resolveNamedProjectPath(configDir, "four")
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "project", "open", "--name", "four")
	if err != nil || stdout != "project: "+fourPath+"\n" || stderr != "" {
		t.Fatalf("\nwanted:\nopened %s\ngot:\nstdout %q, stderr %q, error %v", fourPath, stdout, stderr, err)
	}

	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop"); err != nil {
		t.Fatalf("stopping work: %v", err)
	}
	after := projectTraffic(t, threePath)
	if len(after) != 1 || after[0].Path != "/after" {
		t.Fatalf("\nwanted:\nswitched project path /after\ngot:\n%v", after)
	}
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--port", "0"); err != nil {
		t.Fatalf("restarting work: %v", err)
	}
	scratchpad, err := resolveNamedProjectPath(configDir, "scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	statusStdout, _, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
	if err != nil || !strings.Contains(statusStdout, "project: "+scratchpad+"\n") || strings.Contains(statusStdout, threePath) {
		t.Fatalf("\nwanted:\nrestarted scratchpad, not %s\ngot:\n%s\n%v", threePath, statusStdout, err)
	}

	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop"); err != nil {
		t.Fatalf("stopping work: %v", err)
	}
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "other", "service", "stop"); err != nil {
		t.Fatalf("stopping other: %v", err)
	}
}

func canonicalProjectPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := resolveProjectPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func startNamedInstance(t *testing.T, binary, configDir, name, projectFlag, projectValue string) string {
	t.Helper()
	_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "start", projectFlag, projectValue, "--port", "0")
	if err != nil {
		t.Fatalf("starting %s: %v\n%s", name, err, stderr)
	}
	const prefix = "proxy listener started on "
	index := strings.Index(stderr, prefix)
	if index < 0 {
		t.Fatalf("\nwanted:\nproxy listener startup output\ngot:\n%s", stderr)
	}
	return strings.TrimSpace(stderr[index+len(prefix):])
}

func getViaProxy(t *testing.T, proxyAddress, target string) string {
	t.Helper()
	proxyURL, err := url.Parse("http://" + proxyAddress)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Get(target)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(body)
}

func projectTraffic(t *testing.T, path string) []*domain.RequestResponseSummary {
	t.Helper()
	dbConn, err := db.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	repository := db.NewProxyRepo(dbConn)
	t.Cleanup(func() { repository.Close() })
	summaries, err := repository.GetRequestResponseSummary()
	if err != nil {
		t.Fatal(err)
	}
	return summaries
}
