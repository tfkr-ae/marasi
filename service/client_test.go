package service

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClient(t *testing.T) {
	t.Run("should call the control API without following redirects", func(t *testing.T) {
		baseDir := ""
		if runtime.GOOS != "windows" {
			baseDir = "/tmp"
		}
		dir, err := os.MkdirTemp(baseDir, "marasi-client-")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer os.RemoveAll(dir)
		socketPath := filepath.Join(dir, "work.sock")
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/service/stop" {
				http.Redirect(w, r, "/followed", http.StatusTemporaryRedirect)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		})}
		go server.Serve(listener)
		defer server.Close()
		client := NewClient(socketPath)
		defer client.Close()
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://marasi/service/stop", nil)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusTemporaryRedirect, response.StatusCode)
		}
	})
}
