package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// testControlPassword is the control password tests start instances with.
const testControlPassword = "correct horse battery staple"

// startControlAddressInstance starts name with its control listener also
// reachable at controlAddress, guarded by testControlPassword from
// MARASI_PASSWORD, and returns the start command's stderr.
func startControlAddressInstance(t *testing.T, binary, configDir, name, controlAddress string) string {
	t.Helper()
	return startControlAddressInstanceWith(t, binary, configDir, name, controlAddress, []string{"MARASI_PASSWORD=" + testControlPassword})
}

// startControlAddressInstanceWith starts name with its control listener also
// reachable at controlAddress, with env as its MARASI_ variables and args
// added to service start, and returns the start command's stderr.
func startControlAddressInstanceWith(t *testing.T, binary, configDir, name, controlAddress string, env []string, args ...string) string {
	t.Helper()
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "stop") })
	_, stderr, err := runMarasiEnv(binary, env, append([]string{"--config-dir", configDir, "--instance", name, "service", "start", "--project-name", name, "--port", "0", "--control-address", controlAddress}, args...)...)
	if err != nil {
		t.Fatalf("starting %s: %v\n%s", name, err, stderr)
	}
	return stderr
}

// instanceArguments fails unless some running process has configDir in its
// arguments, and returns the arguments of each such process.
func instanceArguments(t *testing.T, configDir string) []string {
	t.Helper()
	output, err := exec.Command("ps", "-axww", "-o", "args=").Output()
	if err != nil {
		t.Fatalf("listing process arguments: %v", err)
	}
	var instances []string
	for _, args := range strings.Split(string(output), "\n") {
		if strings.Contains(args, configDir) {
			instances = append(instances, args)
		}
	}
	if len(instances) == 0 {
		t.Fatalf("\nwanted:\na running instance with %s in its arguments\ngot:\nnone", configDir)
	}
	return instances
}

// assertPasswordNotInArguments fails unless some running process has
// configDir in its arguments, and none of those has testControlPassword.
func assertPasswordNotInArguments(t *testing.T, configDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	for _, args := range instanceArguments(t, configDir) {
		if strings.Contains(args, testControlPassword) {
			t.Fatalf("\nwanted:\narguments without the control password\ngot:\n%s", args)
		}
	}
}

