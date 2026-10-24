package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// controlListenerStatus is the body of the control listener routes.
type controlListenerStatus struct {
	Status          string  `json:"status"`
	ControlListener *string `json:"control_listener"`
}

// runServiceControl runs service control with args against name and returns
// its stdout and stderr, failing the test when it exits non-zero.
func runServiceControl(t *testing.T, binary, configDir, name string, args ...string) (string, string) {
	t.Helper()
	stdout, stderr, err := runMarasi(binary, append([]string{"--config-dir", configDir, "--instance", name, "service", "control"}, args...)...)
	if err != nil {
		t.Fatalf("service control %v: %v\n%s", args, err, stderr)
	}
	return stdout, stderr
}

func TestServiceControlListener(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should start the network address on a running instance and report the bound port", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startNamedInstance(t, binary, configDir, "work", "--project-name", "work")
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
		_, events, closeEvents := connectInstanceEvents(t, filepath.Join(configDir, "instances", "work.sock"))
		defer closeEvents()

		stdout, stderr := runServiceControl(t, binary, configDir, "work", "start", "--address", "127.0.0.1", "--port", "0", "--json")
		var started controlListenerStatus
		if err := json.Unmarshal([]byte(stdout), &started); err != nil || started.Status != "active" || started.ControlListener == nil ||
			!strings.HasPrefix(*started.ControlListener, "127.0.0.1:") || strings.HasSuffix(*started.ControlListener, ":0") || stderr != "" {
			t.Fatalf("\nwanted:\nactive on 127.0.0.1 with its bound port\ngot:\n%s%s", stdout, stderr)
		}
		address := *started.ControlListener

		_, data := readEventNamed(t, events, "control_listener.started")
		if want := `{"status":"active","control_listener":"` + address + `"}`; data != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, data)
		}

		if got := controlAddressOf(t, binary, configDir, "work"); got == nil || *got != address {
			t.Fatalf("\nwanted:\n%s in service status\ngot:\n%v", address, got)
		}
		stdout, _ = runServiceControl(t, binary, configDir, "work", "status")
		if want := "status: active\ncontrol listener network address: " + address + "\n"; stdout != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, stdout)
		}
		token := controlTokenOf(t, binary, configDir, "work")
		if status, _, body := networkRequest(t, http.MethodGet, address, "/service/status", "Bearer "+token); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 on the started address\ngot:\n%d %s", status, body)
		}

		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "control", "start", "--address", "127.0.0.1", "--port", "0")
		if err == nil || !strings.Contains(stderr, "listener_already_active") {
			t.Fatalf("\nwanted:\nlistener_already_active\ngot:\n%s %v", stderr, err)
		}
	})

	t.Run("should move and stop the network address, keep the token, and remember no endpoint", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		first := *controlAddressOf(t, binary, configDir, "work")
		token := controlTokenOf(t, binary, configDir, "work")
		_, events, closeEvents := connectInstanceEvents(t, filepath.Join(configDir, "instances", "work.sock"))
		defer closeEvents()

		_, stderr := runServiceControl(t, binary, configDir, "work", "update", "--address", "localhost", "--port", "0")
		moved := controlAddressOf(t, binary, configDir, "work")
		if moved == nil || *moved == first || stderr != "control listener network address updated to "+*moved+"\n" {
			t.Fatalf("\nwanted:\na new address on stderr\ngot:\n%s %v", stderr, moved)
		}
		if _, data := readEventNamed(t, events, "control_listener.updated"); data != `{"status":"active","control_listener":"`+*moved+`"}` {
			t.Fatalf("\nwanted:\ncontrol_listener.updated with %s\ngot:\n%s", *moved, data)
		}
		if _, err := net.Dial("tcp", first); err == nil {
			t.Fatalf("\nwanted:\nthe old address %s closed\ngot:\na connection", first)
		}
		if status, _, body := networkRequest(t, http.MethodGet, *moved, "/service/status", "Bearer "+token); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 with the same token on the moved address\ngot:\n%d %s", status, body)
		}

		_, stderr = runServiceControl(t, binary, configDir, "work", "stop")
		if stderr != "control listener network address stopped successfully\n" {
			t.Fatalf("\nwanted:\nstopped successfully\ngot:\n%q", stderr)
		}
		if _, data := readEventNamed(t, events, "control_listener.stopped"); data != `{"status":"inactive","control_listener":null}` {
			t.Fatalf("\nwanted:\ncontrol_listener.stopped inactive\ngot:\n%s", data)
		}
		stdout, _ := runServiceControl(t, binary, configDir, "work", "status", "--json")
		if want := "{\"status\":\"inactive\",\"control_listener\":null}\n"; stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if _, err := net.Dial("tcp", *moved); err == nil {
			t.Fatalf("\nwanted:\nthe stopped address %s closed\ngot:\na connection", *moved)
		}
		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "control", "start")
		if err == nil || !strings.Contains(stderr, "service control start requires --address and --port") {
			t.Fatalf("\nwanted:\nstart to require the address\ngot:\n%s %v", stderr, err)
		}
		for _, args := range [][]string{{"stop"}, {"update", "--address", "127.0.0.1", "--port", "0"}} {
			_, stderr, err := runMarasi(binary, append([]string{"--config-dir", configDir, "--instance", "work", "service", "control"}, args...)...)
			if err == nil || !strings.Contains(stderr, "listener_inactive") {
				t.Fatalf("\nwanted:\n%v to fail with listener_inactive\ngot:\n%s %v", args, stderr, err)
			}
		}

		stdout, _ = runServiceControl(t, binary, configDir, "work", "start", "--address", "127.0.0.1", "--port", "0", "--json")
		var restarted controlListenerStatus
		if err := json.Unmarshal([]byte(stdout), &restarted); err != nil || restarted.ControlListener == nil {
			t.Fatalf("\nwanted:\nan active address\ngot:\n%s", stdout)
		}
		if status, _, body := networkRequest(t, http.MethodGet, *restarted.ControlListener, "/service/status", "Bearer "+token); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 with the same token after stop and start\ngot:\n%d %s", status, body)
		}
	})

	t.Run("should leave the address alone when start or update cannot bind", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startNamedInstance(t, binary, configDir, "work", "--project-name", "work")
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()
		host, port, _ := net.SplitHostPort(occupied.Addr().String())

		_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "control", "start", "--address", host, "--port", port)
		if err == nil || !strings.Contains(stderr, "listener_unavailable") || !strings.Contains(stderr, "address already in use") {
			t.Fatalf("\nwanted:\nlistener_unavailable: address already in use\ngot:\n%s %v", stderr, err)
		}
		if address := controlAddressOf(t, binary, configDir, "work"); address != nil {
			t.Fatalf("\nwanted:\nstill inactive\ngot:\n%s", *address)
		}

		runServiceControl(t, binary, configDir, "work", "start", "--address", "127.0.0.1", "--port", "0")
		before := *controlAddressOf(t, binary, configDir, "work")
		for _, args := range [][]string{
			{"--address", host, "--port", port},
			{"--address", "no-such-host.invalid", "--port", "0"},
		} {
			_, stderr, err := runMarasi(binary, append([]string{"--config-dir", configDir, "--instance", "work", "service", "control", "update"}, args...)...)
			if err == nil || !strings.Contains(stderr, "listener_unavailable") {
				t.Fatalf("\nwanted:\n%v to fail with listener_unavailable\ngot:\n%s %v", args, stderr, err)
			}
			if after := controlAddressOf(t, binary, configDir, "work"); after == nil || *after != before {
				t.Fatalf("\nwanted:\n%s unchanged\ngot:\n%v", before, after)
			}
		}
		token := controlTokenOf(t, binary, configDir, "work")
		if status, _, body := networkRequest(t, http.MethodGet, before, "/service/status", "Bearer "+token); status != http.StatusOK {
			t.Fatalf("\nwanted:\n200 on the unchanged address\ngot:\n%d %s", status, body)
		}
	})

	t.Run("should answer a stop or move sent on the network address before closing it, and drop its event streams", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
		first := *controlAddressOf(t, binary, configDir, "work")
		token := controlTokenOf(t, binary, configDir, "work")

		stream := openNetworkEvents(t, first, token)
		defer stream.Close()
		response, after := rawNetworkRequest(t, first, token, "POST /service/control-listener/update", `{"address":"127.0.0.1","port":0}`)
		var moved controlListenerStatus
		if err := json.Unmarshal([]byte(response), &moved); err != nil || moved.Status != "active" || moved.ControlListener == nil || *moved.ControlListener == first {
			t.Fatalf("\nwanted:\nthe full update response on the old address\ngot:\n%s", response)
		}
		if after != nil {
			t.Fatalf("\nwanted:\nthe old connection closed after the response\ngot:\n%v", after)
		}
		waitStreamEnd(t, stream)

		stream = openNetworkEvents(t, *moved.ControlListener, token)
		defer stream.Close()
		response, after = rawNetworkRequest(t, *moved.ControlListener, token, "POST /service/control-listener/stop", "")
		if want := `{"status":"inactive","control_listener":null}` + "\n"; response != want || after != nil {
			t.Fatalf("\nwanted:\n%s then a closed connection\ngot:\n%s %v", want, response, after)
		}
		waitStreamEnd(t, stream)
		if address := controlAddressOf(t, binary, configDir, "work"); address != nil {
			t.Fatalf("\nwanted:\ninactive, with the socket still answering\ngot:\n%s", *address)
		}
	})

	// The tests run without a terminal, so stderr carries no warning; the
	// warning goes to stderr only when it is a terminal.
	t.Run("should warn in the instance log, and not on a stderr that is no terminal, when start or update binds beyond loopback", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startNamedInstance(t, binary, configDir, "work", "--project-name", "work")
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

		_, stderr := runServiceControl(t, binary, configDir, "work", "start", "--address", "127.0.0.1", "--port", "0")
		if strings.Contains(stderr, "warning") {
			t.Fatalf("\nwanted:\nno warning for loopback\ngot:\n%s", stderr)
		}
		stdout, stderr := runServiceControl(t, binary, configDir, "work", "update", "--address", "0.0.0.0", "--port", "0", "--json")
		address := *controlAddressOf(t, binary, configDir, "work")
		if stderr != "" || !strings.Contains(stdout, `"control_listener":"`+address+`"`) {
			t.Fatalf("\nwanted:\nno stderr\ngot:\n%s%s", stderr, stdout)
		}
		runServiceControl(t, binary, configDir, "work", "stop")
		_, stderr = runServiceControl(t, binary, configDir, "work", "start", "--address", "0.0.0.0", "--port", "0")
		address = *controlAddressOf(t, binary, configDir, "work")
		if want := "control listener network address started on " + address + "\n"; stderr != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stderr)
		}
		log, err := os.ReadFile(filepath.Join(configDir, "instances", "work.log"))
		warning := "warning: control listener network address " + address + " is reachable beyond loopback and serves plain HTTP without TLS; protect it with an SSH tunnel, a VPN, or a TLS reverse proxy\n"
		if err != nil || strings.Count(string(log), "is reachable beyond loopback") != 2 || !strings.Contains(string(log), warning) {
			t.Fatalf("\nwanted:\ntwo warning lines in the instance log\ngot:\n%s %v", log, err)
		}
	})
}

