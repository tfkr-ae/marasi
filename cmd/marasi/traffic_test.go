package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTrafficListCommand(t *testing.T) {
	t.Run("should print aligned rows for the newest page", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"0193802f-f0e7-73d9-a764-06d21e367809","scheme":"https","method":"GET","host":"example.com","path":"/a","status":"200 OK","status_code":200,"content_type":"application/json","length":"12","metadata":{"foo":"bar"},"requested_at":"2026-01-02T03:04:05Z","responded_at":"2026-01-02T03:04:06Z"},{"id":"01938032-1b17-7243-b035-e6a9f4645904","scheme":"https","method":"POST","host":"example.com","path":"/login","status":"401 Unauthorized","status_code":401,"content_type":"text/plain","length":"45","metadata":{},"requested_at":"2026-01-02T03:04:04Z","responded_at":"2026-01-02T03:04:05Z"}],"next_cursor":null}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if sent.Method != http.MethodGet || sent.Path != "/traffic" || sent.RawQuery != "limit=200" {
			t.Fatalf("\nwanted:\nGET /traffic?limit=200\ngot:\n%s %s?%s", sent.Method, sent.Path, sent.RawQuery)
		}
		want := "0193802f-f0e7-73d9-a764-06d21e367809  GET   example.com  /a      200  12\n01938032-1b17-7243-b035-e6a9f4645904  POST  example.com  /login  401  45\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should write next_cursor to stderr when another page exists", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12"}],"next_cursor":"0193802f-f0e7-73d9-a764-06d21e367809"}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "01938032-1b17-7243-b035-e6a9f4645904  GET  example.com  /a  200  12\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if stderr != "next_cursor=0193802f-f0e7-73d9-a764-06d21e367809\n" {
			t.Fatalf("\nwanted:\nnext_cursor=0193802f-f0e7-73d9-a764-06d21e367809\ngot:\n%s", stderr)
		}
	})

	t.Run("should print the control API list body with --json", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12"}],"next_cursor":"0193802f-f0e7-73d9-a764-06d21e367809"}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "--json", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != body {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", body, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should print no rows for an empty page", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno rows\ngot:\n%s", stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should fail and name the instance when the control listener is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "work") {
			t.Fatalf("\nwanted:\nerror naming work\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "work") {
			t.Fatalf("\nwanted:\nstderr naming work\ngot:\n%s", stderr)
		}
	})

	t.Run("should fail without JSON when the control listener is missing and --json is set", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list", "--json")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "work") {
			t.Fatalf("\nwanted:\nerror naming work\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "work") {
			t.Fatalf("\nwanted:\nstderr naming work\ngot:\n%s", stderr)
		}
	})

	t.Run("should reject --project", func(t *testing.T) {
		stdout, stderr, err := executeRoot(t, "--config-dir", serviceConfigDir(t), "traffic", "list", "--project", "scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "unknown flag: --project") {
			t.Fatalf("\nwanted:\nunknown flag: --project\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "unknown flag: --project") {
			t.Fatalf("\nwanted:\nstderr naming unknown flag\ngot:\n%s", stderr)
		}
	})
}

type cannedControlRequest struct {
	Method   string
	Path     string
	RawQuery string
}

func startCannedControlAPI(t *testing.T, configDir, name string, status int, body string) *cannedControlRequest {
	t.Helper()
	socketPath, lockPath, err := instanceResourcePaths(configDir, name)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	listener, lock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	got := &cannedControlRequest{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.Path
		got.RawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})}
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Close()
		releaseInstance(listener, socketPath, lock)
	})
	return got
}

func executeRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	oldJSON := trafficJSON
	oldConfigDir, oldInstance, oldInstancePath := configDir, instance, instancePath
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	t.Cleanup(func() {
		trafficJSON = oldJSON
		configDir, instance, instancePath = oldConfigDir, oldInstance, oldInstancePath
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetContext(nil)
	})
	err = rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}
