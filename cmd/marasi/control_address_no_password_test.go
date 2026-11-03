package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// allowedOriginsOf reads the allowed origins from service control status
// --json.
func allowedOriginsOf(t *testing.T, binary, configDir, name string) []string {
	t.Helper()
	stdout, _ := runServiceControl(t, binary, configDir, name, "status", "--json")
	var status struct {
		AllowedOrigins []string `json:"allowed_origins"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || status.AllowedOrigins == nil {
		t.Fatalf("\nwanted:\nallowed_origins in the status\ngot:\n%s %v", stdout, err)
	}
	return status.AllowedOrigins
}

// noPasswordWarning is the warning line for a network address at address
// that has no control password.
func noPasswordWarning(address string, loopback bool) string {
	if loopback {
		return "warning: control listener network address " + address + " has no control password; any local process can control the instance\n"
	}
	return "warning: control listener network address " + address + " has no control password; any host that can reach it can control the instance\n"
}

func TestControlAddressWithoutPassword(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should refuse an origin that is not allowed and answer an allowed one and a request with no origin", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "open", "127.0.0.1:0", nil, "--allow-origin", "http://localhost:5173")
		address := *controlAddressOf(t, binary, configDir, "open")

		const evil = "https://evil.example"
		refused := `{"error":"origin_not_allowed","message":"origin https://evil.example is not allowed without a control password: set a control password, or allow the origin with --allow-origin"}` + "\n"
		response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: address, headers: []string{"Origin: " + evil}})
		if response.StatusCode != http.StatusForbidden || body != refused {
			t.Fatalf("\nwanted:\n403 %s\ngot:\n%d %s", refused, response.StatusCode, body)
		}
		response, body = sendRaw(t, "tcp", address, rawRequest{method: http.MethodPost, path: "/service/stop", host: address, headers: []string{"Origin: " + evil, "Content-Length: 0"}})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("\nwanted:\n403 for a POST from %s\ngot:\n%d %s", evil, response.StatusCode, body)
		}

		response, body = sendRaw(t, "tcp", address, rawRequest{method: http.MethodOptions, path: "/service/status", host: address, headers: []string{"Origin: " + evil, "Access-Control-Request-Method: POST"}})
		if want := "Vary: Origin"; response.StatusCode != http.StatusForbidden || corsHeaders(response) != want {
			t.Fatalf("\nwanted:\n403 preflight with %s\ngot:\n%d\n%s\n%s", want, response.StatusCode, corsHeaders(response), body)
		}

		response, _ = sendRaw(t, "tcp", address, rawRequest{method: http.MethodOptions, path: "/service/status", host: address, headers: []string{"Origin: http://localhost:5173", "Access-Control-Request-Method: POST"}})
		want := "Access-Control-Allow-Origin: http://localhost:5173\nAccess-Control-Allow-Methods: GET, POST, PUT, DELETE\nAccess-Control-Allow-Headers: Authorization, Content-Type\nVary: Origin"
		if response.StatusCode != http.StatusNoContent || corsHeaders(response) != want {
			t.Fatalf("\nwanted:\n204\n%s\ngot:\n%d\n%s", want, response.StatusCode, corsHeaders(response))
		}
		response, body = sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: address, headers: []string{"Origin: http://localhost:5173"}})
		if want := "Access-Control-Allow-Origin: http://localhost:5173\nVary: Origin"; response.StatusCode != http.StatusOK || corsHeaders(response) != want {
			t.Fatalf("\nwanted:\n200\n%s\ngot:\n%d\n%s\n%s", want, response.StatusCode, corsHeaders(response), body)
		}
		if response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: address}); response.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\n200 with no Origin\ngot:\n%d %s", response.StatusCode, body)
		}
	})

	t.Run("should take allowed origins from MARASI_ALLOW_ORIGIN and --allow-origin, replace them on update, and keep them across a restart", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "env", "127.0.0.1:0", []string{"MARASI_ALLOW_ORIGIN=http://localhost:5173,app://marasi"})
		if got, want := allowedOriginsOf(t, binary, configDir, "env"), []string{"http://localhost:5173", "app://marasi"}; !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
		startControlAddressInstanceWith(t, binary, configDir, "flag", "127.0.0.1:0", []string{"MARASI_ALLOW_ORIGIN=http://ignored.example"}, "--allow-origin", "http://localhost:5173", "--allow-origin", "tauri://localhost")
		if got, want := allowedOriginsOf(t, binary, configDir, "flag"), []string{"http://localhost:5173", "tauri://localhost"}; !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}

		runServiceControl(t, binary, configDir, "flag", "update", "--allow-origin", "https://gui.example")
		if got, want := allowedOriginsOf(t, binary, configDir, "flag"), []string{"https://gui.example"}; !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
		runServiceControl(t, binary, configDir, "flag", "stop")
		runServiceControl(t, binary, configDir, "flag", "start", "--address", "127.0.0.1", "--port", "0", "--allow-origin", "http://localhost:1420")
		if got, want := allowedOriginsOf(t, binary, configDir, "flag"), []string{"http://localhost:1420"}; !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
		stdout, _ := runServiceControl(t, binary, configDir, "flag", "status")
		if !strings.Contains(stdout, "allowed origins: http://localhost:1420\n") {
			t.Fatalf("\nwanted:\nallowed origins: http://localhost:1420\ngot:\n%s", stdout)
		}

		socketPath := filepath.Join(configDir, "instances", "flag.sock")
		if status, body := doInstanceRequest(t, socketPath, http.MethodPost, "/service/restart", ""); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 restarted\ngot:\n%d %s", status, body)
		}
		if got, want := allowedOriginsOf(t, binary, configDir, "flag"), []string{"http://localhost:1420"}; !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\n%v after a restart\ngot:\n%v", want, got)
		}
		runServiceControl(t, binary, configDir, "flag", "update", "--allow-origin", "")
		if got := allowedOriginsOf(t, binary, configDir, "flag"); len(got) != 0 {
			t.Fatalf("\nwanted:\nno allowed origins\ngot:\n%v", got)
		}

		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "flag", "service", "control", "update", "--allow-origin", "http://localhost:5173/app")
		if want := "an allowed origin is null or SCHEME://HOST[:PORT], such as http://localhost:5173"; err == nil || !strings.Contains(stderr, want) {
			t.Fatalf("\nwanted:\nexit 1 with %q\ngot:\n%s %v", want, stderr, err)
		}
	})

	t.Run("should accept the origin null with a warning", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stderr := startControlAddressInstanceWith(t, binary, configDir, "open", "127.0.0.1:0", nil, "--allow-origin", "null")
		const warning = "warning: allowing the origin null lets a sandboxed iframe or a local file on any site call the control listener network address without a password\n"
		log, err := os.ReadFile(filepath.Join(configDir, "instances", "open.log"))
		if err != nil || !strings.Contains(stderr, warning) || strings.Count(string(log), warning) != 1 {
			t.Fatalf("\nwanted:\n%s on stderr and once in the log\ngot:\n%s\n%s %v", warning, stderr, log, err)
		}
		address := *controlAddressOf(t, binary, configDir, "open")
		if response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: address, headers: []string{"Origin: null"}}); response.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\n200 for Origin null\ngot:\n%d %s", response.StatusCode, body)
		}
		_, stderr = runServiceControl(t, binary, configDir, "open", "update", "--allow-origin", "null,http://localhost:5173")
		if !strings.Contains(stderr, warning) {
			t.Fatalf("\nwanted:\n%s on stderr of update\ngot:\n%s", warning, stderr)
		}
	})

	t.Run("should accept only the bound IP and localhost as Host on loopback", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "open", "127.0.0.1:0", nil)
		address := *controlAddressOf(t, binary, configDir, "open")
		_, port, _ := net.SplitHostPort(address)
		for _, host := range []string{address, "127.0.0.1", "127.0.0.1:9999", "localhost:" + port, "LOCALHOST"} {
			if response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: host}); response.StatusCode != http.StatusOK {
				t.Fatalf("\nwanted:\n200 for Host %q\ngot:\n%d %s", host, response.StatusCode, body)
			}
		}
		const mismatch = `{"error":"host_mismatch","message":"the Host header does not name the control listener network address: use its IP address, or set a control password"}` + "\n"
		for _, host := range []string{"127.0.0.2:" + port, "[::1]:" + port, ""} {
			if response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: host}); response.StatusCode != http.StatusMisdirectedRequest || body != mismatch {
				t.Fatalf("\nwanted:\n421 for Host %q\n%s\ngot:\n%d %s", host, mismatch, response.StatusCode, body)
			}
		}
		assertHostnameRefused(t, address, port)
	})

	t.Run("should accept any IP and localhost as Host on a wildcard address", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "wild", "0.0.0.0:0", nil, "--no-password")
		_, port, _ := net.SplitHostPort(*controlAddressOf(t, binary, configDir, "wild"))
		dial := net.JoinHostPort("127.0.0.1", port)
		for _, host := range []string{"0.0.0.0:" + port, "192.0.2.7:" + port, "10.1.2.3:9000", "[::1]:" + port, "localhost"} {
			if response, body := sendRaw(t, "tcp", dial, rawRequest{method: http.MethodGet, path: "/service/status", host: host}); response.StatusCode != http.StatusOK {
				t.Fatalf("\nwanted:\n200 for Host %q\ngot:\n%d %s", host, response.StatusCode, body)
			}
		}
		assertHostnameRefused(t, dial, port)

		socketPath := filepath.Join(configDir, "instances", "wild.sock")
		if status, body := doInstanceRequest(t, socketPath, http.MethodPost, "/service/restart", ""); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 restarted with no password beyond loopback\ngot:\n%d %s", status, body)
		}
		if response, body := sendRaw(t, "tcp", dial, rawRequest{method: http.MethodGet, path: "/service/status", host: "192.0.2.7"}); response.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\n200 from the successor\ngot:\n%d %s", response.StatusCode, body)
		}
	})

	t.Run("should refuse a network address beyond loopback without --no-password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		const refusal = "is beyond loopback: set a control password with --password, or pass --no-password to run without one"
		for _, test := range []struct {
			env  []string
			args []string
		}{
			{env: []string{"MARASI_CONTROL_ADDRESS=0.0.0.0:0"}},
			{args: []string{"--control-address", "0.0.0.0:0"}},
		} {
			_, stderr, err := runMarasiEnv(binary, test.env, append([]string{"--config-dir", configDir, "--instance", "work", "service", "start", "--port", "0"}, test.args...)...)
			if exitCode(err) != 1 || !strings.Contains(stderr, "control listener network address 0.0.0.0:0 "+refusal) {
				t.Fatalf("\nwanted:\nexit 1 with %q\ngot:\n%s %v", refusal, stderr, err)
			}
		}

		startControlAddressInstanceWith(t, binary, configDir, "open", "127.0.0.1:0", nil)
		for _, args := range [][]string{
			{"update", "--address", "0.0.0.0", "--port", "0"},
		} {
			_, stderr, err := runMarasi(binary, append([]string{"--config-dir", configDir, "--instance", "open", "service", "control"}, args...)...)
			if err == nil || !strings.Contains(stderr, refusal) {
				t.Fatalf("\nwanted:\n%v refused with %q\ngot:\n%s %v", args, refusal, stderr, err)
			}
		}
		runServiceControl(t, binary, configDir, "open", "stop")
		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "open", "service", "control", "start", "--address", "0.0.0.0", "--port", "0")
		if err == nil || !strings.Contains(stderr, refusal) {
			t.Fatalf("\nwanted:\nstart refused with %q\ngot:\n%s %v", refusal, stderr, err)
		}
		runServiceControl(t, binary, configDir, "open", "start", "--address", "0.0.0.0", "--port", "0", "--no-password")
		address := *controlAddressOf(t, binary, configDir, "open")
		if !strings.HasPrefix(address, "0.0.0.0:") {
			t.Fatalf("\nwanted:\n0.0.0.0 with --no-password\ngot:\n%s", address)
		}
		runServiceControl(t, binary, configDir, "open", "update", "--password", testControlPassword)
		runServiceControl(t, binary, configDir, "open", "update", "--address", "0.0.0.0", "--port", "0")
	})

	t.Run("should warn once on stderr and in the log without a password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stderr := startControlAddressInstanceWith(t, binary, configDir, "local", "127.0.0.1:0", nil)
		address := *controlAddressOf(t, binary, configDir, "local")
		warning := noPasswordWarning(address, true)
		log, err := os.ReadFile(filepath.Join(configDir, "instances", "local.log"))
		if err != nil || strings.Count(stderr, warning) != 1 || strings.Count(string(log), warning) != 1 {
			t.Fatalf("\nwanted:\n%s once on stderr and in the log\ngot:\n%s\n%s %v", warning, stderr, log, err)
		}

		_, stderr = runServiceControl(t, binary, configDir, "local", "update", "--address", "0.0.0.0", "--port", "0", "--no-password")
		address = *controlAddressOf(t, binary, configDir, "local")
		warning = noPasswordWarning(address, false)
		log, err = os.ReadFile(filepath.Join(configDir, "instances", "local.log"))
		if err != nil || strings.Count(stderr, warning) != 1 || strings.Count(string(log), warning) != 1 || !strings.Contains(string(log), "serves plain HTTP without TLS") {
			t.Fatalf("\nwanted:\n%s once on stderr and in the log, beside the plain-HTTP warning\ngot:\n%s\n%s %v", warning, stderr, log, err)
		}

		guarded := startControlAddressInstance(t, binary, configDir, "guarded", "127.0.0.1:0")
		if strings.Contains(guarded, "no control password") {
			t.Fatalf("\nwanted:\nno warning with a password\ngot:\n%s", guarded)
		}
	})
}

// assertHostnameRefused fails unless a hostname Host is refused at address,
// whose port is port, with the error that names the fix.
func assertHostnameRefused(t *testing.T, address, port string) {
	t.Helper()
	const refused = `{"error":"host_mismatch","message":"the Host header names a hostname, which a web page can rebind to this machine: use the IP address, or set a control password"}` + "\n"
	for _, host := range []string{"evil.example:" + port, "evil.example", "marasi", "localhost.evil.example:" + port} {
		if response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: host}); response.StatusCode != http.StatusMisdirectedRequest || body != refused {
			t.Fatalf("\nwanted:\n421 for Host %q\n%s\ngot:\n%d %s", host, refused, response.StatusCode, body)
		}
	}
}
