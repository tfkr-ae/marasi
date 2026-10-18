package main

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// installEvent formats one SSE frame.
func installEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

// startCannedInstallAPI serves GET /events and answers any other request with
// status and response. The event stream connects at once, writes events after
// the next non-events request, then closes, or with hold stays open until the
// client leaves.
func startCannedInstallAPI(t *testing.T, configDir string, status int, response string, events []string, hold bool) *cannedControlRequest {
	t.Helper()
	posted := make(chan struct{}, 1)
	return startCannedControlAPIHandler(t, configDir, "work", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, response)
			select {
			case posted <- struct{}{}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-posted:
		case <-r.Context().Done():
			return
		}
		for _, event := range events {
			io.WriteString(w, event)
			w.(http.Flusher).Flush()
		}
		if hold {
			<-r.Context().Done()
		}
	})
}

const installStarted = `{"joined":false,"install":{"state":"running","version":"18.10.2026","step":"download","received":0,"total":0},"install_path":"/usr/local/bin/marasi"}` + "\n"

var installProgress = []string{
	`{"instance":"work","version":"18.10.2026","step":"download","received":0,"total":20971520}`,
	`{"instance":"work","version":"18.10.2026","step":"download","received":1048576,"total":20971520}`,
	`{"instance":"work","version":"18.10.2026","step":"download","received":10485760,"total":20971520}`,
	`{"instance":"work","version":"18.10.2026","step":"verify","received":20971520,"total":20971520}`,
	`{"instance":"work","version":"18.10.2026","step":"install","received":20971520,"total":20971520}`,
}

const installDone = `{"instance":"work","version":"18.10.2026","install_path":"/usr/local/bin/marasi"}`

// installEvents is a whole install, after an unrelated update.available and
// progress for another version, which install must skip.
func installEvents() []string {
	events := []string{
		installEvent("update.available", `{"instance":"work","version":"18.10.2026","channel":"release","published":null,"notes":"","page":"https://example.test/18"}`),
		installEvent("update.progress", `{"instance":"work","version":"17.10.2026","step":"download","received":0,"total":10}`),
	}
	for _, progress := range installProgress {
		events = append(events, installEvent("update.progress", progress))
	}
	return append(events, installEvent("update.installed", installDone))
}

func installNDJSON() string {
	var want strings.Builder
	for _, progress := range installProgress {
		want.WriteString(`{"event":"update.progress","data":` + progress + "}\n")
	}
	want.WriteString(`{"event":"update.installed","data":` + installDone + "}\n")
	return want.String()
}

