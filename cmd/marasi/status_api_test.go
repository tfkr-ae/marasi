package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteServiceStatusAPILevel(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(directory, "scratchpad.marasi")
	statusBody := func(api string) string {
		return fmt.Sprintf(`{"status":"running","version":"old-build",%s"channel":null,"instance":"work","project":%q,"proxy_listener":null}`, api, project) + "\n"
	}
	humanStatus := func(api string) string {
		return fmt.Sprintf("status: running\nversion: old-build\n%schannel: none\ninstance: work\nproject: %s\nproxy listener: inactive\n", api, project)
	}
	write := func(body string, cliAPI int, asJSON bool) (string, string, error) {
		var stdout, stderr bytes.Buffer
		err := writeRemoteServiceStatus([]byte(body), cliAPI, asJSON, &stdout, &stderr)
		return stdout.String(), stderr.String(), err
	}

	t.Run("should show both levels and no warning when they match", func(t *testing.T) {
		body := statusBody(`"api":1,`)
		stdout, stderr, err := write(body, 1, false)
		wantOut := humanStatus("api: 1\n") + "cli version: dev\ncli api: 1\n"
		if err != nil || stdout != wantOut || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q stderr %q error %v", wantOut, stdout, stderr, err)
		}
		stdout, stderr, err = write(body, 1, true)
		wantJSON := strings.TrimSuffix(body, "}\n") + `,"cli_version":"dev","cli_api":1}` + "\n"
		if err != nil || stdout != wantJSON || stderr != "" {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q stderr %q error %v", wantJSON, stdout, stderr, err)
		}
	})

	t.Run("should warn when the levels differ", func(t *testing.T) {
		body := statusBody(`"api":2,`)
		warning := "this marasi speaks API level 1 but the instance speaks API level 2"
		stdout, stderr, err := write(body, 1, false)
		wantOut := humanStatus("api: 2\n") + "cli version: dev\ncli api: 1\n"
		if err != nil || stdout != wantOut || stderr != "warning: "+warning+"\n" {
			t.Fatalf("\nwanted:\nstdout %q stderr %q\ngot:\nstdout %q stderr %q error %v", wantOut, "warning: "+warning+"\n", stdout, stderr, err)
		}
		stdout, stderr, err = write(body, 1, true)
		wantJSON := strings.TrimSuffix(body, "}\n") + `,"cli_version":"dev","cli_api":1,"warning":"` + warning + `"}` + "\n"
		if err != nil || stdout != wantJSON || stderr != "" {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q stderr %q error %v", wantJSON, stdout, stderr, err)
		}
	})

	t.Run("should warn when the instance predates API levels", func(t *testing.T) {
		body := statusBody("")
		warning := "this marasi speaks API level 1 but the instance reports no API level"
		stdout, stderr, err := write(body, 1, false)
		wantOut := humanStatus("") + "cli version: dev\ncli api: 1\n"
		if err != nil || stdout != wantOut || stderr != "warning: "+warning+"\n" {
			t.Fatalf("\nwanted:\nstdout %q stderr %q\ngot:\nstdout %q stderr %q error %v", wantOut, "warning: "+warning+"\n", stdout, stderr, err)
		}
		stdout, stderr, err = write(body, 1, true)
		wantJSON := strings.TrimSuffix(body, "}\n") + `,"cli_version":"dev","cli_api":1,"warning":"` + warning + `"}` + "\n"
		if err != nil || stdout != wantJSON || stderr != "" {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q stderr %q error %v", wantJSON, stdout, stderr, err)
		}
	})

	t.Run("should not warn about differing versions when the levels match", func(t *testing.T) {
		_, stderr, err := write(statusBody(`"api":1,`), 1, false)
		if err != nil || stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\nstderr %q error %v", stderr, err)
		}
	})

	t.Run("should reject an api that is not a positive integer", func(t *testing.T) {
		for _, api := range []string{`"api":"1",`, `"api":0,`, `"api":1.5,`, `"api":null,`} {
			for _, asJSON := range []bool{false, true} {
				_, _, err := write(statusBody(api), 1, asJSON)
				if want := "invalid service status: api must be a positive integer"; err == nil || err.Error() != want {
					t.Fatalf("\n%s json=%t wanted:\n%s\ngot:\n%v", api, asJSON, want, err)
				}
			}
		}
	})
}
