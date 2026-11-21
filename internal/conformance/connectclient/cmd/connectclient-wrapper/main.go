// Command connectclient-wrapper is the client under test the
// connect-client suite gives the connectrpc/conformance runner. It runs
// the reference client and sends each of its cases through a shim (see
// connectclient.Wrap). The suite builds it and sets it up through the
// environment:
//
//	MARASI_CC_CLIENT    the referenceclient binary; empty lists the cases,
//	                    answering each at once with an error
//	MARASI_CC_PROXY     the proxy listener, host:port; empty in the direct run
//	MARASI_CC_EVENTS    the file the wrapper appends its events to
//	MARASI_CC_DEADLINE  how long to wait for a case's result, as a Go duration
//	MARASI_CC_PARALLEL  how many unanswered cases the client may have at once
//
// Arguments are passed on to the reference client.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance/connectclient"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "connectclient-wrapper:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	options := connectclient.Options{Proxy: os.Getenv("MARASI_CC_PROXY")}
	if file := os.Getenv("MARASI_CC_EVENTS"); file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		options.Events = f
	}
	path := os.Getenv("MARASI_CC_CLIENT")
	if path == "" {
		// Listing: no client; every case is answered at its deadline, now.
		stdout, never := io.Pipe()
		defer never.Close()
		return connectclient.Wrap(ctx, os.Stdin, os.Stdout, connectclient.Client{Stdin: discard{}, Stdout: stdout}, options)
	}
	deadline, err := time.ParseDuration(os.Getenv("MARASI_CC_DEADLINE"))
	if err != nil {
		return fmt.Errorf("reading MARASI_CC_DEADLINE: %w", err)
	}
	options.Deadline = deadline
	if options.Parallel, err = strconv.Atoi(os.Getenv("MARASI_CC_PARALLEL")); err != nil {
		return fmt.Errorf("reading MARASI_CC_PARALLEL: %w", err)
	}
	client := exec.Command(path, os.Args[1:]...)
	client.Stderr = os.Stderr
	stdin, err := client.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := client.StdoutPipe()
	if err != nil {
		return err
	}
	if err := client.Start(); err != nil {
		return err
	}
	stopClient := func() {
		_ = client.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = client.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = client.Process.Kill()
			<-done
		}
	}
	return connectclient.Wrap(ctx, os.Stdin, os.Stdout, connectclient.Client{Stdin: stdin, Stdout: stdout, Stop: stopClient}, options)
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
func (discard) Close() error                { return nil }