func TestUpdateInstallCommands(t *testing.T) {
	binary := buildMarasi(t)
	installedHuman := "download 0% of 20.0 MiB\n" +
		"download 50% of 20.0 MiB\n" +
		"verify\n" +
		"install\n" +
		"installed 18.10.2026 at /usr/local/bin/marasi; restart instances to run it with marasi service restart\n"

	for _, test := range []struct {
		name   string
		args   []string
		path   string
		body   string
		stderr string
	}{
		{
			name: "should install the update and follow its progress", args: []string{"update", "install"},
			path: "/update/install", stderr: "installing 18.10.2026 to /usr/local/bin/marasi\n" + installedHuman,
		},
		{
			name: "should reinstall with confirmation and follow its progress", args: []string{"update", "reinstall", "--yes"},
			path: "/update/reinstall", body: `{"confirm":true}`, stderr: "reinstalling 18.10.2026 to /usr/local/bin/marasi\n" + installedHuman,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedInstallAPI(t, configDir, http.StatusAccepted, installStarted, installEvents(), false)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil || stdout != "" || stderr != test.stderr {
				t.Fatalf("\nwanted:\nempty stdout, stderr %q\ngot:\nstdout %q, stderr %q, error %v", test.stderr, stdout, stderr, err)
			}
			requests := sent.requests()
			if len(requests) != 2 || requests[0].Path != "/events" || requests[1].Method != http.MethodPost || requests[1].Path != test.path || requests[1].Body != test.body {
				t.Fatalf("\nwanted:\nGET /events, then POST %s %q\ngot:\n%+v", test.path, test.body, requests)
			}

			stdout, stderr, err = runMarasi(binary, append(args, "--json")...)
			if want := installNDJSON(); err != nil || stdout != want || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
			}
		})
	}

	t.Run("should follow a joined install and exit 1 when it fails", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		joined := strings.Replace(installStarted, `"joined":false`, `"joined":true`, 1)
		failed := `{"instance":"work","version":"18.10.2026","kind":"network","step":"download","message":"connection reset","page":"https://example.test/18"}`
		startCannedInstallAPI(t, configDir, http.StatusAccepted, joined, []string{installEvent("update.failed", failed)}, false)
		args := []string{"--config-dir", configDir, "--instance", "work", "update", "install"}

		stdout, stderr, err := runMarasi(binary, args...)
		want := "following the install of 18.10.2026 already running on instance work\n" +
			"Error: installing 18.10.2026 failed at download (network): connection reset; see https://example.test/18\n"
		if err == nil || stdout != "" || stderr != want {
			t.Fatalf("\nwanted:\nexit 1, stderr %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, append(args, "--json")...)
		want = `{"event":"update.failed","data":` + failed + "}\n"
		if err == nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nexit 1, stdout %q, empty stderr\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should report an install the instance refuses", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedInstallAPI(t, configDir, http.StatusConflict, `{"error":"no_update","message":"01.10.2026 is the latest build on the release channel"}`+"\n", nil, false)
		args := []string{"--config-dir", configDir, "--instance", "work", "update", "install"}
		want := "installing update: no_update: 01.10.2026 is the latest build on the release channel"

		stdout, stderr, err := runMarasi(binary, args...)
		if err == nil || stdout != "" || stderr != "Error: "+want+"\n" {
			t.Fatalf("\nwanted:\nerror %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
		stdout, stderr, err = runMarasi(binary, append(args, "--json")...)
		assertJSONCommandError(t, stdout, stderr, err, want)
	})

	t.Run("should exit 1 when the event stream ends before the install does", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedInstallAPI(t, configDir, http.StatusAccepted, installStarted, nil, false)
		args := []string{"--config-dir", configDir, "--instance", "work", "update", "install"}
		want := "instance work closed the event stream before the install of 18.10.2026 finished; run marasi update to see its state"

		_, stderr, err := runMarasi(binary, args...)
		if err == nil || !strings.HasSuffix(stderr, "Error: "+want+"\n") {
			t.Fatalf("\nwanted:\nerror %q\ngot:\nstderr %q, error %v", want, stderr, err)
		}
		stdout, stderr, err := runMarasi(binary, append(args, "--json")...)
		if err == nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nexit 1 with no output after the stream connected\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should stop following on interrupt and leave the install running", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("sending an interrupt needs Unix signals")
		}
		configDir := serviceConfigDir(t)
		sent := startCannedInstallAPI(t, configDir, http.StatusAccepted, installStarted, []string{installEvent("update.progress", installProgress[0])}, true)
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "update", "install")
		stderrPipe, err := command.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(stderrPipe)
		var stderr strings.Builder
		for !strings.Contains(stderr.String(), "download 0%") {
			line, err := reader.ReadString('\n')
			stderr.WriteString(line)
			if err != nil {
				t.Fatalf("wanted progress before interrupting, got %q: %v", stderr.String(), err)
			}
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		rest, _ := io.ReadAll(reader)
		stderr.Write(rest)
		if err := command.Wait(); err != nil {
			t.Fatalf("wanted exit 0 after interrupt, got %v, stderr %q", err, stderr.String())
		}
		want := "stopped following; the install of 18.10.2026 continues on instance work, run marasi update to see it\n"
		if !strings.HasSuffix(stderr.String(), want) {
			t.Fatalf("\nwanted stderr ending:\n%q\ngot:\n%q", want, stderr.String())
		}
		for _, request := range sent.requests() {
			if request.Method == http.MethodDelete {
				t.Fatalf("wanted the install left running, got %+v", request)
			}
		}
	})
}

// updateStatusBody is a GET /update body with an update available whose
// notes run past what human output shows.
const updateStatusBody = `{"installed":{"version":"01.10.2026","channel":"release"},"development":false,"channel":"release","auto_check":true,` +
	`"last_check":{"at":"2026-10-18T09:00:00Z","latest":null,"error":null},` +
	`"available":{"version":"18.10.2026","channel":"release","published":"2026-10-18T06:00:00Z",` +
	`"notes":"\n## Changes\n- one\n- two\n- three\n- four\n- five\n- six\n","page":"https://github.com/tfkr-ae/marasi/releases/tag/18.10.2026"},` +
	`"install":{"state":"idle"},"restart_required":true,"install_path":"/usr/local/bin/marasi"}` + "\n"