// controlAddressOf reads the control listener's network address from
// service status --json.
func controlAddressOf(t *testing.T, binary, configDir, name string) *string {
	t.Helper()
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "status", "--json")
	if err != nil {
		t.Fatalf("reading status of %s: %v\n%s", name, err, stderr)
	}
	var status struct {
		ControlListener *string `json:"control_listener"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decoding status %q: %v", stdout, err)
	}
	return status.ControlListener
}

// networkRequest sends one raw HTTP request to the network address and
// returns its status, headers, and body. authorization is sent as the
// Authorization header when it is not empty.
func networkRequest(t *testing.T, method, address, path, authorization string) (int, http.Header, string) {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		t.Fatalf("sending %s %s to the network address: %v", method, path, err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return response.StatusCode, response.Header, string(body)
}

func TestControlAddress(t *testing.T) {
	binary := buildMarasi(t)

	const unauthorized = "{\"error\":\"unauthorized\",\"message\":\"send the control password as Authorization: Bearer\"}\n"

	t.Run("should serve the control API at the network address only with the control password from MARASI_PASSWORD or --password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "env", "127.0.0.1:0")
		startControlAddressInstanceWith(t, binary, configDir, "flag", "127.0.0.1:0", nil, "--password", testControlPassword)
		assertPasswordNotInArguments(t, configDir)

		for _, name := range []string{"env", "flag"} {
			address := controlAddressOf(t, binary, configDir, name)
			if address == nil || !strings.HasPrefix(*address, "127.0.0.1:") || strings.HasSuffix(*address, ":0") {
				t.Fatalf("\nwanted:\ncontrol_listener 127.0.0.1 with its bound port\ngot:\n%v", address)
			}

			status, header, body := networkRequest(t, http.MethodGet, *address, "/service/status", "")
			if status != http.StatusUnauthorized || header.Get("Content-Type") != "application/json" || header.Get("WWW-Authenticate") != "Bearer" || body != unauthorized {
				t.Fatalf("\nwanted:\n401 application/json %s\ngot:\n%d %s %s", unauthorized, status, header.Get("Content-Type"), body)
			}

			status, _, body = networkRequest(t, http.MethodGet, *address, "/service/status", "Bearer "+testControlPassword)
			_, socketBody := doInstanceRequest(t, filepath.Join(configDir, "instances", name+".sock"), http.MethodGet, "/service/status", "")
			if status != http.StatusOK || body != socketBody {
				t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", socketBody, status, body)
			}
		}
	})

	t.Run("should refuse a wrong or misplaced password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")

		for _, test := range []struct {
			name          string
			path          string
			authorization string
		}{
			{name: "a wrong password", path: "/service/status", authorization: "Bearer " + strings.Repeat("x", len(testControlPassword))},
			{name: "a prefix of the password", path: "/service/status", authorization: "Bearer " + testControlPassword[:5]},
			{name: "a longer password", path: "/service/status", authorization: "Bearer " + testControlPassword + "0"},
			{name: "another scheme", path: "/service/status", authorization: "Basic " + testControlPassword},
			{name: "a query-string password", path: "/service/status?password=" + url.QueryEscape(testControlPassword) + "&access_token=" + url.QueryEscape(testControlPassword)},
		} {
			status, header, body := networkRequest(t, http.MethodGet, address, test.path, test.authorization)
			if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") != "Bearer" || body != unauthorized {
				t.Fatalf("\nwanted:\n401 for %s\ngot:\n%d %q %s", test.name, status, header.Get("WWW-Authenticate"), body)
			}
		}
		status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", "bearer "+testControlPassword)
		if status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 for a lowercase scheme\ngot:\n%d %s", status, body)
		}
	})

	t.Run("should forget the password when the instance stops and ignore a token file", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		const staleToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		tokenPath := filepath.Join(configDir, "instances", "work.token")
		if err := os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tokenPath, []byte(staleToken+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", "Bearer "+staleToken); status != http.StatusUnauthorized {
			t.Fatalf("\nwanted:\n401 for the old token file's token\ngot:\n%d %s", status, body)
		}

		if _, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop"); err != nil {
			t.Fatalf("stopping work: %v\n%s", err, stderr)
		}
		entries, err := os.ReadDir(filepath.Join(configDir, "instances"))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			content, err := os.ReadFile(filepath.Join(configDir, "instances", entry.Name()))
			if err == nil && strings.Contains(string(content), testControlPassword) {
				t.Fatalf("\nwanted:\nno file holding the password\ngot:\n%s", entry.Name())
			}
		}

		startControlAddressInstanceWith(t, binary, configDir, "work", "127.0.0.1:0", nil)
		address = *controlAddressOf(t, binary, configDir, "work")
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", ""); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 with no password after a start without one\ngot:\n%d %s", status, body)
		}
	})

	t.Run("should print the network address before the proxy listener line and in JSON", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stderr := startControlAddressInstance(t, binary, configDir, "work", "localhost:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		proxyListener := strings.TrimSpace(stderr[strings.Index(stderr, "proxy listener started on ")+len("proxy listener started on "):])
		want := "instance work started\ncontrol listener network address started on " + address + "\nproxy listener started on " + proxyListener + "\n"
		if stderr != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, stderr)
		}

		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "json", "service", "stop") })
		stdout, stderr, err := runMarasiEnv(binary, []string{"MARASI_PASSWORD=" + testControlPassword}, "--config-dir", configDir, "--instance", "json", "service", "start", "--project-name", "json", "--port", "0", "--control-address", "127.0.0.1:0", "--json")
		if err != nil || stderr != "" {
			t.Fatalf("starting json: %v\n%s", err, stderr)
		}
		jsonAddress := controlAddressOf(t, binary, configDir, "json")
		var started struct {
			Instance        string  `json:"instance"`
			ProxyListener   string  `json:"proxy_listener"`
			ControlListener *string `json:"control_listener"`
		}
		if err := json.Unmarshal([]byte(stdout), &started); err != nil || started.Instance != "json" || started.ControlListener == nil || *started.ControlListener != *jsonAddress {
			t.Fatalf("\nwanted:\ncontrol_listener %s\ngot:\n%s", *jsonAddress, stdout)
		}
	})

	// The tests run without a terminal, so stderr carries no warning; the
	// warning goes to stderr only when it is a terminal.
	t.Run("should warn in the instance log, and not on a stderr that is no terminal, when the address is beyond loopback", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stderr := startControlAddressInstance(t, binary, configDir, "work", "0.0.0.0:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		warning := "warning: control listener network address " + address + " is reachable beyond loopback and serves plain HTTP without TLS; protect it with an SSH tunnel, a VPN, or a TLS reverse proxy\n"
		if strings.Contains(stderr, "warning") || !strings.HasPrefix(stderr, "instance work started\ncontrol listener network address started on ") {
			t.Fatalf("\nwanted:\nno warning line on stderr\ngot:\n%s", stderr)
		}
		log, err := os.ReadFile(filepath.Join(configDir, "instances", "work.log"))
		if err != nil || strings.Count(string(log), warning) != 1 {
			t.Fatalf("\nwanted:\none warning line in the instance log\ngot:\n%s %v", log, err)
		}

		loopback := startControlAddressInstance(t, binary, configDir, "local", "127.0.0.1:0")
		localLog, err := os.ReadFile(filepath.Join(configDir, "instances", "local.log"))
		if err != nil || strings.Contains(loopback, "warning") || strings.Contains(string(localLog), "warning") {
			t.Fatalf("\nwanted:\nno warning for loopback\ngot:\n%s\n%s %v", loopback, localLog, err)
		}
	})

	t.Run("should fail to start on an address in use and leave the name free", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()

		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--port", "0", "--control-address", occupied.Addr().String())
		wantError := "binding control listener network address on " + occupied.Addr().String() + ": "
		if err == nil || !strings.Contains(stderr, wantError) || !strings.Contains(stderr, "address already in use") {
			t.Fatalf("\nwanted:\nexit 1 with %q\ngot:\n%s %v", wantError, stderr, err)
		}
		if _, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status"); err == nil || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("\nwanted:\ninstance work is not running\ngot:\n%s %v", stderr, err)
		}
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
	})

	t.Run("should show the network address in service status and service list, and never the password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := controlAddressOf(t, binary, configDir, "work")
		token := testControlPassword
		wantLine := "control listener network address: " + *address + "\n"

		for _, args := range [][]string{
			{"--instance", "work", "service", "status"},
			{"--instance", "work", "service", "status", "--json"},
			{"service", "list"},
			{"service", "list", "--json"},
		} {
			stdout, stderr, err := runMarasi(binary, append([]string{"--config-dir", configDir}, args...)...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", args, err, stderr)
			}
			if strings.Contains(stdout, token) {
				t.Fatalf("\nwanted:\n%v without the control password\ngot:\n%s", args, stdout)
			}
			if args[len(args)-1] == "--json" {
				if !strings.Contains(stdout, "\"control_listener\":\""+*address+"\"") {
					t.Fatalf("\nwanted:\n%v with control_listener %s\ngot:\n%s", args, *address, stdout)
				}
			} else if !strings.Contains(stdout, wantLine) {
				t.Fatalf("\nwanted:\n%v with %q\ngot:\n%s", args, wantLine, stdout)
			}
		}

		log, err := os.ReadFile(filepath.Join(configDir, "instances", "work.log"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(log), token) {
			t.Fatalf("\nwanted:\ninstance log without the control password\ngot:\n%s", log)
		}
	})

	t.Run("should report whether a password is set in the control listener status and events, and never the password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		startControlAddressInstanceWith(t, binary, configDir, "open", "127.0.0.1:0", nil)
		_, events, closeEvents := connectInstanceEvents(t, filepath.Join(configDir, "instances", "work.sock"))
		defer closeEvents()

		for _, test := range []struct {
			name  string
			set   bool
			human string
		}{
			{name: "work", set: true, human: "set"},
			{name: "open", set: false, human: "none"},
		} {
			address := *controlAddressOf(t, binary, configDir, test.name)
			stdout, _ := runServiceControl(t, binary, configDir, test.name, "status", "--json")
			if want := `{"status":"active","control_listener":"` + address + `","password_set":` + strconv.FormatBool(test.set) + `,"allowed_origins":[]}` + "\n"; stdout != want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
			}
			stdout, _ = runServiceControl(t, binary, configDir, test.name, "status")
			if want := "status: active\ncontrol listener network address: " + address + "\npassword: " + test.human + "\nallowed origins: none\n"; stdout != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, stdout)
			}
		}

		updateOut, _ := runServiceControl(t, binary, configDir, "work", "update", "--address", "127.0.0.1", "--port", "0", "--json")
		_, updated := readEventNamed(t, events, "control_listener.updated")
		runServiceControl(t, binary, configDir, "work", "stop")
		_, stopped := readEventNamed(t, events, "control_listener.stopped")
		startOut, _ := runServiceControl(t, binary, configDir, "work", "start", "--address", "127.0.0.1", "--port", "0", "--json")
		_, started := readEventNamed(t, events, "control_listener.started")
		for _, data := range []string{updateOut, updated, startOut, started} {
			if !strings.Contains(data, `"status":"active"`) || !strings.HasSuffix(strings.TrimSpace(data), `,"password_set":true,"allowed_origins":[]}`) || strings.Contains(data, testControlPassword) {
				t.Fatalf("\nwanted:\nan active status with password_set true and no password\ngot:\n%s", data)
			}
		}
		if want := `{"status":"inactive","control_listener":null,"password_set":true,"allowed_origins":[]}`; stopped != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stopped)
		}
		address := *controlAddressOf(t, binary, configDir, "work")
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", ""); status != http.StatusUnauthorized {
			t.Fatalf("\nwanted:\n401 on a restarted network address that keeps the password\ngot:\n%d %s", status, body)
		}
	})

	t.Run("should refuse a password that cannot be sent as a bearer header", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, test := range []struct {
			env  []string
			args []string
			want string
		}{
			{args: []string{"--password", " padded"}, want: "invalid --password: a control password cannot start or end with whitespace or contain control characters"},
			{env: []string{"MARASI_PASSWORD=line\nbreak"}, want: "invalid MARASI_PASSWORD: a control password cannot start or end with whitespace or contain control characters"},
		} {
			_, stderr, err := runMarasiEnv(binary, test.env, append([]string{"--config-dir", configDir, "--instance", "work", "service", "start", "--port", "0", "--control-address", "127.0.0.1:0"}, test.args...)...)
			if exitCode(err) != 1 || !strings.Contains(stderr, test.want) {
				t.Fatalf("\nwanted:\nexit 1 with %q\ngot:\n%s %v", test.want, stderr, err)
			}
		}
	})

	t.Run("should remove the control token routes, command, and flags", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		socketPath := filepath.Join(configDir, "instances", "work.sock")
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/service/control-listener/token"},
			{http.MethodPost, "/service/control-listener/token/rotate"},
		} {
			if status, body := doInstanceRequest(t, socketPath, route.method, route.path, ""); status != http.StatusNotFound {
				t.Fatalf("\nwanted:\n404 for %s %s\ngot:\n%d %s", route.method, route.path, status, body)
			}
		}
		for _, test := range []struct {
			args []string
			want string
		}{
			{args: []string{"--instance", "work", "service", "control", "token"}, want: `unknown command "token" for "marasi service control"`},
			{args: []string{"--instance", "work", "service", "control", "token", "--rotate"}, want: "unknown flag: --rotate"},
			{args: []string{"--token-file", "token", "--instance", "work", "service", "status"}, want: "unknown flag: --token-file"},
		} {
			stdout, stderr, err := runMarasi(binary, append([]string{"--config-dir", configDir}, test.args...)...)
			if err == nil || !strings.Contains(stdout+stderr, test.want) {
				t.Fatalf("\nwanted:\n%v to fail with %q\ngot:\n%s%s %v", test.args, test.want, stdout, stderr, err)
			}
		}
	})

	t.Run("should report none and create no token file without --control-address", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startNamedInstance(t, binary, configDir, "work", "--project-name", "work")
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		if err != nil || !strings.Contains(stdout, "control listener network address: none\n") {
			t.Fatalf("\nwanted:\ncontrol listener network address: none\ngot:\n%s%s%v", stdout, stderr, err)
		}
		if address := controlAddressOf(t, binary, configDir, "work"); address != nil {
			t.Fatalf("\nwanted:\ncontrol_listener null\ngot:\n%s", *address)
		}
		if _, err := os.Stat(filepath.Join(configDir, "instances", "work.token")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\nno control token file\ngot:\n%v", err)
		}
	})
}
