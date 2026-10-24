package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLocalFile writes contents to a new file named name in the CLI
// machine's working directory and returns its path.
func writeLocalFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertFileContents fails unless path holds contents.
func assertFileContents(t *testing.T, path, contents string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != contents {
		t.Fatalf("\nwanted:\n%s holding %q\ngot:\n%q, %v", path, contents, got, err)
	}
}

// assertNoFile fails unless nothing exists at path.
func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("\nwanted:\nno file at %s\ngot:\n%v", path, err)
	}
}

func TestRemoteAddUploads(t *testing.T) {
	binary := buildMarasi(t)
	instance := startRemoteInstance(t, binary)

	t.Run("should upload a wordlist, publish wordlist.added, and delete the local file", func(t *testing.T) {
		_, events, closeEvents := connectInstanceEvents(t, filepath.Join(instance.configDir, "instances", "work.sock"))
		defer closeEvents()
		source := writeLocalFile(t, "remote-users.txt", "alice\nbob\n")

		stdout, stderr, err := instance.remote("wordlist", "add", source)
		if err != nil || stdout != "" || stderr != "wordlist remote-users.txt added\n" {
			t.Fatalf("\nwanted:\nexit 0, empty stdout, stderr %q\ngot:\n%v, stdout %q, stderr %q", "wordlist remote-users.txt added\n", err, stdout, stderr)
		}
		assertNoFile(t, source)
		assertFileContents(t, filepath.Join(instance.configDir, "wordlists", "remote-users.txt"), "alice\nbob\n")
		_, data := readEventNamed(t, events, "wordlist.added")
		if !strings.Contains(data, `{"name":"remote-users.txt","size":10}`) {
			t.Fatalf("\nwanted:\nwordlist.added listing remote-users.txt\ngot:\n%s", data)
		}
	})

	t.Run("should keep the local file when the instance already has that wordlist", func(t *testing.T) {
		source := writeLocalFile(t, "remote-taken.txt", "first\n")
		if _, stderr, err := instance.remote("wordlist", "add", source); err != nil {
			t.Fatalf("adding the first wordlist: %v\n%s", err, stderr)
		}
		source = writeLocalFile(t, "remote-taken.txt", "second\n")

		stdout, stderr, err := instance.remote("wordlist", "add", source, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "adding wordlist: wordlist_already_exists")
		assertFileContents(t, source, "second\n")
		assertFileContents(t, filepath.Join(instance.configDir, "wordlists", "remote-taken.txt"), "first\n")
	})

	t.Run("should upload a report template, publish report.template.added, and delete the local file", func(t *testing.T) {
		_, events, closeEvents := connectInstanceEvents(t, filepath.Join(instance.configDir, "instances", "work.sock"))
		defer closeEvents()
		source := writeLocalFile(t, "remote.md", "# {{ .Title }}\n")

		stdout, stderr, err := instance.remote("report", "template", "add", source)
		if err != nil || stdout != "" || stderr != "report template remote.md added\n" {
			t.Fatalf("\nwanted:\nexit 0, empty stdout, stderr %q\ngot:\n%v, stdout %q, stderr %q", "report template remote.md added\n", err, stdout, stderr)
		}
		assertNoFile(t, source)
		assertFileContents(t, filepath.Join(instance.configDir, "templates", "remote.md"), "# {{ .Title }}\n")
		_, data := readEventNamed(t, events, "report.template.added")
		if !strings.Contains(data, `{"name":"remote.md","size":15}`) {
			t.Fatalf("\nwanted:\nreport.template.added listing remote.md\ngot:\n%s", data)
		}
	})

	t.Run("should keep the local file when the instance refuses a report template", func(t *testing.T) {
		taken := writeLocalFile(t, "remote-taken.md", "first\n")
		if _, stderr, err := instance.remote("report", "template", "add", taken); err != nil {
			t.Fatalf("adding the first template: %v\n%s", err, stderr)
		}
		for _, test := range []struct {
			name string
			want string
		}{
			{name: "remote-taken.md", want: "adding report template: report_template_already_exists"},
			{name: ".hidden.md", want: "adding report template: invalid_report_template_request"},
		} {
			source := writeLocalFile(t, test.name, "second\n")
			stdout, stderr, err := instance.remote("report", "template", "add", source, "--json")
			assertJSONCommandError(t, stdout, stderr, err, test.want)
			assertFileContents(t, source, "second\n")
		}
		assertFileContents(t, filepath.Join(instance.configDir, "templates", "remote-taken.md"), "first\n")
		assertNoFile(t, filepath.Join(instance.configDir, "templates", ".hidden.md"))
	})
}

