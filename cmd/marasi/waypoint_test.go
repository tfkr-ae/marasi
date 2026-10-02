package main

import (
	"net/http"
	"runtime"
	"testing"
)

func TestWaypointCommands(t *testing.T) {
	binary := buildMarasi(t)

	for _, test := range []struct {
		name       string
		instance   string
		args       []string
		response   string
		method     string
		body       string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "should list waypoints as blocks",
			instance:   "list",
			args:       []string{"waypoint", "list"},
			response:   `{"items":[{"hostname":"a.example:80","override":"127.0.0.1:8080"},{"hostname":"z.example:443","override":"[::1]:9000"}]}` + "\n",
			method:     http.MethodGet,
			wantStdout: "hostname: a.example:80\noverride: 127.0.0.1:8080\n\nhostname: z.example:443\noverride: [::1]:9000\n",
		},
		{
			name:       "should add a waypoint",
			instance:   "add",
			args:       []string{"waypoint", "add", "--hostname", "example.com:443", "--override", "127.0.0.1:8080"},
			response:   `{"items":[{"hostname":"example.com:443","override":"127.0.0.1:8080"}]}` + "\n",
			method:     http.MethodPost,
			body:       `{"hostname":"example.com:443","override":"127.0.0.1:8080"}`,
			wantStderr: "waypoint example.com:443 added\n",
		},
		{
			name:       "should update a waypoint",
			instance:   "update",
			args:       []string{"waypoint", "update", "--hostname", "example.com:443", "--override", "127.0.0.1:9000"},
			response:   `{"items":[{"hostname":"example.com:443","override":"127.0.0.1:9000"}]}` + "\n",
			method:     http.MethodPost,
			body:       `{"hostname":"example.com:443","override":"127.0.0.1:9000"}`,
			wantStderr: "waypoint example.com:443 updated\n",
		},
		{
			name:       "should remove a waypoint",
			instance:   "remove",
			args:       []string{"waypoint", "remove", "--hostname", "example.com:443"},
			response:   `{"items":[]}` + "\n",
			method:     http.MethodDelete,
			body:       `{"hostname":"example.com:443"}`,
			wantStderr: "waypoint example.com:443 removed\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, test.instance, http.StatusOK, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", test.instance}, test.args...)

			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			wantPath := "/waypoint"
			if test.instance == "update" {
				wantPath = "/waypoint/update"
			}
			if got.Method != test.method || got.Path != wantPath || got.Body != test.body || got.ContentType != requestContentType(test.body) {
				t.Fatalf("\nwanted:\n%s %s body %q content type %q\ngot:\n%s %s body %q content type %q", test.method, wantPath, test.body, requestContentType(test.body), got.Method, got.Path, got.Body, got.ContentType)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q", test.wantStdout, test.wantStderr, stdout, stderr)
			}
		})
	}
}

func TestWaypointCommandContract(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should pass successful responses through byte for byte in JSON mode", func(t *testing.T) {
		for index, args := range [][]string{
			{"waypoint", "list"},
			{"waypoint", "add", "--hostname", "example.com:443", "--override", "127.0.0.1:8080"},
			{"waypoint", "update", "--hostname", "example.com:443", "--override", "127.0.0.1:9000"},
			{"waypoint", "remove", "--hostname", "example.com:443"},
		} {
			configDir := serviceConfigDir(t)
			instanceName := "json-" + string(rune('a'+index))
			body := " {\n  \"unexpected\": true\n} "
			startCannedControlAPI(t, configDir, instanceName, http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", instanceName, "--json"}, args...)

			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\n%v wanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", args, body, stdout, stderr, err)
			}
		}
	})

	t.Run("should normalize unavailable instances and API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "waypoint", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		for _, test := range []struct {
			args      []string
			operation string
		}{
			{[]string{"waypoint", "list"}, "listing waypoints"},
			{[]string{"waypoint", "add", "--hostname", "example.com:443", "--override", "127.0.0.1:8080"}, "adding waypoint"},
			{[]string{"waypoint", "update", "--hostname", "example.com:443", "--override", "127.0.0.1:9000"}, "updating waypoint"},
			{[]string{"waypoint", "remove", "--hostname", "example.com:443"}, "removing waypoint"},
		} {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "api", http.StatusConflict, `{"error":"waypoint_already_exists"}`)
			args := append([]string{"--config-dir", configDir, "--instance", "api", "--json"}, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			assertJSONCommandError(t, stdout, stderr, err, test.operation+": waypoint_already_exists")
		}
	})

	t.Run("should enforce arguments and required flags", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"waypoint", "list", "extra"},
			{"waypoint", "add"},
			{"waypoint", "add", "--hostname", "example.com:443"},
			{"waypoint", "add", "--override", "127.0.0.1:8080"},
			{"waypoint", "add", "extra", "--hostname", "example.com:443", "--override", "127.0.0.1:8080"},
			{"waypoint", "update"},
			{"waypoint", "update", "--hostname", "example.com:443"},
			{"waypoint", "update", "--override", "127.0.0.1:9000"},
			{"waypoint", "update", "extra", "--hostname", "example.com:443", "--override", "127.0.0.1:9000"},
			{"waypoint", "remove"},
			{"waypoint", "remove", "extra", "--hostname", "example.com:443"},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should leave an empty list silent", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "empty", http.StatusOK, `{"items":[]}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "empty", "waypoint", "list")
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty streams, nil error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})
}

func TestWaypointCommandLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("service process fixture uses Unix control sockets")
	}
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--project-name", "waypoint-cli", "--port", "0"); err != nil {
		t.Fatalf("starting service: %v", err)
	}
	_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "waypoint", "add", "--hostname", "example.com:443", "--override", "127.0.0.1:8080")
	if err != nil || stderr != "waypoint example.com:443 added\n" {
		t.Fatalf("adding waypoint: stderr %q, error %v", stderr, err)
	}
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "waypoint", "list")
	want := "hostname: example.com:443\noverride: 127.0.0.1:8080\n"
	if err != nil || stdout != want || stderr != "" {
		t.Fatalf("listing waypoints: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	_, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "waypoint", "update", "--hostname", "example.com:443", "--override", "127.0.0.1:9000")
	if err != nil || stderr != "waypoint example.com:443 updated\n" {
		t.Fatalf("updating waypoint: stderr %q, error %v", stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "waypoint", "list")
	want = "hostname: example.com:443\noverride: 127.0.0.1:9000\n"
	if err != nil || stdout != want || stderr != "" {
		t.Fatalf("listing updated waypoint: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	_, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "waypoint", "remove", "--hostname", "example.com:443")
	if err != nil || stderr != "waypoint example.com:443 removed\n" {
		t.Fatalf("removing waypoint: stderr %q, error %v", stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "waypoint", "list")
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("listing removed waypoint: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}