// openNetworkEvents opens an event stream on the network address and reads
// its connected comment.
func openNetworkEvents(t *testing.T, address, token string) io.ReadCloser {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Transport: &http.Transport{}}).Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("opening events on %s: %v %v", address, response, err)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("\nwanted:\n: connected\ngot:\n%q %v", line, err)
	}
	return response.Body
}

// waitStreamEnd waits for an event stream to end, which it does at once
// when its connection is closed, well before a closed server's drain
// period runs out.
func waitStreamEnd(t *testing.T, stream io.Reader) {
	t.Helper()
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, stream)
		ended <- err
	}()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("\nwanted:\nthe event stream on the closed address to end\ngot:\nstill open")
	}
}

// rawNetworkRequest sends "METHOD PATH" with body on one connection to the
// network address, and returns the response body and the error of a later
// read on the same connection, which is nil once the server has closed it.
func rawNetworkRequest(t *testing.T, address, token, line, body string) (string, error) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	request := fmt.Sprintf("%s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", line, address, token, len(body), body)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading the response to %s: %v", line, err)
	}
	payload, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("\nwanted:\n200 for %s\ngot:\n%d %s %v", line, response.StatusCode, payload, err)
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return string(payload), fmt.Errorf("connection still open after the response: %v", err)
	}
	return string(payload), nil
}
