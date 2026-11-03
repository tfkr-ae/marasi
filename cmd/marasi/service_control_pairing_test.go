package main

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// pairingPayload is the pairing payload as service control pair --json
// prints it. Password is a pointer so a test can tell an absent password
// from an empty one.
type pairingPayload struct {
	URL      string  `json:"url"`
	Password *string `json:"password"`
	Instance string  `json:"instance"`
}

// runPairJSON runs service control pair --json with args and env, and
// decodes the payload it prints.
func runPairJSON(t *testing.T, binary string, env []string, args ...string) pairingPayload {
	t.Helper()
	stdout, stderr, err := runMarasiEnv(binary, env, append(args, "service", "control", "pair", "--json")...)
	if err != nil {
		t.Fatalf("running service control pair: %v\n%s", err, stderr)
	}
	var payload pairingPayload
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decoding pairing payload %q: %v", stdout, err)
	}
	return payload
}

func TestServiceControlPair(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should print the URL, password, and instance of a network address on a specific IP", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")

		got := runPairJSON(t, binary, nil, "--config-dir", configDir, "--instance", "work")
		if got.URL != "http://"+address || got.Password == nil || *got.Password != testControlPassword || got.Instance != "work" {
			t.Fatalf("\nwanted:\nhttp://%s %q work\ngot:\n%+v", address, testControlPassword, got)
		}
	})
	t.Run("should omit the password when none is set", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "open", "127.0.0.1:0", nil)
		address := *controlAddressOf(t, binary, configDir, "open")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "open", "service", "control", "pair", "--json")
		want := `{"url":"http://` + address + `","instance":"open"}` + "\n"
		if err != nil || stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s%s%v", want, stdout, stderr, err)
		}
	})

	t.Run("should use a non-loopback IP of this machine for a wildcard address", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "0.0.0.0:0")
		_, port, _ := net.SplitHostPort(*controlAddressOf(t, binary, configDir, "work"))

		got := runPairJSON(t, binary, nil, "--config-dir", configDir, "--instance", "work")
		host, gotPort, err := net.SplitHostPort(strings.TrimPrefix(got.URL, "http://"))
		if err != nil || gotPort != port || !isLocalNonLoopbackIP(t, host) {
			t.Fatalf("\nwanted:\nhttp://<non-loopback IP of this machine>:%s\ngot:\n%s", port, got.URL)
		}
	})

	t.Run("should replace the host and keep the port with --host", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		_, port, _ := net.SplitHostPort(*controlAddressOf(t, binary, configDir, "work"))

		for _, test := range []struct{ host, want string }{
			{host: "100.64.0.7", want: "http://100.64.0.7:" + port},
			{host: "fd7a:115c::7", want: "http://[fd7a:115c::7]:" + port},
			{host: "laptop.tail1234.ts.net", want: "http://laptop.tail1234.ts.net:" + port},
		} {
			got := runPairJSON(t, binary, nil, "--config-dir", configDir, "--instance", "work", "--host", test.host)
			if got.URL != test.want || got.Password == nil || *got.Password != testControlPassword {
				t.Fatalf("\nwanted:\n%s with the password\ngot:\n%+v", test.want, got)
			}
		}
	})

	t.Run("should refuse --host with a hostname on an instance without a password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "open", "127.0.0.1:0", nil)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "open", "service", "control", "pair", "--host", "laptop.local")
		if err == nil || stdout != "" || !strings.Contains(stderr, "has no control password") || !strings.Contains(stderr, "pass an IP address to --host or set a control password") {
			t.Fatalf("\nwanted:\nexit 1 naming the missing password\ngot:\n%s%s%v", stdout, stderr, err)
		}
		if got := runPairJSON(t, binary, nil, "--config-dir", configDir, "--instance", "open", "--host", "192.168.1.20"); !strings.HasPrefix(got.URL, "http://192.168.1.20:") {
			t.Fatalf("\nwanted:\nan IP --host accepted\ngot:\n%+v", got)
		}
	})

	t.Run("should build the payload from --remote and the password the CLI holds", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		remote := "http://" + *controlAddressOf(t, binary, configDir, "work")
		remoteArgs := []string{"--config-dir", t.TempDir(), "--remote", remote}

		got := runPairJSON(t, binary, []string{"MARASI_PASSWORD=" + testControlPassword}, remoteArgs...)
		if got.URL != remote || got.Password == nil || *got.Password != testControlPassword || got.Instance != "work" {
			t.Fatalf("\nwanted:\n%s %q work\ngot:\n%+v", remote, testControlPassword, got)
		}
		got = runPairJSON(t, binary, nil, append(remoteArgs, "--password", testControlPassword, "--host", "100.64.0.7")...)
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(remote, "http://"))
		if got.URL != "http://100.64.0.7:"+port || got.Password == nil || *got.Password != testControlPassword {
			t.Fatalf("\nwanted:\nhttp://100.64.0.7:%s with the password\ngot:\n%+v", port, got)
		}

		openDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, openDir, "open", "127.0.0.1:0", nil)
		openRemote := "http://" + *controlAddressOf(t, binary, openDir, "open")
		stdout, stderr, err := runMarasi(binary, "--config-dir", t.TempDir(), "--remote", openRemote, "service", "control", "pair", "--json")
		want := `{"url":"` + openRemote + `","instance":"open"}` + "\n"
		if err != nil || stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s%s%v", want, stdout, stderr, err)
		}
		_, stderr, err = runMarasi(binary, "--config-dir", t.TempDir(), "--remote", openRemote, "service", "control", "pair", "--host", "laptop.local")
		if err == nil || !strings.Contains(stderr, "has no control password") {
			t.Fatalf("\nwanted:\nexit 1 naming the missing password\ngot:\n%s%v", stderr, err)
		}
	})

	t.Run("should refuse the pairing route on the network address", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")

		status, header, body := networkRequest(t, http.MethodGet, address, "/service/control-listener/pairing", "Bearer "+testControlPassword)
		want := `{"error":"socket_only","message":"pairing is available only through the socket"}` + "\n"
		if status != http.StatusForbidden || header.Get("Content-Type") != "application/json" || body != want {
			t.Fatalf("\nwanted:\n403 %s\ngot:\n%d %s", want, status, body)
		}
	})

	t.Run("should fail with a hint to start one when no network address is running", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startNamedInstance(t, binary, configDir, "work", "--project-name", "work")
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

		status, body := doInstanceRequest(t, filepath.Join(configDir, "instances", "work.sock"), http.MethodGet, "/service/control-listener/pairing", "")
		wantBody := `{"error":"listener_inactive","message":"the control listener has no network address running"}` + "\n"
		if status != http.StatusConflict || body != wantBody {
			t.Fatalf("\nwanted:\n409 %s\ngot:\n%d %s", wantBody, status, body)
		}
		for _, args := range [][]string{nil, {"--json"}} {
			stdout, stderr, err := runMarasi(binary, append([]string{"--config-dir", configDir, "--instance", "work", "service", "control", "pair"}, args...)...)
			// --json reports the error as JSON on stdout, and human mode on stderr.
			if err == nil || !strings.Contains(stdout+stderr, "start one with marasi service control start --address HOST --port PORT") {
				t.Fatalf("\nwanted:\nexit 1 with a hint to start one\ngot:\n%s%s%v", stdout, stderr, err)
			}
		}
	})

	t.Run("should fail with a hint when the instance is not running", func(t *testing.T) {
		stdout, stderr, err := runMarasi(binary, "--config-dir", serviceConfigDir(t), "--instance", "absent", "service", "control", "pair")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance absent is not running; start one with marasi service start --instance absent") {
			t.Fatalf("\nwanted:\nexit 1 naming the instance\ngot:\n%s%s%v", stdout, stderr, err)
		}
	})

	t.Run("should print a QR code followed by the URL", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		url := "http://" + *controlAddressOf(t, binary, configDir, "work")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "control", "pair")
		qrBlock, urlLine, found := strings.Cut(stdout, "\n\n")
		if err != nil || !found || urlLine != url+"\n" || !strings.Contains(qrBlock, "█") || !strings.Contains(qrBlock, "▀") || strings.Contains(stdout, testControlPassword) {
			t.Fatalf("\nwanted:\na QR block, a blank line, and %s\ngot:\n%s%s%v", url, stdout, stderr, err)
		}
	})
}

// isLocalNonLoopbackIP reports whether host is an IP address on one of this
// machine's interfaces, other than loopback.
func isLocalNonLoopbackIP(t *testing.T, host string) bool {
	t.Helper()
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("listing interface addresses: %v", err)
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
			return true
		}
	}
	return false
}
