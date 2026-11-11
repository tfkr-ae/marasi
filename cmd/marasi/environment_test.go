package main

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// envInstanceStatus is the part of service status --json that the
// environment variables for service start decide.
type envInstanceStatus struct {
	Instance        string  `json:"instance"`
	Project         string  `json:"project"`
	ProxyListener   *string `json:"proxy_listener"`
	ControlListener *string `json:"control_listener"`
}

// statusFromEnv reads service status --json with env and args, and stops the
// instance it names when the test ends.
func statusFromEnv(t *testing.T, binary string, env []string, args ...string) envInstanceStatus {
	t.Helper()
	t.Cleanup(func() { runMarasiEnv(binary, env, append(args, "service", "stop")...) })
	stdout, stderr, err := runMarasiEnv(binary, env, append(args, "service", "status", "--json")...)
	if err != nil {
		t.Fatalf("reading status: %v\n%s", err, stderr)
	}
	var status envInstanceStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decoding status %q: %v", stdout, err)
	}
	return status
}

// freePort returns a TCP port on host that nothing listens on.
func freePort(t *testing.T, host string) string {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func TestServiceStartEnvironment(t *testing.T) {
	t.Parallel()

	binary := buildMarasi(t)

	t.Run("should start the instance the variables describe and reach it by them", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		port := freePort(t, "::1")
		env := []string{"MARASI_CONFIG_DIR=" + configDir, "MARASI_INSTANCE=work"}
		start := append([]string{"MARASI_PROJECT_NAME=envproject", "MARASI_ADDRESS=::1", "MARASI_PORT=" + port, "MARASI_CONTROL_ADDRESS=127.0.0.1:0"}, env...)
		if _, stderr, err := runMarasiEnv(binary, start, "service", "start"); err != nil {
			t.Fatalf("starting from the environment: %v\n%s", err, stderr)
		}
		status := statusFromEnv(t, binary, env)
		canonical, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			t.Fatal(err)
		}
		wantProject := filepath.Join(canonical, "projects", "envproject.marasi")
		wantProxy := "[::1]:" + port
		if status.Instance != "work" || status.Project != wantProject || status.ProxyListener == nil || *status.ProxyListener != wantProxy || status.ControlListener == nil || !strings.HasPrefix(*status.ControlListener, "127.0.0.1:") {
			t.Fatalf("\nwanted:\ninstance work, project %s, proxy listener %s, control listener on 127.0.0.1\ngot:\n%+v", wantProject, wantProxy, status)
		}
	})

	t.Run("should open the project file MARASI_PROJECT names", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		project := filepath.Join(configDir, "env.marasi")
		env := []string{"MARASI_CONFIG_DIR=" + configDir}
		if _, stderr, err := runMarasiEnv(binary, append([]string{"MARASI_PROJECT=" + project}, env...), "service", "start", "--port", "0"); err != nil {
			t.Fatalf("starting from MARASI_PROJECT: %v\n%s", err, stderr)
		}
		status := statusFromEnv(t, binary, env)
		if filepath.Base(status.Project) != "env.marasi" {
			t.Fatalf("\nwanted:\nproject %s\ngot:\n%s", project, status.Project)
		}
	})

	t.Run("should take each passed flag over its variable", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		ignored := []string{
			"MARASI_CONFIG_DIR=" + t.TempDir(),
			"MARASI_INSTANCE=ignored",
			"MARASI_PROJECT=" + filepath.Join(t.TempDir(), "ignored.marasi"),
			"MARASI_ADDRESS=::1",
			"MARASI_PORT=1",
			"MARASI_CONTROL_ADDRESS=not an address",
		}
		flags := []string{"--config-dir", configDir, "--instance", "work"}
		start := append(append([]string{}, flags...), "service", "start", "--project-name", "flagged", "--address", "127.0.0.1", "--port", "0", "--control-address", "127.0.0.1:0")
		if _, stderr, err := runMarasiEnv(binary, ignored, start...); err != nil {
			t.Fatalf("starting with flags over variables: %v\n%s", err, stderr)
		}
		status := statusFromEnv(t, binary, ignored, flags...)
		if status.Instance != "work" || filepath.Base(status.Project) != "flagged.marasi" || status.ProxyListener == nil || !strings.HasPrefix(*status.ProxyListener, "127.0.0.1:") || status.ControlListener == nil {
			t.Fatalf("\nwanted:\ninstance work, project flagged.marasi, proxy listener on 127.0.0.1, a control listener\ngot:\n%+v", status)
		}
	})

	t.Run("should let --project ignore MARASI_PROJECT_NAME", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		project := filepath.Join(configDir, "flagged.marasi")
		env := []string{"MARASI_CONFIG_DIR=" + configDir, "MARASI_PROJECT_NAME=ignored"}
		if _, stderr, err := runMarasiEnv(binary, env, "service", "start", "--project", project, "--port", "0"); err != nil {
			t.Fatalf("starting with --project over MARASI_PROJECT_NAME: %v\n%s", err, stderr)
		}
		status := statusFromEnv(t, binary, env)
		if filepath.Base(status.Project) != "flagged.marasi" {
			t.Fatalf("\nwanted:\nproject %s\ngot:\n%s", project, status.Project)
		}
	})

	t.Run("should refuse conflicting variables, conflicting flags, a remote, and a bad value before starting", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		project := filepath.Join(configDir, "env.marasi")
		for _, test := range []struct {
			env  []string
			args []string
			want string
		}{
			{env: []string{"MARASI_PROJECT=" + project, "MARASI_PROJECT_NAME=work"}, want: "MARASI_PROJECT and MARASI_PROJECT_NAME cannot be used together"},
			{args: []string{"--project", project, "--project-name", "work"}, want: "--project and --project-name are mutually exclusive"},
			{env: []string{"MARASI_REMOTE=http://127.0.0.1:1"}, want: "marasi service start acts on this machine and does not accept MARASI_REMOTE"},
			{env: []string{"MARASI_PORT=http"}, want: `invalid MARASI_PORT "http"`},
		} {
			args := append([]string{"--config-dir", configDir, "service", "start"}, test.args...)
			stdout, stderr, err := runMarasiEnv(binary, test.env, args...)
			if exitCode(err) != 1 || stdout != "" || !strings.Contains(stderr, test.want) {
				t.Fatalf("\nwanted:\nusage error %q for %v %v\ngot:\nstdout %q stderr %q error %v", test.want, test.env, test.args, stdout, stderr, err)
			}
			stdout, stderr, err = runMarasiEnv(binary, test.env, append(args, "--json")...)
			assertJSONCommandError(t, stdout, stderr, err, test.want)
		}
		if matches, _ := filepath.Glob(filepath.Join(configDir, "instances", "*.sock")); len(matches) != 0 {
			t.Fatalf("\nwanted:\nno instance started\ngot:\n%v", matches)
		}
	})

	t.Run("should name each variable in its flag's help", func(t *testing.T) {
		for _, test := range []struct {
			args []string
			want []string
		}{
			{args: []string{"--help"}, want: []string{"(env MARASI_CONFIG_DIR)", "(env MARASI_INSTANCE)", "(env MARASI_REMOTE)"}},
			{args: []string{"service", "start", "--help"}, want: []string{"(env MARASI_PROJECT)", "(env MARASI_PROJECT_NAME)", "(env MARASI_ADDRESS)", "(env MARASI_PORT)", "(env MARASI_CONTROL_ADDRESS)"}},
		} {
			stdout, stderr, err := runMarasiEnv(binary, nil, test.args...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", test.args, err, stderr)
			}
			for _, want := range test.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("\nwanted:\n%v help containing %q\ngot:\n%s", test.args, want, stdout)
				}
			}
			if strings.Contains(stdout, "MARASI_JSON") {
				t.Fatalf("\nwanted:\nno MARASI_JSON in %v help\ngot:\n%s", test.args, stdout)
			}
		}
	})

	t.Run("should not read MARASI_JSON", func(t *testing.T) {
		stdout, stderr, err := runMarasiEnv(binary, []string{"MARASI_JSON=1"}, "--config-dir", serviceConfigDir(t), "service", "status")
		if exitCode(err) != 1 || stdout != "" || !strings.Contains(stderr, "not running") {
			t.Fatalf("\nwanted:\nhuman error on stderr only\ngot:\nstdout %q stderr %q error %v", stdout, stderr, err)
		}
	})
}

