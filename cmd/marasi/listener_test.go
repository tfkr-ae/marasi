package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListenerCommand(t *testing.T) {
	binary := buildMarasi(t)

	for _, test := range []struct {
		name       string
		args       []string
		response   string
		method     string
		path       string
		body       string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "should start with no overrides",
			args:       []string{"listener", "start"},
			response:   `{"status":"active","proxy_listener":"127.0.0.1:53142"}` + "\n",
			method:     http.MethodPost,
			path:       "/listener/start",
			wantStderr: "proxy listener started on 127.0.0.1:53142\n",
		},
		{
			name:       "should start with only an address override",
			args:       []string{"listener", "start", "--address", "localhost"},
			response:   `{"status":"active","proxy_listener":"127.0.0.1:8080"}` + "\n",
			method:     http.MethodPost,
			path:       "/listener/start",
			body:       `{"address":"localhost"}`,
			wantStderr: "proxy listener started on 127.0.0.1:8080\n",
		},
		{
			name:       "should start with only a port override",
			args:       []string{"listener", "start", "--port", "0"},
			response:   `{"status":"active","proxy_listener":"127.0.0.1:53141"}` + "\n",
			method:     http.MethodPost,
			path:       "/listener/start",
			body:       `{"port":0}`,
			wantStderr: "proxy listener started on 127.0.0.1:53141\n",
		},
		{
			name:       "should start with address and port overrides",
			args:       []string{"listener", "start", "--address", "localhost", "--port", "8081"},
			response:   `{"status":"active","proxy_listener":"127.0.0.1:8081"}` + "\n",
			method:     http.MethodPost,
			path:       "/listener/start",
			body:       `{"address":"localhost","port":8081}`,
			wantStderr: "proxy listener started on 127.0.0.1:8081\n",
		},
		{
			name:       "should update only the supplied address",
			args:       []string{"listener", "update", "--address", "localhost"},
			response:   `{"status":"active","proxy_listener":"127.0.0.1:8080"}` + "\n",
			method:     http.MethodPost,
			path:       "/listener/update",
			body:       `{"address":"localhost"}`,
			wantStderr: "proxy listener updated to 127.0.0.1:8080\n",
		},
		{
			name:       "should update only the supplied port",
			args:       []string{"listener", "update", "--port", "0"},
			response:   `{"status":"active","proxy_listener":"127.0.0.1:53143"}` + "\n",
			method:     http.MethodPost,
			path:       "/listener/update",
			body:       `{"port":0}`,
			wantStderr: "proxy listener updated to 127.0.0.1:53143\n",
		},
		{
			name:       "should stop the listener",
			args:       []string{"listener", "stop"},
			response:   `{"status":"inactive","proxy_listener":null}` + "\n",
			method:     http.MethodPost,
			path:       "/listener/stop",
			wantStderr: "proxy listener stopped successfully\n",
		},
		{
			name:       "should print active status",
			args:       []string{"listener", "status"},
			response:   `{"status":"active","proxy_listener":"127.0.0.1:53142"}` + "\n",
			method:     http.MethodGet,
			path:       "/listener/status",
			wantStdout: "status: active\nproxy listener: 127.0.0.1:53142\n",
		},
		{
			name:       "should print inactive status",
			args:       []string{"listener", "status"},
			response:   `{"status":"inactive","proxy_listener":null}` + "\n",
			method:     http.MethodGet,
			path:       "/listener/status",
			wantStdout: "status: inactive\nproxy listener: inactive\n",
		},
		{
			name:       "should print the active listener address",
			args:       []string{"listener", "address"},
			response:   `{"status":"active","proxy_listener":"[::1]:53142"}` + "\n",
			method:     http.MethodGet,
			path:       "/listener/status",
			wantStdout: "[::1]:53142\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)

			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			if got.Method != test.method || got.Path != test.path || got.Body != test.body {
				t.Fatalf("\nwanted:\n%s %s body %q\ngot:\n%s %s body %q", test.method, test.path, test.body, got.Method, got.Path, got.Body)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q", test.wantStdout, test.wantStderr, stdout, stderr)
			}
		})
	}

	t.Run("should project the active listener address in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "json", http.StatusOK, `{"status":"active","proxy_listener":"127.0.0.1:53142"}`+"\n")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "json", "listener", "address", "--json")
		if err != nil || stdout != `{"proxy_listener":"127.0.0.1:53142"}`+"\n" || stderr != "" {
			t.Fatalf("\nwanted:\ncompact address JSON on stdout, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should reject an inactive listener without success output", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "inactive", http.StatusOK, `{"status":"inactive","proxy_listener":null}`+"\n")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "inactive", "listener", "address")
		if err == nil || stdout != "" || !strings.Contains(stderr, "proxy listener is inactive") {
			t.Fatalf("\nwanted:\ninactive listener error with empty stdout\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "inactive", "listener", "address", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "proxy listener is inactive")
	})

	t.Run("should reject an invalid active listener address without success output", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "invalid", http.StatusOK, `{"status":"active","proxy_listener":"not-an-endpoint"}`+"\n")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "invalid", "listener", "address")
		if err == nil || stdout != "" || !strings.Contains(stderr, "invalid listener status") {
			t.Fatalf("\nwanted:\ninvalid listener status with empty stdout\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "invalid", "listener", "address", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "invalid listener status")
	})

	t.Run("should normalize address API and response errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "api", http.StatusBadGateway, `{}`)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "api", "listener", "address")
		if err == nil || stdout != "" || !strings.Contains(stderr, "getting proxy listener address: 502 Bad Gateway") {
			t.Fatalf("\nwanted:\naddress API error with empty stdout\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		configDir = serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "malformed", http.StatusOK, `{`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "malformed", "listener", "address", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "decoding listener status")

		configDir = serviceConfigDir(t)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "listener", "address")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance missing is not running") {
			t.Fatalf("\nwanted:\nunreachable instance error with empty stdout\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	for _, command := range []string{"start", "stop", "update", "status"} {
		t.Run("should pass through the "+command+" response in JSON mode", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			body := " {\n  \"unexpected\": true\n} "
			startCannedControlAPI(t, configDir, "named", http.StatusOK, body)
			args := []string{"--config-dir", configDir, "--instance", "named", "listener", command, "--json"}
			if command == "update" {
				args = append(args, "--port", "8080")
			}

			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", body, stdout, stderr, err)
			}
		})
	}

	t.Run("should normalize unavailable instances and control errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "status", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")

		startCannedControlAPI(t, configDir, "api", http.StatusConflict, `{"error":"listener_inactive"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "api", "listener", "update", "--port", "8080", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "updating proxy listener: listener_inactive")

		startCannedControlAPI(t, configDir, "malformed", http.StatusBadGateway, `{`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "malformed", "listener", "start", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "starting proxy listener: 502 Bad Gateway")
	})

	t.Run("should reject arguments, missing update flags, and invalid ports", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"listener", "start", "extra"},
			{"listener", "stop", "extra"},
			{"listener", "update"},
			{"listener", "status", "extra"},
			{"listener", "address", "extra"},
			{"listener", "start", "--port", "-1"},
			{"listener", "update", "--port", "65536"},
			{"listener", "start", "--port", "0x50"},
			{"listener", "status", "--address", "127.0.0.1"},
			{"listener", "address", "--address", "127.0.0.1"},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should normalize cancellation", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		started, release := startBlockingControlAPI(t, configDir, "work")
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "listener", "status", "--json")
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatalf("starting listener status command: %v", err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			command.Process.Kill()
			t.Fatal("listener status request did not reach the control API")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			command.Process.Kill()
			t.Fatalf("interrupting listener status command: %v", err)
		}
		err := command.Wait()
		close(release)
		assertJSONCommandError(t, stdout.String(), stderr.String(), err, "context canceled")
	})
}

func startBlockingControlAPI(t *testing.T, configDir, name string) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	socketPath, lockPath, err := instanceResourcePaths(configDir, name)
	if err != nil {
		t.Fatalf("resolving instance resources: %v", err)
	}
	listener, lock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		t.Fatalf("claiming instance: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		<-release
	})}
	go server.Serve(listener)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		server.Close()
		releaseInstance(listener, socketPath, lock)
	})
	return started, release
}

func TestListenerCommandLifecycle(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

	_, startStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--project-name", "listener-cli", "--port", "0")
	if err != nil {
		t.Fatalf("starting service: %v", err)
	}
	const startupPrefix = "proxy listener started on "
	startupIndex := strings.Index(startStderr, startupPrefix)
	if startupIndex < 0 {
		t.Fatalf("\nwanted:\nproxy listener startup output\ngot:\n%s", startStderr)
	}
	startedAddress := strings.TrimSpace(startStderr[startupIndex+len(startupPrefix):])

	addressStdout, addressStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "address")
	if err != nil || addressStdout != startedAddress+"\n" || addressStderr != "" {
		t.Fatalf("\nwanted:\naddress %q on stdout, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", startedAddress+"\n", addressStdout, addressStderr, err)
	}

	shell := exec.Command("sh", "-c", `address="$($MARASI --config-dir "$CONFIG" --instance "$INSTANCE" listener address)"; printf '%s' "$address"`)
	shell.Env = append(os.Environ(), "MARASI="+binary, "CONFIG="+configDir, "INSTANCE=work")
	shellOutput, err := shell.Output()
	if err != nil || string(shellOutput) != startedAddress {
		t.Fatalf("\nwanted:\nshell substitution %q\ngot:\n%q, error %v", startedAddress, shellOutput, err)
	}

	_, stopStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "stop")
	if err != nil || stopStderr != "proxy listener stopped successfully\n" {
		t.Fatalf("stopping listener: stderr %q, error %v", stopStderr, err)
	}
	statusStdout, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "status")
	if err != nil || statusStdout != "status: inactive\nproxy listener: inactive\n" {
		t.Fatalf("inactive status: stdout %q, error %v", statusStdout, err)
	}
	_, startStderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "start", "--port", "0")
	if err != nil {
		t.Fatalf("restarting listener: %v", err)
	}
	restartedAddress := strings.TrimSpace(strings.TrimPrefix(startStderr, "proxy listener started on "))
	_, updateStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "update", "--port", "0")
	if err != nil {
		t.Fatalf("updating listener: %v", err)
	}
	updatedAddress := strings.TrimSpace(strings.TrimPrefix(updateStderr, "proxy listener updated to "))
	if updatedAddress == "" || updatedAddress == restartedAddress {
		t.Fatalf("\nwanted:\nlistener moved from %s\ngot:\n%s", restartedAddress, updatedAddress)
	}
	connection, err := net.Dial("tcp", updatedAddress)
	if err != nil {
		t.Fatalf("dialing updated listener: %v", err)
	}
	connection.Close()
	serviceStatus, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
	projectPath, pathErr := filepath.EvalSymlinks(filepath.Join(configDir, "projects", "listener-cli.marasi"))
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if err != nil || !strings.Contains(serviceStatus, fmt.Sprintf("project: %s\nproxy listener: %s\n", projectPath, updatedAddress)) {
		t.Fatalf("project continuity: stdout %q, error %v", serviceStatus, err)
	}
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop"); err != nil {
		t.Fatalf("stopping service: %v", err)
	}
}