const updateStatusHuman = "installed: 01.10.2026 (release)\n" +
	"channel: release\n" +
	"auto check: on\n" +
	"last check: 2026-10-18T09:00:00Z\n" +
	"available: 18.10.2026 (release, published 2026-10-18T06:00:00Z)\n" +
	"page: https://github.com/tfkr-ae/marasi/releases/tag/18.10.2026\n" +
	"notes:\n" +
	"  ## Changes\n" +
	"  - one\n" +
	"  - two\n" +
	"  - three\n" +
	"  - four\n" +
	"  ... 2 more lines on the page\n" +
	"install: idle\n" +
	"restart required: yes\n" +
	"install path: /usr/local/bin/marasi\n"

// updateCurrentBody is a GET /update body after a failed check, with a
// failed install and nothing available.
const updateCurrentBody = `{"installed":{"version":"18.10.2026","channel":"nightly"},"development":false,"channel":"release","auto_check":false,` +
	`"last_check":{"at":"2026-10-18T09:00:00Z","latest":null,"error":"github: 503 Service Unavailable"},"available":null,` +
	`"install":{"state":"failed","version":"18.10.2026","step":"download","received":10,"total":100,` +
	`"error":{"kind":"cancelled","step":"download","message":"the install was cancelled: context canceled","page":""}},` +
	`"restart_required":false,"install_path":"/opt/marasi/marasi"}` + "\n"

const updateCurrentHuman = "installed: 18.10.2026 (nightly)\n" +
	"channel: release\n" +
	"auto check: off\n" +
	"last check: 2026-10-18T09:00:00Z failed: github: 503 Service Unavailable\n" +
	"available: none\n" +
	"install: failed 18.10.2026 at download: the install was cancelled: context canceled\n" +
	"restart required: no\n" +
	"install path: /opt/marasi/marasi\n"

const updateDevelopmentBody = `{"installed":{"version":"18.10.2026 (dev)","channel":null},"development":true,"channel":null,"auto_check":true,` +
	`"last_check":null,"available":null,"install":{"state":"idle"},"restart_required":false,"install_path":"/tmp/marasi"}` + "\n"

const updateDevelopmentHuman = "installed: 18.10.2026 (dev) (development build)\n" +
	"channel: none\n" +
	"auto check: off (development build)\n" +
	"last check: never\n" +
	"available: none\n" +
	"install: idle\n" +
	"restart required: no\n" +
	"install path: /tmp/marasi\n"

const updateRunningBody = `{"installed":{"version":"01.10.2026","channel":"release"},"development":false,"channel":"release","auto_check":true,` +
	`"last_check":{"at":"2026-10-18T09:00:00Z","latest":{"version":"18.10.2026","channel":"release","published":null,"notes":"","page":"https://example.test/18"},"error":null},` +
	`"available":{"version":"18.10.2026","channel":"release","published":null,"notes":"","page":"https://example.test/18"},` +
	`"install":{"state":"running","version":"18.10.2026","step":"download","received":1048576,"total":20971520},` +
	`"restart_required":false,"install_path":"/usr/local/bin/marasi"}` + "\n"

const updateRunningHuman = "installed: 01.10.2026 (release)\n" +
	"channel: release\n" +
	"auto check: on\n" +
	"last check: 2026-10-18T09:00:00Z\n" +
	"available: 18.10.2026 (release)\n" +
	"page: https://example.test/18\n" +
	"install: running 18.10.2026, download 5% of 20.0 MiB\n" +
	"restart required: no\n" +
	"install path: /usr/local/bin/marasi\n"

