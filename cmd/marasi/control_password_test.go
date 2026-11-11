package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertNetworkStatus fails unless GET /service/status at address with
// authorization answers want.
func assertNetworkStatus(t *testing.T, address, authorization string, want int) {
	t.Helper()
	if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", authorization); status != want {
		t.Fatalf("\nwanted:\n%d for %q\ngot:\n%d %s", want, authorization, status, body)
	}
}

// assertPasswordSet fails unless service control status --json for name
// reports want as password_set and never contains testControlPassword.
func assertPasswordSet(t *testing.T, binary, configDir, name string, want bool) {
	t.Helper()
	stdout, _ := runServiceControl(t, binary, configDir, name, "status", "--json")
	var status struct {
		PasswordSet bool `json:"password_set"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || status.PasswordSet != want || strings.Contains(stdout, testControlPassword) {
		t.Fatalf("\nwanted:\npassword_set %v and no password\ngot:\n%s", want, stdout)
	}
}

// openNetworkConn opens one keep-alive connection to address, sends a
// status request on it with authorization, and returns the connection once
// the response is read.
func openNetworkConn(t *testing.T, address, authorization string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/service/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", authorization)
	if err := request.Write(conn); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Close {
		t.Fatalf("\nwanted:\n200 on a kept-alive connection\ngot:\n%d close %v", response.StatusCode, response.Close)
	}
	return conn, reader
}

// assertConnClosed fails unless the server closes conn within a few
// seconds. A drop can arrive as a reset rather than a clean EOF.
func assertConnClosed(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := reader.ReadByte()
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Fatalf("\nwanted:\nthe old-password connection closed\ngot:\nstill open %v", err)
	}
}

func TestControlPasswordChange(t *testing.T) {
	t.Parallel()

	binary := buildMarasi(t)

	t.Run("should replace the password from the socket with service control update --password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")

		stdout, _ := runServiceControl(t, binary, configDir, "work", "update", "--password", "new password", "--json")
		if want := `{"status":"active","control_listener":"` + address + `","password_set":true,"allowed_origins":[]}` + "\n"; stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		assertNetworkStatus(t, address, "Bearer "+testControlPassword, http.StatusUnauthorized)
		assertNetworkStatus(t, address, "Bearer new password", http.StatusOK)
	})

	t.Run("should rotate the password over --remote, authenticating with MARASI_PASSWORD", func(t *testing.T) {
		instance := startRemoteInstance(t, binary)
		address := strings.TrimPrefix(instance.url, "http://")

		stdout, stderr, err := instance.remote("service", "control", "update", "--password", "rotated")
		if err != nil || stdout != "" || stderr != "control listener network address updated to "+address+"\n" {
			t.Fatalf("\nwanted:\nthe update reported on stderr\ngot:\n%q %q %v", stdout, stderr, err)
		}
		assertNetworkStatus(t, address, "Bearer "+testControlPassword, http.StatusUnauthorized)
		assertNetworkStatus(t, address, "Bearer rotated", http.StatusOK)
		if _, stderr, err := instance.remote("service", "status"); exitCode(err) != 1 || !strings.Contains(stderr, "check --password or MARASI_PASSWORD") {
			t.Fatalf("\nwanted:\nthe old password refused over --remote\ngot:\n%s %v", stderr, err)
		}
		instance.password = "rotated"
		if _, stderr, err := instance.remote("service", "status"); err != nil {
			t.Fatalf("\nwanted:\nthe new password accepted over --remote\ngot:\n%s %v", stderr, err)
		}
	})

	t.Run("should drop connections and event streams that authenticated with the old password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		conn, reader := openNetworkConn(t, address, "Bearer "+testControlPassword)
		stream := openNetworkEvents(t, address, testControlPassword)
		defer stream.Close()

		runServiceControl(t, binary, configDir, "work", "update", "--password", "new password")
		assertConnClosed(t, conn, reader)
		waitStreamEnd(t, stream)
	})

	t.Run("should close the connection that changed the password after answering it", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		response, after := rawNetworkRequest(t, address, testControlPassword, "POST /service/control-listener/update", `{"password":"new password"}`)
		if want := `{"status":"active","control_listener":"` + address + `","password_set":true,"allowed_origins":[]}` + "\n"; response != want || after != nil {
			t.Fatalf("\nwanted:\n%s then a closed connection\ngot:\n%s %v", want, response, after)
		}
	})

	t.Run("should remove the password with --no-password from the socket and over --remote", func(t *testing.T) {
		instance := startRemoteInstance(t, binary)
		address := strings.TrimPrefix(instance.url, "http://")
		conn, reader := openNetworkConn(t, address, "Bearer "+testControlPassword)

		if _, stderr, err := instance.remote("service", "control", "update", "--no-password"); err != nil {
			t.Fatalf("removing the password over --remote: %v\n%s", err, stderr)
		}
		assertConnClosed(t, conn, reader)
		assertNetworkStatus(t, address, "", http.StatusOK)
		assertPasswordSet(t, binary, instance.configDir, "work", false)
		stdout, _ := runServiceControl(t, binary, instance.configDir, "work", "status")
		if !strings.Contains(stdout, "\npassword: none\n") {
			t.Fatalf("\nwanted:\npassword: none\ngot:\n%s", stdout)
		}

		runServiceControl(t, binary, instance.configDir, "work", "update", "--password", "again")
		assertNetworkStatus(t, address, "", http.StatusUnauthorized)
		runServiceControl(t, binary, instance.configDir, "work", "update", "--no-password")
		assertNetworkStatus(t, address, "", http.StatusOK)
	})

	t.Run("should take the password from MARASI_PASSWORD over the socket and keep it when none is given", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "work", "127.0.0.1:0", nil)
		address := *controlAddressOf(t, binary, configDir, "work")
		socket := []string{"--config-dir", configDir, "--instance", "work", "service", "control"}

		if _, stderr, err := runMarasiEnv(binary, []string{"MARASI_PASSWORD=from env"}, append(socket, "update")...); err != nil {
			t.Fatalf("updating with MARASI_PASSWORD: %v\n%s", err, stderr)
		}
		assertNetworkStatus(t, address, "Bearer from env", http.StatusOK)
		if _, stderr, err := runMarasiEnv(binary, []string{"MARASI_PASSWORD=from env"}, append(socket, "update", "--password", "from flag")...); err != nil {
			t.Fatalf("updating with --password over MARASI_PASSWORD: %v\n%s", err, stderr)
		}
		assertNetworkStatus(t, address, "Bearer from flag", http.StatusOK)

		if _, stderr, err := runMarasiEnv(binary, nil, append(socket, "update", "--address", "127.0.0.1", "--port", "0")...); err != nil {
			t.Fatalf("moving the address: %v\n%s", err, stderr)
		}
		moved := *controlAddressOf(t, binary, configDir, "work")
		assertNetworkStatus(t, moved, "Bearer from flag", http.StatusOK)

		runServiceControl(t, binary, configDir, "work", "stop")
		if _, stderr, err := runMarasiEnv(binary, []string{"MARASI_PASSWORD=from env"}, append(socket, "start", "--address", "127.0.0.1", "--port", "0", "--no-password")...); err != nil {
			t.Fatalf("starting with --no-password over MARASI_PASSWORD: %v\n%s", err, stderr)
		}
		assertNetworkStatus(t, *controlAddressOf(t, binary, configDir, "work"), "", http.StatusOK)
		runServiceControl(t, binary, configDir, "work", "stop")
		runServiceControl(t, binary, configDir, "work", "start", "--address", "127.0.0.1", "--port", "0", "--password", "on start")
		assertNetworkStatus(t, *controlAddressOf(t, binary, configDir, "work"), "Bearer on start", http.StatusOK)
	})

	t.Run("should refuse an update with nothing to change, both password flags, or an invalid password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstanceWith(t, binary, configDir, "work", "127.0.0.1:0", nil)
		socket := []string{"--config-dir", configDir, "--instance", "work", "service", "control"}
		for _, test := range []struct {
			env  []string
			args []string
			want string
		}{
			{args: []string{"update"}, want: "service control update requires --address and --port, --password, --no-password, or --allow-origin"},
			{args: []string{"update", "--port", "0"}, want: "service control update requires --address and --port together"},
			{args: []string{"update", "--password", "x", "--no-password"}, want: "[password no-password] are set none of the others can be"},
			{args: []string{"update", "--password", " padded"}, want: "invalid --password: a control password cannot start or end with whitespace"},
			{env: []string{"MARASI_PASSWORD=line\nbreak"}, args: []string{"update"}, want: "invalid MARASI_PASSWORD: a control password cannot start or end with whitespace"},
			{args: []string{"start", "--password", "x"}, want: "service control start requires --address and --port"},
		} {
			_, stderr, err := runMarasiEnv(binary, test.env, append(socket, test.args...)...)
			if exitCode(err) == 0 || !strings.Contains(stderr, test.want) {
				t.Fatalf("\nwanted:\nfailure with %q for %v\ngot:\n%s %v", test.want, test.args, stderr, err)
			}
		}
		socketPath := filepath.Join(configDir, "instances", "work.sock")
		for _, body := range []string{`{}`, `{"password":""}`, `{"password":"x","no_password":true}`, `{"no_password":false}`} {
			if code, response := doInstanceRequest(t, socketPath, http.MethodPost, "/service/control-listener/update", body); code != http.StatusBadRequest || !strings.Contains(response, `"invalid_listener_request"`) {
				t.Fatalf("\nwanted:\n400 invalid_listener_request for %s\ngot:\n%d %s", body, code, response)
			}
		}
		if code, response := doInstanceRequest(t, socketPath, http.MethodPost, "/service/control-listener/update", `{"password":" padded"}`); code != http.StatusBadRequest || !strings.Contains(response, `"error":"invalid_password"`) {
			t.Fatalf("\nwanted:\n400 invalid_password\ngot:\n%d %s", code, response)
		}
	})

	t.Run("should report a password change in control_listener.updated, and nothing for the same password", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		_, events, closeEvents := connectInstanceEvents(t, filepath.Join(configDir, "instances", "work.sock"))
		defer closeEvents()
		conn, reader := openNetworkConn(t, address, "Bearer "+testControlPassword)

		runServiceControl(t, binary, configDir, "work", "update", "--password", testControlPassword)
		runServiceControl(t, binary, configDir, "work", "update", "--no-password")
		_, updated := readEventNamed(t, events, "control_listener.updated")
		if want := `{"status":"active","control_listener":"` + address + `","password_set":false,"allowed_origins":[]}`; updated != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, updated)
		}
		assertConnClosed(t, conn, reader)
	})

	t.Run("should keep the current password across a restart after a runtime change", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		address := *controlAddressOf(t, binary, configDir, "work")
		runServiceControl(t, binary, configDir, "work", "update", "--password", testControlPassword+" rotated")

		if _, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "restart"); err != nil {
			t.Fatalf("restarting: %v\n%s", err, stderr)
		}
		assertNetworkStatus(t, address, "Bearer "+testControlPassword+" rotated", http.StatusOK)
		assertNetworkStatus(t, address, "Bearer "+testControlPassword, http.StatusUnauthorized)
		assertPasswordNotInArguments(t, configDir)

		runServiceControl(t, binary, configDir, "work", "update", "--no-password")
		if _, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "restart"); err != nil {
			t.Fatalf("restarting: %v\n%s", err, stderr)
		}
		assertNetworkStatus(t, address, "", http.StatusOK)
		assertPasswordSet(t, binary, configDir, "work", false)
	})
}
