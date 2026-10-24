package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// runMarasiEnv runs the binary with env added to an environment that has no
// MARASI_REMOTE or MARASI_TOKEN of its own, so the test decides both.
func runMarasiEnv(binary string, env []string, args ...string) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	command := exec.Command(binary, args...)
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "MARASI_REMOTE=") && !strings.HasPrefix(variable, "MARASI_TOKEN=") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, env...)
	command.Stdout = &outBuf
	command.Stderr = &errBuf
	err = command.Run()
	return outBuf.String(), errBuf.String(), err
}

// exitCode is the exit status of a finished command: 0 for no error, -1 for
// a command that did not run.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// remoteInstance is a real service instance reachable at a network address,
// and a CLI config dir on a stand-in for a second machine.
type remoteInstance struct {
	binary    string
	configDir string // the instance machine's config dir
	cliDir    string // the CLI's config dir, which holds no instance
	url       string // the network address as a --remote URL
	token     string
}

// startRemoteInstance starts instance work with a network address on
// 127.0.0.1 and reads its address and control token through the socket.
func startRemoteInstance(t *testing.T, binary string) remoteInstance {
	t.Helper()
	configDir := serviceConfigDir(t)
	startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
	return remoteInstance{
		binary:    binary,
		configDir: configDir,
		cliDir:    t.TempDir(),
		url:       "http://" + *controlAddressOf(t, binary, configDir, "work"),
		token:     controlTokenOf(t, binary, configDir, "work"),
	}
}

// local runs a command against the instance through its socket.
func (r remoteInstance) local(args ...string) (stdout, stderr string, err error) {
	return runMarasiEnv(r.binary, nil, append([]string{"--config-dir", r.configDir, "--instance", "work"}, args...)...)
}

// remote runs a command against the instance over --remote with
// MARASI_TOKEN, from the CLI's own config dir.
func (r remoteInstance) remote(args ...string) (stdout, stderr string, err error) {
	return runMarasiEnv(r.binary, []string{"MARASI_TOKEN=" + r.token}, append([]string{"--config-dir", r.cliDir, "--remote", r.url}, args...)...)
}

func TestRemote(t *testing.T) {
	binary := buildMarasi(t)
	instance := startRemoteInstance(t, binary)

	t.Run("should give the same output over --remote as over the socket", func(t *testing.T) {
		for _, args := range [][]string{
			{"service", "status"},
			{"service", "status", "--json"},
			{"traffic", "list"},
			{"traffic", "list", "--json"},
			{"listener", "status", "--json"},
			{"scope", "check", "https://example.com/", "--json"},
			{"traffic", "get", "00000000-0000-0000-0000-000000000000"},
			{"traffic", "get", "00000000-0000-0000-0000-000000000000", "--json"},
		} {
			wantOut, wantErr, wantRunErr := instance.local(args...)
			gotOut, gotErr, gotRunErr := instance.remote(args...)
			if gotOut != wantOut || gotErr != wantErr || exitCode(gotRunErr) != exitCode(wantRunErr) {
				t.Fatalf("\nwanted:\n%v: stdout %q stderr %q exit %d\ngot:\nstdout %q stderr %q exit %d", args, wantOut, wantErr, exitCode(wantRunErr), gotOut, gotErr, exitCode(gotRunErr))
			}
		}
	})
}

