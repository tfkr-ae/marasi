package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestArtifactCommands(t *testing.T) {
	binary := buildMarasi(t)
	id := "01938032-1b17-7243-b035-e6a9f4645904"
	testCaseID := "0193802f-f0e7-73d9-a764-06d21e367809"
	metadata := `{"id":"` + id + `","filename":"proof shot.png","mime_type":"image/png","size":9,"test_case_id":"` + testCaseID + `","finding_id":null,"created_at":"2026-09-16T10:00:00Z"}` + "\n"

	t.Run("upload sends raw file bytes and reports on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := filepath.Join(t.TempDir(), "proof shot.png")
		if err := os.WriteFile(path, []byte("PNG bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, metadata)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "artifact", "upload", "--test-case", testCaseID, "--file", path)
		if err != nil {
			t.Fatal(err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodPost || got.Path != "/test-case/"+testCaseID+"/artifact" || got.RawQuery != "filename=proof+shot.png" || got.Body != "PNG bytes" || got.ContentType != "image/png" {
			t.Fatalf("unexpected upload request: %+v", got)
		}
		if stdout != "" || stderr != "artifact "+id+" uploaded successfully\n" {
			t.Fatalf("unexpected output stdout %q stderr %q", stdout, stderr)
		}
	})

	t.Run("get prints metadata and passes JSON through", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, metadata)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "artifact", "get", id)
		if err != nil {
			t.Fatal(err)
		}
		if sent.snapshot().Path != "/artifact/"+id || stderr != "" || !strings.Contains(stdout, "filename: proof shot.png\n") || !strings.Contains(stdout, "finding_id: \n") {
			t.Fatalf("unexpected get output stdout %q stderr %q", stdout, stderr)
		}

		configDir = serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "json", http.StatusOK, metadata)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "json", "artifact", "get", id, "--json")
		if err != nil || stdout != metadata || stderr != "" {
			t.Fatalf("unexpected JSON get stdout %q stderr %q error %v", stdout, stderr, err)
		}
	})

	t.Run("delete reports on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"id":"`+id+`"}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "artifact", "delete", id)
		if err != nil || sent.snapshot().Method != http.MethodDelete || sent.snapshot().Path != "/artifact/"+id || stdout != "" || stderr != "artifact "+id+" deleted successfully\n" {
			t.Fatalf("unexpected delete stdout %q stderr %q error %v request %+v", stdout, stderr, err, sent.snapshot())
		}
	})

	t.Run("requires exactly one upload parent", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		path := filepath.Join(t.TempDir(), "proof.bin")
		os.WriteFile(path, []byte("proof"), 0600)
		for _, args := range [][]string{
			{"artifact", "upload", "--file", path},
			{"artifact", "upload", "--file", path, "--test-case", id, "--finding", id},
		} {
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "missing"}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("wanted invalid invocation for %v", args)
			}
		}
	})

	t.Run("refuses JSON download before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "artifact", "download", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "does not support --json")
	})

	t.Run("downloads to the metadata basename and overwrites", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		requests := startArtifactDownloadAPI(t, configDir, "work", metadata, []byte("new bytes"))
		dir := t.TempDir()
		output := filepath.Join(dir, "proof shot.png")
		if err := os.WriteFile(output, []byte("old bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, err := runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "work", "artifact", "download", id)
		if err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(output)
		if err != nil || string(contents) != "new bytes" {
			t.Fatalf("wanted overwritten bytes, got %q, %v", contents, err)
		}
		if stdout != "proof shot.png\n" || stderr != "" {
			t.Fatalf("unexpected download output stdout %q stderr %q", stdout, stderr)
		}
		if got := requests.snapshot(); got != "/artifact/"+id+"\n/artifact/"+id+"/content\n" {
			t.Fatalf("unexpected download requests %q", got)
		}
	})
}

type artifactDownloadRequests struct {
	sync.Mutex
	paths strings.Builder
}

func (requests *artifactDownloadRequests) snapshot() string {
	requests.Lock()
	defer requests.Unlock()
	return requests.paths.String()
}

func startArtifactDownloadAPI(t *testing.T, configDir, name, metadata string, contents []byte) *artifactDownloadRequests {
	t.Helper()
	socketPath, lockPath, err := instanceResourcePaths(configDir, name)
	if err != nil {
		t.Fatal(err)
	}
	listener, lock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		t.Fatal(err)
	}
	requests := &artifactDownloadRequests{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Lock()
		requests.paths.WriteString(r.URL.Path + "\n")
		requests.Unlock()
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.Header().Set("Content-Type", "image/png")
			w.Write(contents)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, metadata)
	})}
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Close()
		releaseInstance(listener, socketPath, lock)
	})
	return requests
}

func runMarasiInDir(binary, dir string, args ...string) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	command := exec.Command(binary, args...)
	command.Dir = dir
	command.Stdout = &outBuf
	command.Stderr = &errBuf
	err = command.Run()
	return outBuf.String(), errBuf.String(), err
}
