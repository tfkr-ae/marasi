package main

import (
	"net/http"
	"strings"
	"testing"
)

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