func TestRemoteSelection(t *testing.T) {
	binary := buildMarasi(t)
	instance := startRemoteInstance(t, binary)
	wantStatus, _, err := instance.local("service", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(instance.token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wrongPath := filepath.Join(t.TempDir(), "wrong")
	if err := os.WriteFile(wrongPath, []byte(strings.Repeat("0", len(instance.token))), 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("should reach the instance named by MARASI_REMOTE, or by --remote over it", func(t *testing.T) {
		for _, test := range []struct {
			name string
			env  []string
			args []string
		}{
			{name: "MARASI_REMOTE", env: []string{"MARASI_REMOTE=" + instance.url, "MARASI_TOKEN=" + instance.token}},
			{name: "--remote over MARASI_REMOTE", env: []string{"MARASI_REMOTE=http://127.0.0.1:1", "MARASI_TOKEN=" + instance.token}, args: []string{"--remote", instance.url}},
			{name: "--token-file", args: []string{"--remote", instance.url, "--token-file", tokenPath}},
			{name: "--token-file over MARASI_TOKEN", env: []string{"MARASI_TOKEN=wrong"}, args: []string{"--remote", instance.url, "--token-file", tokenPath}},
		} {
			args := append([]string{"--config-dir", instance.cliDir}, test.args...)
			stdout, stderr, err := runMarasiEnv(binary, test.env, append(args, "service", "status", "--json")...)
			if err != nil || stdout != wantStatus || stderr != "" {
				t.Fatalf("\nwanted:\n%s: %s\ngot:\nstdout %q stderr %q error %v", test.name, wantStatus, stdout, stderr, err)
			}
		}
	})

	t.Run("should print a token error when the instance rejects the control token", func(t *testing.T) {
		want := "control token rejected by " + instance.url + ": check MARASI_TOKEN or --token-file"
		args := []string{"--config-dir", instance.cliDir, "--remote", instance.url, "--token-file", wrongPath}
		for _, command := range [][]string{{"service", "status"}, {"traffic", "list"}, {"events"}, {"service", "restart"}} {
			stdout, stderr, err := runMarasiEnv(binary, nil, append(args, command...)...)
			if exitCode(err) != 1 || stdout != "" || !strings.Contains(stderr, "Error: "+want+"\n") {
				t.Fatalf("\nwanted:\n%v: %s\ngot:\nstdout %q stderr %q error %v", command, want, stdout, stderr, err)
			}
			stdout, stderr, err = runMarasiEnv(binary, nil, append(append(args, command...), "--json")...)
			assertJSONCommandError(t, stdout, stderr, err, want)
		}
	})

	t.Run("should say no instance answers at a remote URL nothing listens on", func(t *testing.T) {
		stdout, stderr, err := runMarasiEnv(binary, []string{"MARASI_TOKEN=" + instance.token}, "--config-dir", instance.cliDir, "--remote", "http://127.0.0.1:1", "service", "restart")
		want := "Error: no instance answers at http://127.0.0.1:1\n"
		if exitCode(err) != 1 || stdout != "" || !strings.HasSuffix(stderr, want) {
			t.Fatalf("\nwanted:\n%q\ngot:\nstdout %q stderr %q error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should skip the local socket-path length check", func(t *testing.T) {
		longDir := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
		stdout, stderr, err := runMarasiEnv(binary, []string{"MARASI_TOKEN=" + instance.token}, "--config-dir", longDir, "--remote", instance.url, "service", "status", "--json")
		if err != nil || stdout != wantStatus || stderr != "" {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q stderr %q error %v", wantStatus, stdout, stderr, err)
		}
	})

	t.Run("should refuse service start, service list, and for now service stop with --remote", func(t *testing.T) {
		for _, test := range []struct {
			env  []string
			args []string
			want string
		}{
			{args: []string{"--remote", instance.url, "service", "start"}, want: "marasi service start acts on this machine and does not accept --remote"},
			{args: []string{"--remote", instance.url, "service", "list"}, want: "marasi service list acts on this machine and does not accept --remote"},
			{env: []string{"MARASI_REMOTE=" + instance.url}, args: []string{"service", "list"}, want: "marasi service list acts on this machine and does not accept MARASI_REMOTE"},
			{args: []string{"--remote", instance.url, "service", "stop"}, want: "marasi service stop does not support --remote yet"},
		} {
			env := append([]string{"MARASI_TOKEN=" + instance.token}, test.env...)
			args := append([]string{"--config-dir", instance.cliDir}, test.args...)
			stdout, stderr, err := runMarasiEnv(binary, env, args...)
			if exitCode(err) != 1 || stdout != "" || stderr != "Error: "+test.want+"\n" {
				t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q stderr %q error %v", test.want, stdout, stderr, err)
			}
			stdout, stderr, err = runMarasiEnv(binary, env, append(args, "--json")...)
			assertJSONCommandError(t, stdout, stderr, err, test.want)
		}
		if _, err := os.Stat(filepath.Join(instance.cliDir, "instances")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\nno local instance\ngot:\n%v", err)
		}
	})

	t.Run("should stream events over --remote", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.Interrupt cannot be sent to a Windows process")
		}
		stdoutReader, stdoutWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stdoutReader.Close()
			stdoutWriter.Close()
		})
		command := exec.Command(binary, "--json", "--config-dir", instance.cliDir, "--remote", instance.url, "events")
		command.Env = append(os.Environ(), "MARASI_TOKEN="+instance.token)
		command.Stdout = stdoutWriter
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if command.ProcessState == nil {
				command.Process.Kill()
				command.Wait()
			}
		})
		lines := make(chan string)
		go func() {
			reader := bufio.NewReader(stdoutReader)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					close(lines)
					return
				}
				lines <- line
			}
		}()

		// The JSON stream prints nothing on connect, so create launchpads
		// until one created after the subscription arrives.
		deadline := time.After(10 * time.Second)
		for {
			if _, stderr, err := instance.remote("launchpad", "create", "--name", "remote", "--json"); err != nil {
				t.Fatalf("creating launchpad: %v\n%s", err, stderr)
			}
			select {
			case line := <-lines:
				if !strings.HasPrefix(line, `{"event":"launchpad.created"`) {
					t.Fatalf("\nwanted:\nlaunchpad.created\ngot:\n%s", line)
				}
				if err := command.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
				if err := command.Wait(); err != nil || stderr.String() != "" {
					t.Fatalf("\nwanted:\nexit 0 and empty stderr\ngot:\n%v %q", err, stderr.String())
				}
				return
			case <-time.After(100 * time.Millisecond):
			case <-deadline:
				t.Fatal("events over --remote printed no event")
			}
		}
	})
}

