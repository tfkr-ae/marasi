package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startControlAddressInstance starts name with its control listener also
// reachable at controlAddress, and returns the start command's stderr.
func startControlAddressInstance(t *testing.T, binary, configDir, name, controlAddress string) string {
	t.Helper()
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "stop") })
	_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", name, "service", "start", "--port", "0", "--control-address", controlAddress)
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