// uploadRequest is what a canned remote control API received.
type uploadRequest struct {
	method, path, query, contentType, body string
}

// startCannedUploadAPI serves status and body at a network address for every
// request and sends each request it received on the returned channel.
func startCannedUploadAPI(t *testing.T, status int, body string) (string, <-chan uploadRequest) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan uploadRequest, 10)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ := io.ReadAll(r.Body)
		requests <- uploadRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, contentType: r.Header.Get("Content-Type"), body: string(received)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String(), requests
}

// The instance in these tests shares the CLI's file system, so only a canned
// API can show that a remote add sends contents rather than a path.
func TestRemoteAddSendsContents(t *testing.T) {
	binary := buildMarasi(t)

	for _, test := range []struct {
		args func(source string) []string
		name string
		path string
	}{
		{args: func(source string) []string { return []string{"wordlist", "add", source} }, name: "users.txt", path: "/wordlist"},
		{args: func(source string) []string { return []string{"report", "template", "add", source} }, name: "custom.md", path: "/report/template"},
	} {
		t.Run("should send the name and contents to "+test.path+", not a path on the CLI machine", func(t *testing.T) {
			remote, requests := startCannedUploadAPI(t, http.StatusOK, `{"items":[]}`+"\n")
			source := writeLocalFile(t, test.name, "line one\nline two\n")

			stdout, stderr, err := runMarasiEnv(binary, []string{"MARASI_TOKEN=secret"}, append([]string{"--config-dir", t.TempDir(), "--remote", remote, "--json"}, test.args(source)...)...)
			if err != nil || stdout != `{"items":[]}`+"\n" || stderr != "" {
				t.Fatalf("\nwanted:\nexit 0 and the response passed through\ngot:\n%v, stdout %q, stderr %q", err, stdout, stderr)
			}
			want := uploadRequest{method: http.MethodPost, path: test.path, query: "name=" + test.name, contentType: "application/octet-stream", body: "line one\nline two\n"}
			if got := <-requests; got != want {
				t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
			}
			assertNoFile(t, source)
		})

		t.Run("should keep the local file when "+test.path+" refuses the upload", func(t *testing.T) {
			remote, _ := startCannedUploadAPI(t, http.StatusConflict, `{"error":"already_exists"}`+"\n")
			source := writeLocalFile(t, test.name, "kept\n")

			_, stderr, err := runMarasiEnv(binary, []string{"MARASI_TOKEN=secret"}, append([]string{"--config-dir", t.TempDir(), "--remote", remote}, test.args(source)...)...)
			if exitCode(err) != 1 || !strings.Contains(stderr, "already_exists") {
				t.Fatalf("\nwanted:\nexit 1 naming already_exists\ngot:\n%v, stderr %q", err, stderr)
			}
			assertFileContents(t, source, "kept\n")
		})

		t.Run("should keep the local file when no instance answers for "+test.path, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			closed := "http://" + listener.Addr().String()
			listener.Close()
			source := writeLocalFile(t, test.name, "kept\n")

			_, stderr, err := runMarasiEnv(binary, []string{"MARASI_TOKEN=secret"}, append([]string{"--config-dir", t.TempDir(), "--remote", closed}, test.args(source)...)...)
			if exitCode(err) != 1 || stderr == "" {
				t.Fatalf("\nwanted:\nexit 1 with an error\ngot:\n%v, stderr %q", err, stderr)
			}
			assertFileContents(t, source, "kept\n")
		})
	}
}