// startCannedRemoteAPI serves a canned control API at a network address and
// returns its URL and the requests it received.
func startCannedRemoteAPI(t *testing.T) (string, *cannedControlRequest) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := &cannedControlRequest{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.mu.Lock()
		got.Method, got.Path = r.Method, r.URL.Path
		got.history = append(got.history, got.controlRequest)
		got.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String(), got
}

func TestRemoteUsageErrors(t *testing.T) {
	binary := buildMarasi(t)
	remote, sent := startCannedRemoteAPI(t)
	emptyToken := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(emptyToken, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("should fail before any request on --instance with a remote or a missing token", func(t *testing.T) {
		for _, test := range []struct {
			env  []string
			args []string
			want string
		}{
			{env: []string{"MARASI_TOKEN=secret"}, args: []string{"--remote", remote, "--instance", "work", "service", "status"}, want: "--remote and --instance cannot be used together"},
			{env: []string{"MARASI_TOKEN=secret", "MARASI_REMOTE=" + remote}, args: []string{"--instance", "default", "traffic", "list"}, want: "MARASI_REMOTE and --instance cannot be used together"},
			{args: []string{"--remote", remote, "service", "status"}, want: "--remote needs the instance's control token: set MARASI_TOKEN or pass --token-file PATH"},
			{env: []string{"MARASI_REMOTE=" + remote}, args: []string{"events"}, want: "--remote needs the instance's control token"},
			{env: []string{"MARASI_TOKEN=secret"}, args: []string{"--remote", remote, "--token-file", emptyToken, "service", "status"}, want: "control token file " + emptyToken + " is empty"},
			{env: []string{"MARASI_TOKEN=secret"}, args: []string{"--remote", remote, "--token-file", filepath.Join(t.TempDir(), "missing"), "service", "status"}, want: "reading control token file"},
			{env: []string{"MARASI_TOKEN=secret"}, args: []string{"--remote", "ftp://" + strings.TrimPrefix(remote, "http://"), "service", "status"}, want: "must start with http:// or https://"},
			{env: []string{"MARASI_TOKEN=secret"}, args: []string{"--remote", remote + "?token=secret", "service", "status"}, want: "must not have a user, query, or fragment"},
			{args: []string{"--token-file", emptyToken, "service", "status"}, want: "--token-file needs --remote or MARASI_REMOTE"},
		} {
			args := append([]string{"--config-dir", t.TempDir()}, test.args...)
			stdout, stderr, err := runMarasiEnv(binary, test.env, args...)
			if exitCode(err) != 1 || stdout != "" || !strings.Contains(stderr, test.want) {
				t.Fatalf("\nwanted:\nusage error %q for %v\ngot:\nstdout %q stderr %q error %v", test.want, test.args, stdout, stderr, err)
			}
			stdout, stderr, err = runMarasiEnv(binary, test.env, append(args, "--json")...)
			assertJSONCommandError(t, stdout, stderr, err, test.want)
			if got := sent.requests(); len(got) != 0 {
				t.Fatalf("\nwanted:\nno request for %v\ngot:\n%v", test.args, got)
			}
		}
	})

	t.Run("should send the remote URL's host and the token as a bearer", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		requests := make(chan *http.Request, 1)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"items":[],"next_cursor":null}`)
		})}
		go server.Serve(listener)
		t.Cleanup(func() { server.Close() })
		address := listener.Addr().String()

		_, stderr, err := runMarasiEnv(binary, []string{"MARASI_TOKEN=secret"}, "--config-dir", t.TempDir(), "--remote", "http://"+address+"/prefix/", "traffic", "list", "--json", "--limit", "5")
		if err != nil {
			t.Fatalf("traffic list: %v\n%s", err, stderr)
		}
		got := <-requests
		if got.Host != address || got.URL.Path != "/prefix/traffic" || got.URL.Query().Get("limit") != "5" || got.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("\nwanted:\nHost %s, /prefix/traffic?limit=5, Bearer secret\ngot:\nHost %s, %s?%s, %q", address, got.Host, got.URL.Path, got.URL.RawQuery, got.Header.Get("Authorization"))
		}
	})
}

func TestRemoteRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("restart fixture renames over a running executable")
	}

	t.Run("should restart the instance onto its installed build over --remote", func(t *testing.T) {
		binary := buildMarasiVersion(t, "old-build")
		next := buildMarasiVersion(t, "new-build")
		instance := startRemoteInstance(t, binary)
		installBuild(t, next, binary)

		stdout, stderr, err := instance.remote("--json", "service", "restart")
		if err != nil || stderr != "" {
			t.Fatalf("restart: stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		var result struct {
			Instance string `json:"instance"`
			Status   string `json:"status"`
			Version  string `json:"version"`
		}
		if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Instance != "work" || result.Status != "restarted" || result.Version != "new-build" {
			t.Fatalf("\nwanted:\nwork restarted on new-build\ngot:\n%s %v", stdout, err)
		}
		stdout, stderr, err = instance.remote("--json", "service", "status")
		if err != nil || !strings.Contains(stdout, `"version":"new-build"`) {
			t.Fatalf("\nwanted:\nnew-build at the same address and token\ngot:\nstdout %q stderr %q error %v", stdout, stderr, err)
		}
	})
}
