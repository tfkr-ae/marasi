package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// startControlAddressInstance starts name with its control listener also
// reachable at controlAddress, and returns the start command's stderr.
func startControlAddressInstance(t *testing.T, binary, configDir, name, controlAddress string) string {
	t.Helper()
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "stop") })
	_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "start", "--project-name", name, "--port", "0", "--control-address", controlAddress)
	if err != nil {
		t.Fatalf("starting %s: %v\n%s", name, err, stderr)
	}
	return stderr
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

// controlTokenOf reads the control token through the socket with
// service control token.
func controlTokenOf(t *testing.T, binary, configDir, name string) string {
	t.Helper()
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "control", "token")
	if err != nil {
		t.Fatalf("reading control token of %s: %v\n%s", name, err, stderr)
	}
	return strings.TrimSuffix(stdout, "\n")
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

	t.Run("should serve the control API at the network address only with the control token", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")

		address := controlAddressOf(t, binary, configDir, "work")
		if address == nil || !strings.HasPrefix(*address, "127.0.0.1:") || strings.HasSuffix(*address, ":0") {
			t.Fatalf("\nwanted:\ncontrol_listener 127.0.0.1 with its bound port\ngot:\n%v", address)
		}

		status, header, body := networkRequest(t, http.MethodGet, *address, "/service/status", "")
		wantBody := "{\"error\":\"unauthorized\",\"message\":\"send the control token as Authorization: Bearer\"}\n"
		if status != http.StatusUnauthorized || header.Get("Content-Type") != "application/json" || body != wantBody {
			t.Fatalf("\nwanted:\n401 application/json %s\ngot:\n%d %s %s", wantBody, status, header.Get("Content-Type"), body)
		}

		token := controlTokenOf(t, binary, configDir, "work")
		status, _, body = networkRequest(t, http.MethodGet, *address, "/service/status", "Bearer "+token)
		_, socketBody := doInstanceRequest(t, filepath.Join(configDir, "instances", "work.sock"), http.MethodGet, "/service/status", "")
		if status != http.StatusOK || body != socketBody {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", socketBody, status, body)
		}
	})

	t.Run("should refuse a wrong or misplaced token and the token route on the network address", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		token := controlTokenOf(t, binary, configDir, "work")

		const unauthorized = "{\"error\":\"unauthorized\",\"message\":\"send the control token as Authorization: Bearer\"}\n"
		for _, test := range []struct {
			name          string
			path          string
			authorization string
		}{
			{name: "a wrong token", path: "/service/status", authorization: "Bearer " + strings.Repeat("0", len(token))},
			{name: "a token of another length", path: "/service/status", authorization: "Bearer " + token + "0"},
			{name: "another scheme", path: "/service/status", authorization: "Basic " + token},
			{name: "a query-string token", path: "/service/status?token=" + token + "&access_token=" + token},
		} {
			status, header, body := networkRequest(t, http.MethodGet, address, test.path, test.authorization)
			if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") != "Bearer" || body != unauthorized {
				t.Fatalf("\nwanted:\n401 for %s\ngot:\n%d %q %s", test.name, status, header.Get("WWW-Authenticate"), body)
			}
		}

		status, _, body := networkRequest(t, http.MethodGet, address, "/service/control-listener/token", "bearer "+token)
		wantForbidden := "{\"error\":\"socket_only\",\"message\":\"the control token is available only through the socket\"}\n"
		if status != http.StatusForbidden || body != wantForbidden {
			t.Fatalf("\nwanted:\n403 %s\ngot:\n%d %s", wantForbidden, status, body)
		}

		status, body = doInstanceRequest(t, filepath.Join(configDir, "instances", "work.sock"), http.MethodGet, "/service/control-listener/token", "")
		if want := "{\"token\":\"" + token + "\"}\n"; status != http.StatusOK || body != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, status, body)
		}
		if len(token) != 64 {
			t.Fatalf("\nwanted:\n32 random bytes as 64 hex characters\ngot:\n%q", token)
		}
	})

	t.Run("should keep the token in an owner-only file and reuse it on the next start", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		token := controlTokenOf(t, binary, configDir, "work")
		tokenPath := filepath.Join(configDir, "instances", "work.token")
		content, err := os.ReadFile(tokenPath)
		if err != nil || string(content) != token+"\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q %v", token, content, err)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(tokenPath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("\nwanted:\n-rw-------\ngot:\n%v %v", info.Mode(), err)
			}
		}

		if _, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop"); err != nil {
			t.Fatalf("stopping work: %v\n%s", err, stderr)
		}
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", "Bearer "+token); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 with the first start's token\ngot:\n%d %s", status, body)
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
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "json", "service", "start", "--project-name", "json", "--port", "0", "--control-address", "127.0.0.1:0", "--json")
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

	t.Run("should show the network address in service status and service list, and never the token", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := controlAddressOf(t, binary, configDir, "work")
		token := controlTokenOf(t, binary, configDir, "work")
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
				t.Fatalf("\nwanted:\n%v without the control token\ngot:\n%s", args, stdout)
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
			t.Fatalf("\nwanted:\ninstance log without the control token\ngot:\n%s", log)
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

// openNetworkConn dials the network address, writes request when it is not
// empty, and reads until the bytes received contain until.
func openNetworkConn(t *testing.T, address, request, until string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dialing the network address: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if request == "" {
		return conn
	}
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("writing to the network address: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var received []byte
	buffer := make([]byte, 4096)
	for !strings.Contains(string(received), until) {
		n, err := conn.Read(buffer)
		received = append(received, buffer[:n]...)
		if err != nil {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q %v", until, received, err)
		}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	return conn
}

// readUntilClosed discards what conn sends until the peer closes it, and
// fails if that takes longer than five seconds.
func readUntilClosed(conn net.Conn) error {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	buffer := make([]byte, 4096)
	for {
		_, err := conn.Read(buffer)
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return err
		}
		if err != nil {
			return nil
		}
	}
}

// readResponseHead returns the status line of the next response on conn.
func readResponseHead(t *testing.T, conn net.Conn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("reading a response on the network address: %v", err)
	}
	return line
}

func TestControlTokenRotate(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should replace the token so the old one gets 401 and the new one works", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		oldToken := controlTokenOf(t, binary, configDir, "work")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "control", "token", "--rotate")
		if err != nil {
			t.Fatalf("rotating the control token: %v\n%s", err, stderr)
		}
		newToken := strings.TrimSuffix(stdout, "\n")
		if len(newToken) != 64 || newToken == oldToken || stdout != newToken+"\n" || stderr != "" {
			t.Fatalf("\nwanted:\na new 64 character token on stdout\ngot:\n%q %q", stdout, stderr)
		}
		if read := controlTokenOf(t, binary, configDir, "work"); read != newToken {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", newToken, read)
		}
		content, err := os.ReadFile(filepath.Join(configDir, "instances", "work.token"))
		if err != nil || string(content) != newToken+"\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q %v", newToken, content, err)
		}
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", "Bearer "+oldToken); status != http.StatusUnauthorized {
			t.Fatalf("\nwanted:\n401 with the old token\ngot:\n%d %s", status, body)
		}
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", "Bearer "+newToken); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 with the new token\ngot:\n%d %s", status, body)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "control", "token", "--rotate", "--json")
		var rotated struct {
			Token string `json:"token"`
		}
		if err != nil || json.Unmarshal([]byte(stdout), &rotated) != nil || len(rotated.Token) != 64 || rotated.Token == newToken {
			t.Fatalf("\nwanted:\n{\"token\":...} with a new token\ngot:\n%q %q %v", stdout, stderr, err)
		}
	})

	t.Run("should drop connections and event streams that used the old token and publish token_rotated without it", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		oldToken := controlTokenOf(t, binary, configDir, "work")
		_, socketEvents, closeSocketEvents := connectInstanceEvents(t, filepath.Join(configDir, "instances", "work.sock"))
		defer closeSocketEvents()

		stream := openNetworkConn(t, address, "GET /events HTTP/1.1\r\nHost: "+address+"\r\nAuthorization: Bearer "+oldToken+"\r\n\r\n", ": connected")
		keepAlive := openNetworkConn(t, address, "GET /service/status HTTP/1.1\r\nHost: "+address+"\r\nAuthorization: Bearer "+oldToken+"\r\n\r\n", "\"status\":\"running\"")
		idle := openNetworkConn(t, address, "", "")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "control", "token", "--rotate")
		if err != nil {
			t.Fatalf("rotating the control token: %v\n%s", err, stderr)
		}
		newToken := strings.TrimSuffix(stdout, "\n")

		_, data := readEventNamed(t, socketEvents, "control_listener.token_rotated")
		if data != "{}" || strings.Contains(data, newToken) {
			t.Fatalf("\nwanted:\ncontrol_listener.token_rotated with {}\ngot:\n%s", data)
		}
		for name, conn := range map[string]net.Conn{"the event stream": stream, "the keep-alive connection": keepAlive} {
			if err := readUntilClosed(conn); err != nil {
				t.Fatalf("\nwanted:\n%s closed after rotation\ngot:\n%v", name, err)
			}
		}
		if _, err := idle.Write([]byte("GET /service/status HTTP/1.1\r\nHost: " + address + "\r\nAuthorization: Bearer " + newToken + "\r\n\r\n")); err != nil {
			t.Fatalf("\nwanted:\na connection that never authenticated kept open\ngot:\n%v", err)
		}
		if got := readResponseHead(t, idle); !strings.HasPrefix(got, "HTTP/1.1 200 ") {
			t.Fatalf("\nwanted:\n200 with the new token on the unauthenticated connection\ngot:\n%s", got)
		}
	})

	t.Run("should refuse rotation on the network address and keep the token", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		token := controlTokenOf(t, binary, configDir, "work")

		status, _, body := networkRequest(t, http.MethodPost, address, "/service/control-listener/token/rotate", "Bearer "+token)
		wantForbidden := "{\"error\":\"socket_only\",\"message\":\"the control token can be rotated only through the socket\"}\n"
		if status != http.StatusForbidden || body != wantForbidden {
			t.Fatalf("\nwanted:\n403 %s\ngot:\n%d %s", wantForbidden, status, body)
		}
		if read := controlTokenOf(t, binary, configDir, "work"); read != token {
			t.Fatalf("\nwanted:\nthe token unchanged %s\ngot:\n%s", token, read)
		}
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", "Bearer "+token); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 with the unchanged token\ngot:\n%d %s", status, body)
		}
	})
}
