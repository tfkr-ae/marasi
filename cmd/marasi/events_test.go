package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEventsCommand(t *testing.T) {
	t.Run("should print the connected comment and one named event", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedEventAPI(t, configDir, "work", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": connected\n\nevent: traffic.request\ndata: {\"path\":\"/a\"}\n\n")
		})

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "events")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/events" || got.RawQuery != "" {
			t.Fatalf("\nwanted:\nGET /events\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
		if stdout != "traffic.request {\"path\":\"/a\"}\n" {
			t.Fatalf("\nwanted:\ntraffic.request {\"path\":\"/a\"}\ngot:\n%s", stdout)
		}
		if stderr != ": connected\n" {
			t.Fatalf("\nwanted:\n: connected\ngot:\n%s", stderr)
		}
	})

	t.Run("should preserve payloads and omit comments", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedEventAPI(t, configDir, "work", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": connected\n\n: heartbeat\n\n: ignored\n\nevent: traffic.request\ndata: { \"id\" : 1 }  \n\nevent: future.event\ndata: [ 1, 2 ]\n\n")
		})

		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "events")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != "traffic.request { \"id\" : 1 }  \nfuture.event [ 1, 2 ]\n" {
			t.Fatalf("\nwanted:\npreserved known and unknown events\ngot:\n%s", stdout)
		}
		if stderr != ": connected\n" {
			t.Fatalf("\nwanted:\n: connected\ngot:\n%s", stderr)
		}
	})

	for _, args := range [][]string{
		{"--json", "--config-dir", "CONFIG", "--instance", "work", "events"},
		{"--config-dir", "CONFIG", "--instance", "work", "events", "--json"},
	} {
		t.Run("should write NDJSON with the global option in either position", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			startCannedEventAPI(t, configDir, "work", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": connected\n\n: heartbeat\n\nevent: traffic.response\ndata: { \"id\" : 1 }\n\nevent: future.\"event\ndata: [ 2 ]\n\n")
			})
			commandArgs := append([]string(nil), args...)
			for index := range commandArgs {
				if commandArgs[index] == "CONFIG" {
					commandArgs[index] = configDir
				}
			}

			stdout, stderr, err := runMarasi(buildMarasi(t), commandArgs...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			want := "{\"event\":\"traffic.response\",\"data\":{ \"id\" : 1 }}\n{\"event\":\"future.\\\"event\",\"data\":[ 2 ]}\n"
			if stdout != want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
			}
			if stderr != "" {
				t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
			}
		})
	}

	t.Run("should reject extra arguments before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedEventAPI(t, configDir, "work", func(http.ResponseWriter, *http.Request) {})

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "events", "extra")
		if err == nil {
			t.Fatalf("\nwanted:\nargument error\ngot:\n%v", err)
		}
		if got := sent.snapshot(); got.Method != "" {
			t.Fatalf("\nwanted:\nno request\ngot:\n%s %s", got.Method, got.Path)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if stderr == "" {
			t.Fatalf("\nwanted:\nargument error on stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should fail with exit status one and name a missing instance", func(t *testing.T) {
		stdout, stderr, err := runMarasi(buildMarasi(t), "--config-dir", serviceConfigDir(t), "--instance", "work", "events")
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("\nwanted:\nexit status 1\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "work") {
			t.Fatalf("\nwanted:\nstderr naming work\ngot:\n%s", stderr)
		}
	})

	t.Run("should report a missing instance as JSON", func(t *testing.T) {
		stdout, stderr, err := runMarasi(buildMarasi(t), "events", "--json", "--config-dir", serviceConfigDir(t), "--instance", "work")
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("\nwanted:\nexit status 1\ngot:\n%v", err)
		}
		if !strings.Contains(stdout, `"error":"instance work is not running"`) || strings.Count(stdout, "\n") != 1 {
			t.Fatalf("\nwanted:\none JSON error naming work\ngot:\n%s", stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should treat close before connected as failure", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedEventAPI(t, configDir, "work", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
		})

		_, _, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "events")
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("\nwanted:\nexit status 1\ngot:\n%v", err)
		}
	})

	t.Run("should not append a JSON error after a read failure", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedEventAPI(t, configDir, "work", func(w http.ResponseWriter, _ *http.Request) {
			connection, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer connection.Close()
			fmt.Fprint(buffer, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
			body := ": connected\n\nevent: traffic.request\ndata: {\"id\":1}\n\n"
			fmt.Fprintf(buffer, "%x\r\n%s\r\n", len(body), body)
			fmt.Fprint(buffer, "not-a-chunk\r\n")
			buffer.Flush()
		})

		stdout, stderr, err := runMarasi(buildMarasi(t), "--json", "--config-dir", configDir, "--instance", "work", "events")
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("\nwanted:\nexit status 1\ngot:\n%v", err)
		}
		if stdout != "{\"event\":\"traffic.request\",\"data\":{\"id\":1}}\n" {
			t.Fatalf("\nwanted:\nonly the event object\ngot:\n%s", stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should flush each JSON event before exit", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.Interrupt cannot be sent to a Windows process")
		}
		configDir := serviceConfigDir(t)
		startCannedEventAPI(t, configDir, "work", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": connected\n\nevent: traffic.request\ndata: {\"id\":1}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		stdoutReader, stdoutWriter, err := os.Pipe()
		if err != nil {
			t.Fatalf("creating stdout pipe: %v", err)
		}
		t.Cleanup(func() {
			stdoutReader.Close()
			stdoutWriter.Close()
		})
		var stderr bytes.Buffer
		command := exec.Command(buildMarasi(t), "--json", "--config-dir", configDir, "--instance", "work", "events")
		command.Stdout = stdoutWriter
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatalf("starting events command: %v", err)
		}
		t.Cleanup(func() {
			if command.ProcessState == nil {
				command.Process.Kill()
				command.Wait()
			}
		})
		lineRead := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(stdoutReader).ReadString('\n')
			lineRead <- line
		}()
		var stdout string
		select {
		case stdout = <-lineRead:
		case <-time.After(time.Second):
			t.Fatal("events command did not flush its JSON event")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("interrupting events command: %v", err)
		}
		if err := command.Wait(); err != nil {
			t.Fatalf("\nwanted:\nexit status 0\ngot:\n%v", err)
		}
		if stdout != "{\"event\":\"traffic.request\",\"data\":{\"id\":1}}\n" {
			t.Fatalf("\nwanted:\none JSON event\ngot:\n%s", stdout)
		}
		if stderr.String() != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr.String())
		}
	})

	t.Run("should exit successfully when interrupted after connecting", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.Interrupt cannot be sent to a Windows process")
		}
		configDir := serviceConfigDir(t)
		startCannedEventAPI(t, configDir, "work", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": connected\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})

		stderrReader, stderrWriter, err := os.Pipe()
		if err != nil {
			t.Fatalf("creating stderr pipe: %v", err)
		}
		t.Cleanup(func() {
			stderrReader.Close()
			stderrWriter.Close()
		})
		var stdout bytes.Buffer
		command := exec.Command(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "events")
		command.Stdout = &stdout
		command.Stderr = stderrWriter
		if err := command.Start(); err != nil {
			t.Fatalf("starting events command: %v", err)
		}
		t.Cleanup(func() {
			if command.ProcessState == nil {
				command.Process.Kill()
				command.Wait()
			}
		})
		connected := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(stderrReader).ReadString('\n')
			connected <- line
		}()
		var stderr string
		select {
		case stderr = <-connected:
		case <-time.After(time.Second):
			t.Fatal("events command did not connect")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("interrupting events command: %v", err)
		}
		if err := command.Wait(); err != nil {
			t.Fatalf("\nwanted:\nexit status 0\ngot:\n%v", err)
		}
		stderrWriter.Close()
		if stdout.String() != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout.String())
		}
		if stderr != ": connected\n" {
			t.Fatalf("\nwanted:\n: connected\ngot:\n%s", stderr)
		}
	})
}

func startCannedEventAPI(t *testing.T, configDir, name string, write func(http.ResponseWriter, *http.Request)) *cannedControlRequest {
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
		requestBody, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.Method = r.Method
		got.Path = r.URL.Path
		got.RawQuery = r.URL.RawQuery
		got.Body = string(requestBody)
		got.mu.Unlock()
		write(w, r)
	})}
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Close()
		releaseInstance(listener, socketPath, lock)
	})
	return got
}
