package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawRequest is one HTTP/1.1 request written byte for byte, so a test can
// send any Host header, as a browser on a rebound name would.
type rawRequest struct {
	method  string
	path    string
	host    string
	headers []string
}

// sendRaw writes request over a new connection to address on network and
// returns the response with its body read.
func sendRaw(t *testing.T, network, address string, request rawRequest) (*http.Response, string) {
	t.Helper()
	conn, err := net.Dial(network, address)
	if err != nil {
		t.Fatalf("dialing %s: %v", address, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	text := request.method + " " + request.path + " HTTP/1.1\r\nHost: " + request.host + "\r\nConnection: close\r\n"
	for _, header := range request.headers {
		text += header + "\r\n"
	}
	if _, err := io.WriteString(conn, text+"\r\n"); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading response to %s %s: %v", request.method, request.path, err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return response, string(body)
}

// corsHeaders returns the CORS response headers of response, one per line,
// so a test can compare them all at once.
func corsHeaders(response *http.Response) string {
	var lines []string
	for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Vary"} {
		if values := response.Header.Values(name); len(values) > 0 {
			lines = append(lines, name+": "+strings.Join(values, ", "))
		}
	}
	return strings.Join(lines, "\n")
}

func TestControlAddressBrowserAccess(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	startControlAddressInstance(t, binary, configDir, "work", "127.0.0.1:0")
	address := *controlAddressOf(t, binary, configDir, "work")
	token := controlTokenOf(t, binary, configDir, "work")
	bearer := "Authorization: Bearer " + token

	t.Run("should echo any origin and never allow credentials", func(t *testing.T) {
		for _, origin := range []string{"http://localhost:5173", "null", "app://marasi", "https://evil.example"} {
			response, _ := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: address, headers: []string{bearer, "Origin: " + origin}})
			want := "Access-Control-Allow-Origin: " + origin + "\nVary: Origin"
			if response.StatusCode != http.StatusOK || corsHeaders(response) != want {
				t.Fatalf("\nwanted:\n200\n%s\ngot:\n%d\n%s", want, response.StatusCode, corsHeaders(response))
			}
		}
	})

	t.Run("should answer a preflight without the token and allow the bearer header", func(t *testing.T) {
		for _, path := range []string{"/service/status", "/project/open", "/service/control-listener/token"} {
			response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodOptions, path: path, host: address, headers: []string{
				"Origin: null",
				"Access-Control-Request-Method: POST",
				"Access-Control-Request-Headers: authorization, content-type",
			}})
			want := "Access-Control-Allow-Origin: null\nAccess-Control-Allow-Methods: GET, POST, PUT, DELETE\nAccess-Control-Allow-Headers: Authorization, Content-Type\nVary: Origin"
			if response.StatusCode != http.StatusNoContent || corsHeaders(response) != want || body != "" {
				t.Fatalf("\nwanted:\n204 for %s\n%s\ngot:\n%d\n%s\n%s", path, want, response.StatusCode, corsHeaders(response), body)
			}
		}
	})

	t.Run("should never add CORS headers on the socket", func(t *testing.T) {
		socketPath := filepath.Join(configDir, "instances", "work.sock")
		for _, method := range []string{http.MethodGet, http.MethodOptions} {
			response, _ := sendRaw(t, "unix", socketPath, rawRequest{method: method, path: "/service/status", host: "marasi", headers: []string{"Origin: https://evil.example", "Access-Control-Request-Method: GET"}})
			if got := corsHeaders(response); got != "" {
				t.Fatalf("\nwanted:\nno CORS headers on %s through the socket\ngot:\n%s", method, got)
			}
		}
	})

	t.Run("should refuse a Host that does not name the bound address before authentication", func(t *testing.T) {
		_, port, _ := net.SplitHostPort(address)
		const misdirected = "{\"error\":\"host_mismatch\",\"message\":\"the Host header does not name the control listener network address\"}\n"
		for _, host := range []string{"evil.example:" + port, "evil.example", "127.0.0.1:1", "127.0.0.2:" + port, "", "marasi"} {
			for _, headers := range [][]string{{bearer}, nil} {
				response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: host, headers: headers})
				if response.StatusCode != http.StatusMisdirectedRequest || body != misdirected {
					t.Fatalf("\nwanted:\n421 for Host %q with %v\n%s\ngot:\n%d %s", host, headers, misdirected, response.StatusCode, body)
				}
			}
		}
		for _, host := range []string{address, "localhost:" + port, "LOCALHOST:" + port} {
			if response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: host, headers: []string{bearer}}); response.StatusCode != http.StatusOK {
				t.Fatalf("\nwanted:\n200 for Host %q\ngot:\n%d %s", host, response.StatusCode, body)
			}
		}
	})

	t.Run("should accept any Host naming the bound port on a wildcard address", func(t *testing.T) {
		startControlAddressInstance(t, binary, configDir, "wild", "0.0.0.0:0")
		wild := *controlAddressOf(t, binary, configDir, "wild")
		wildToken := controlTokenOf(t, binary, configDir, "wild")
		_, port, _ := net.SplitHostPort(wild)
		dial := net.JoinHostPort("127.0.0.1", port)
		for _, host := range []string{wild, "evil.example:" + port, "192.0.2.7:" + port} {
			if response, body := sendRaw(t, "tcp", dial, rawRequest{method: http.MethodGet, path: "/service/status", host: host, headers: []string{"Authorization: Bearer " + wildToken}}); response.StatusCode != http.StatusOK {
				t.Fatalf("\nwanted:\n200 for Host %q\ngot:\n%d %s", host, response.StatusCode, body)
			}
		}
		if response, _ := sendRaw(t, "tcp", dial, rawRequest{method: http.MethodGet, path: "/service/status", host: "evil.example:1", headers: []string{"Authorization: Bearer " + wildToken}}); response.StatusCode != http.StatusMisdirectedRequest {
			t.Fatalf("\nwanted:\n421 for another port\ngot:\n%d", response.StatusCode)
		}
	})

	t.Run("should stream events to a client that sends the bearer header", func(t *testing.T) {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(conn, "GET /events HTTP/1.1\r\nHost: "+address+"\r\n"+bearer+"\r\nOrigin: http://localhost:5173\r\nAccept: text/event-stream\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		// The stream never ends, so closing conn ends it; closing the body
		// would wait for the end.
		want := "Access-Control-Allow-Origin: http://localhost:5173\nVary: Origin"
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" || corsHeaders(response) != want {
			t.Fatalf("\nwanted:\n200 text/event-stream\n%s\ngot:\n%d %s\n%s", want, response.StatusCode, response.Header.Get("Content-Type"), corsHeaders(response))
		}

		stream := bufio.NewReader(response.Body)
		if line, err := stream.ReadString('\n'); err != nil || line != ": connected\n" {
			t.Fatalf("\nwanted:\n: connected\ngot:\n%q %v", line, err)
		}
		if _, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "stop"); err != nil {
			t.Fatalf("stopping the proxy listener: %v\n%s", err, stderr)
		}
		for {
			line, err := stream.ReadString('\n')
			if err != nil {
				t.Fatalf("\nwanted:\nevent: listener.stopped\ngot:\n%v", err)
			}
			if line == "event: listener.stopped\n" {
				break
			}
		}
	})

	t.Run("should refuse a cross-origin request without the token with CORS headers", func(t *testing.T) {
		response, body := sendRaw(t, "tcp", address, rawRequest{method: http.MethodGet, path: "/service/status", host: address, headers: []string{"Origin: https://evil.example"}})
		want := "Access-Control-Allow-Origin: https://evil.example\nVary: Origin"
		if response.StatusCode != http.StatusUnauthorized || corsHeaders(response) != want || !strings.Contains(body, "\"unauthorized\"") {
			t.Fatalf("\nwanted:\n401\n%s\ngot:\n%d\n%s\n%s", want, response.StatusCode, corsHeaders(response), body)
		}
	})
}
