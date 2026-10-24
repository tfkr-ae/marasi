package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRemoteProjects drives a real instance over --remote from a CLI config
// dir that stands in for a second machine, so a project name or path that
// resolved on the CLI's side would land in the wrong directory.
func TestRemoteProjects(t *testing.T) {
	binary := buildMarasi(t)
	instance := startRemoteInstance(t, binary)
	projectsDir := filepath.Join(instance.configDir, "projects")
	if err := os.WriteFile(filepath.Join(projectsDir, "alpha.marasi"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(projectsDir, "folder.marasi"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(projectsDir, "alpha.marasi"), filepath.Join(projectsDir, "linked.marasi")); err != nil {
		t.Fatal(err)
	}

	t.Run("should list the instance machine's projects over --remote as the local list does there", func(t *testing.T) {
		for _, args := range [][]string{
			{"project", "list"},
			{"project", "list", "--json"},
		} {
			wantOut, wantErr, wantRunErr := runMarasiEnv(binary, nil, append([]string{"--config-dir", instance.configDir}, args...)...)
			gotOut, gotErr, gotRunErr := instance.remote(args...)
			if gotOut != wantOut || gotErr != wantErr || exitCode(gotRunErr) != exitCode(wantRunErr) {
				t.Fatalf("\nwanted:\n%v: stdout %q stderr %q exit %d\ngot:\nstdout %q stderr %q exit %d", args, wantOut, wantErr, exitCode(wantRunErr), gotOut, gotErr, exitCode(gotRunErr))
			}
		}
	})

	t.Run("should list this machine's projects without --remote and with no instance", func(t *testing.T) {
		stdout, stderr, err := runMarasiEnv(binary, nil, "--config-dir", instance.cliDir, "project", "list", "--json")
		if want := "{\"items\":[]}\n"; err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\n%q\ngot:\nstdout %q stderr %q err %v", want, stdout, stderr, err)
		}
	})

	t.Run("should resolve --name in the instance's config dir and create nothing on the CLI's machine", func(t *testing.T) {
		stdout, stderr, err := instance.remote("project", "open", "--name", "beta")
		want := "project: " + canonicalProjectPath(t, filepath.Join(projectsDir, "beta.marasi")) + "\n"
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\n%q\ngot:\nstdout %q stderr %q err %v", want, stdout, stderr, err)
		}
		if _, err := os.Stat(filepath.Join(instance.cliDir, "projects")); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nno projects directory in the CLI's config dir\ngot:\n%v", err)
		}
	})

	t.Run("should refuse an invalid --name before dialing", func(t *testing.T) {
		stdout, stderr, err := instance.remote("project", "open", "--name", "../beta")
		if wantErr := "Error: ../beta: invalid project name: absolute paths and parent directory references are not allowed\n"; exitCode(err) != 1 || stdout != "" || stderr != wantErr {
			t.Fatalf("\nwanted:\nexit 1 stderr %q\ngot:\nexit %d stdout %q stderr %q", wantErr, exitCode(err), stdout, stderr)
		}
	})

	t.Run("should send --path unchanged for the instance to resolve", func(t *testing.T) {
		stdout, stderr, err := instance.remote("project", "open", "--path", filepath.Join(projectsDir, "linked.marasi"))
		want := "project: " + canonicalProjectPath(t, filepath.Join(projectsDir, "alpha.marasi")) + "\n"
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\n%q\ngot:\nstdout %q stderr %q err %v", want, stdout, stderr, err)
		}

		// A relative path would name a file in the CLI's working directory if
		// the CLI resolved it; sent unchanged, the instance refuses it.
		stdout, stderr, err = instance.remote("project", "open", "--path", "relative.marasi", "--json")
		if wantOut := "{\"error\":\"opening project: invalid_project_request\"}\n"; exitCode(err) != 1 || stdout != wantOut || stderr != "" {
			t.Fatalf("\nwanted:\nexit 1 stdout %q\ngot:\nexit %d stdout %q stderr %q", wantOut, exitCode(err), stdout, stderr)
		}
	})

	t.Run("should report on stderr what a remote open of a name waits for", func(t *testing.T) {
		posted, reported := make(chan struct{}), make(chan struct{})
		var body string
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/events" {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, ": connected\n\n")
				w.(http.Flusher).Flush()
				<-posted
				io.WriteString(w, "event: project.waiting\ndata: {\"project\":\"/instance/projects/beta.marasi\",\"waiting_for\":[{\"kind\":\"extension\",\"name\":\"workshop\"}]}\n\n")
				w.(http.Flusher).Flush()
				close(reported)
				<-r.Context().Done()
				return
			}
			content, _ := io.ReadAll(r.Body)
			body = string(content)
			close(posted)
			<-reported
			// Let the CLI print the event before the open returns.
			time.Sleep(100 * time.Millisecond)
			io.WriteString(w, `{"project":"/instance/projects/beta.marasi"}`+"\n")
		})}
		go server.Serve(listener)
		t.Cleanup(func() { server.Close() })

		stdout, stderr, err := runMarasiEnv(binary, []string{"MARASI_TOKEN=token"}, "--config-dir", instance.cliDir, "--remote", "http://"+listener.Addr().String(), "project", "open", "--name", "beta.marasi")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v: %s", err, stderr)
		}
		if want := `{"name":"beta"}`; body != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, body)
		}
		if wantOut, wantErr := "project: /instance/projects/beta.marasi\n", "waiting for extension workshop\n"; stdout != wantOut || stderr != wantErr {
			t.Fatalf("\nwanted:\nstdout %q stderr %q\ngot:\nstdout %q stderr %q", wantOut, wantErr, stdout, stderr)
		}
	})

	// Last, since it moves the instance's projects directory aside.
	t.Run("should create a missing projects directory on the instance machine as a local named open does", func(t *testing.T) {
		moved := projectsDir + ".moved"
		if err := os.Rename(projectsDir, moved); err != nil {
			t.Fatal(err)
		}
		if _, stderr, err := instance.remote("project", "open", "--name", "gamma", "--json"); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v: %s", err, stderr)
		}
		info, err := os.Stat(projectsDir)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v %v", os.FileMode(0755), info, err)
		}
		// Close gamma before its directory goes, so the instance stops cleanly.
		if _, stderr, err := instance.remote("project", "open", "--path", filepath.Join(moved, "alpha.marasi"), "--json"); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v: %s", err, stderr)
		}
	})
}