func TestInstanceSelectionEnvironment(t *testing.T) {
	t.Parallel()

	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	local := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{}`)
	remote, sentRemote := startCannedRemoteAPI(t)
	reset := func() {
		local.reset()
		sentRemote.reset()
	}

	t.Run("should send each command to the instance its flags and variables pick", func(t *testing.T) {
		for _, test := range []struct {
			name       string
			env        []string
			args       []string
			wantRemote bool
		}{
			{name: "MARASI_INSTANCE", env: []string{"MARASI_INSTANCE=work"}},
			{name: "--instance over MARASI_INSTANCE", env: []string{"MARASI_INSTANCE=other"}, args: []string{"--instance", "work"}},
			{name: "--instance over MARASI_REMOTE", env: []string{"MARASI_REMOTE=" + remote}, args: []string{"--instance", "work"}},
			{name: "--remote over MARASI_INSTANCE", env: []string{"MARASI_INSTANCE=work", "MARASI_PASSWORD=secret"}, args: []string{"--remote", remote}, wantRemote: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				reset()
				args := append(append([]string{"--config-dir", configDir}, test.args...), "traffic", "list", "--json")
				if _, stderr, err := runMarasiEnv(binary, test.env, args...); err != nil {
					t.Fatalf("%v: %v\n%s", args, err, stderr)
				}
				gotLocal, gotRemote := len(local.requests()) != 0, len(sentRemote.requests()) != 0
				if gotRemote != test.wantRemote || gotLocal == test.wantRemote {
					t.Fatalf("\nwanted:\nremote %v\ngot:\nlocal request %v, remote request %v", test.wantRemote, gotLocal, gotRemote)
				}
			})
		}
	})

	t.Run("should refuse MARASI_REMOTE with MARASI_INSTANCE, and --remote with --instance, before any request", func(t *testing.T) {
		for _, test := range []struct {
			env  []string
			args []string
			want string
		}{
			{env: []string{"MARASI_REMOTE=" + remote, "MARASI_INSTANCE=work", "MARASI_PASSWORD=secret"}, args: []string{"traffic", "list"}, want: "MARASI_REMOTE and MARASI_INSTANCE cannot be used together"},
			{env: []string{"MARASI_PASSWORD=secret"}, args: []string{"--remote", remote, "--instance", "work", "traffic", "list"}, want: "--remote and --instance cannot be used together"},
			{env: []string{"MARASI_REMOTE=" + remote}, args: []string{"service", "list"}, want: "marasi service list acts on this machine and does not accept MARASI_REMOTE"},
		} {
			reset()
			args := append([]string{"--config-dir", configDir}, test.args...)
			stdout, stderr, err := runMarasiEnv(binary, test.env, args...)
			if exitCode(err) != 1 || stdout != "" || !strings.Contains(stderr, test.want) {
				t.Fatalf("\nwanted:\nusage error %q\ngot:\nstdout %q stderr %q error %v", test.want, stdout, stderr, err)
			}
			if len(local.requests()) != 0 || len(sentRemote.requests()) != 0 {
				t.Fatalf("\nwanted:\nno request for %v %v\ngot:\nlocal %v remote %v", test.env, test.args, local.requests(), sentRemote.requests())
			}
		}
	})
}