func TestUpdateStatusCommands(t *testing.T) {
	binary := buildMarasi(t)
	runCannedCommandCases(t, binary, []cannedCommandCase{
		{
			name: "should print the update status with the first lines of the notes", args: []string{"update"},
			status: http.StatusOK, response: updateStatusBody,
			method: http.MethodGet, path: "/update", stdout: updateStatusHuman,
		},
		{
			name: "should print a failed check and a failed install", args: []string{"update"},
			status: http.StatusOK, response: updateCurrentBody,
			method: http.MethodGet, path: "/update", stdout: updateCurrentHuman,
		},
		{
			name: "should print a development build as one that never updates", args: []string{"update"},
			status: http.StatusOK, response: updateDevelopmentBody,
			method: http.MethodGet, path: "/update", stdout: updateDevelopmentHuman,
		},
		{
			name: "should print the progress of a running install", args: []string{"update"},
			status: http.StatusOK, response: updateRunningBody,
			method: http.MethodGet, path: "/update", stdout: updateRunningHuman,
		},
		{
			name: "should check now and print the update status", args: []string{"update", "check"},
			status: http.StatusOK, response: updateStatusBody,
			method: http.MethodPost, path: "/update/check", stdout: updateStatusHuman,
		},
		{
			name: "should print the channel", args: []string{"update", "channel"},
			status: http.StatusOK, response: updateStatusBody,
			method: http.MethodGet, path: "/update", stdout: "channel: release\n",
		},
		{
			name: "should switch the channel", args: []string{"update", "channel", "nightly"},
			status: http.StatusOK, response: strings.Replace(updateStatusBody, `"channel":"release","auto_check"`, `"channel":"nightly","auto_check"`, 1),
			method: http.MethodPut, path: "/update/channel", body: `{"channel":"nightly"}`, stdout: "channel: nightly\n",
		},
		{
			name: "should explain that switching from nightly to release keeps the nightly", args: []string{"update", "channel", "release"},
			status: http.StatusOK, response: updateCurrentBody,
			method: http.MethodPut, path: "/update/channel", body: `{"channel":"release"}`, stdout: "channel: release\n",
			stderr: "the installed nightly build stays until the release channel has a newer build; marasi update reinstall --yes installs the latest release build now\n",
		},
	})
}

func TestUpdateCommandErrors(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should tell the user to start an instance when none is running", func(t *testing.T) {
		for _, args := range [][]string{
			{"update"},
			{"update", "check"},
			{"update", "channel"},
			{"update", "channel", "nightly"},
			{"update", "install"},
			{"update", "reinstall", "--yes"},
		} {
			configDir := serviceConfigDir(t)
			base := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
			want := "instance work is not running; start one with marasi service start --instance work"
			stdout, stderr, err := runMarasi(binary, base...)
			if err == nil || stdout != "" || !strings.Contains(stderr, want) {
				t.Fatalf("%v: wanted error %q, got stdout %q, stderr %q, error %v", args, want, stdout, stderr, err)
			}
			stdout, stderr, err = runMarasi(binary, append(base, "--json")...)
			assertJSONCommandError(t, stdout, stderr, err, want)
		}
	})

	t.Run("should name the default instance without an instance flag", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "update")
		want := "instance default is not running; start one with marasi service start\n"
		if err == nil || !strings.HasSuffix(stderr, want) {
			t.Fatalf("wanted error ending %q, got stderr %q, error %v", want, stderr, err)
		}
	})

	t.Run("should report the control API error", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusConflict, `{"error":"development_build","message":"a development build does not check for or install updates"}`+"\n")
		want := "checking for updates: development_build: a development build does not check for or install updates"
		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "update", "check")
		if err == nil || !strings.Contains(stderr, want) {
			t.Fatalf("wanted error %q, got stderr %q, error %v", want, stderr, err)
		}
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "update", "check", "--json")
		assertJSONCommandError(t, stdout, stderr, err, want)
	})

	t.Run("should refuse an unknown channel before contacting the instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, updateStatusBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "update", "channel", "beta", "--json")
		assertJSONCommandError(t, stdout, stderr, err, `unknown channel "beta": choose release or nightly`)
		if got := sent.requests(); len(got) != 0 {
			t.Fatalf("wanted no request, got %v", got)
		}
	})

	t.Run("should warn and exit 1 when reinstall is not confirmed", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, updateStatusBody)
		want := "reinstalling replaces the installed build with the latest build on its channel, even when that build is older, and projects opened in a newer build may not open in it; rerun with --yes to reinstall"
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "update", "reinstall")
		if err == nil || stdout != "" || !strings.Contains(stderr, want) {
			t.Fatalf("wanted error %q, got stdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "update", "reinstall", "--json")
		assertJSONCommandError(t, stdout, stderr, err, want)
		if got := sent.requests(); len(got) != 0 {
			t.Fatalf("wanted no request, got %v", got)
		}
	})
}
