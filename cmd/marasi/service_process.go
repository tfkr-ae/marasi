package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/service"
)

const (
	startupReady = byte('R')
	startupError = byte('E')
	detachChild  = byte('D')
)

var serviceChild bool

// childStartup is the child's first message to its parent.
type childStartup struct {
	proxyListener   string // address the proxy is listening on
	controlListener string // control listener network address, "" when none
	err             error  // startup failure, if any
}

func init() {
	startCmd.Flags().BoolVar(&serviceChild, "service-child", false, "")
	_ = startCmd.Flags().MarkHidden("service-child")
}

// startServiceProcess starts a detached child and waits until it reports ready or fails.
func startServiceProcess(ctx context.Context, options serviceOptions, stdout, stderr io.Writer, asJSON bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("opening service executable: %w", err)
	}
	instanceName := filepath.Base(options.instancePath)
	args := []string{
		"--config-dir", options.configDir,
		"--instance", instanceName,
		"service", "start",
		"--project", options.projectPath,
		"--address", options.proxyAddress,
		"--port", strconv.FormatUint(uint64(options.proxyPort), 10),
		"--service-child",
	}
	if options.controlAddress != "" {
		args = append(args, "--control-address", options.controlAddress)
	}
	command := exec.Command(executable, args...)
	configureDetachedProcess(command)
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening discarded child output: %w", err)
	}
	defer discard.Close()
	command.Stderr = discard
	childInput, startupResult, processResult, err := launchServiceChild(command)
	if err != nil {
		return err
	}

	select {
	case startup := <-startupResult:
		if startup.err != nil {
			stopChild(childInput, processResult)
			return startup.err
		}
		if err := ctx.Err(); err != nil {
			stopChild(childInput, processResult)
			return err
		}
		if err := writeStartOutput(stdout, stderr, asJSON, instanceName, startup); err != nil {
			stopChild(childInput, processResult)
			return err
		}
		if _, err := childInput.Write([]byte{detachChild}); err != nil {
			stopChild(childInput, processResult)
			return fmt.Errorf("detaching service child: %w", err)
		}
		_ = childInput.Close()
		return nil
	case <-ctx.Done():
		stopChild(childInput, processResult)
		return ctx.Err()
	}
}

// launchServiceChild starts command and reads its startup status from its
// stdout. The child writes to an os.Pipe rather than a StdoutPipe, because
// Wait closes a StdoutPipe when the child exits, and a child that writes its
// startup error and exits at once would otherwise close the pipe under the
// read. The parent drops its copy of the write end, so the read sees EOF once
// the child is gone.
func launchServiceChild(command *exec.Cmd) (io.WriteCloser, <-chan childStartup, <-chan error, error) {
	childInput, err := command.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening service child input: %w", err)
	}
	childOutput, childOutputWriter, err := os.Pipe()
	if err != nil {
		childInput.Close()
		return nil, nil, nil, fmt.Errorf("opening service child output: %w", err)
	}
	command.Stdout = childOutputWriter
	if err := command.Start(); err != nil {
		childInput.Close()
		childOutput.Close()
		childOutputWriter.Close()
		return nil, nil, nil, fmt.Errorf("starting service process: %w", err)
	}
	_ = childOutputWriter.Close()
	processResult := make(chan error, 1)
	go func() { processResult <- command.Wait() }()
	startupResult := make(chan childStartup, 1)
	go func() {
		startup := readChildStartup(bufio.NewReader(childOutput))
		// A ready child keeps running after the parent detaches, and keeps
		// its stdout open until the parent exits, as before.
		if startup.err != nil {
			_ = childOutput.Close()
		}
		startupResult <- startup
	}()
	return childInput, startupResult, processResult, nil
}

// writeStartOutput writes the start result in JSON or human form. A control
// listener network address beyond loopback adds a warning on stderr when it
// is a terminal, written here because the detached child's stderr is
// discarded. The instance log carries the warning either way.
func writeStartOutput(stdout, stderr io.Writer, asJSON bool, instanceName string, startup childStartup) error {
	var controlListener *string
	warning := ""
	if startup.controlListener != "" {
		controlListener = &startup.controlListener
		if line := service.ControlAddressWarning(startup.controlListener); line != "" && writesToTerminal(stderr) {
			warning = line + "\n"
		}
	}
	if asJSON {
		payload, err := json.Marshal(struct {
			Instance        string  `json:"instance"`
			ProxyListener   string  `json:"proxy_listener"`
			ControlListener *string `json:"control_listener"`
		}{Instance: instanceName, ProxyListener: startup.proxyListener, ControlListener: controlListener})
		if err != nil {
			return fmt.Errorf("encoding service startup: %w", err)
		}
		if _, err := io.WriteString(stderr, warning); err != nil {
			return fmt.Errorf("writing service startup: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", payload); err != nil {
			return fmt.Errorf("writing service startup: %w", err)
		}
		return nil
	}
	// The proxy listener line stays last: scripts read the rest of stderr
	// after it as the proxy listener address.
	output := fmt.Sprintf("instance %s started\n%s", instanceName, warning)
	if controlListener != nil {
		output += fmt.Sprintf("control listener network address started on %s\n", *controlListener)
	}
	output += fmt.Sprintf("proxy listener started on %s\n", startup.proxyListener)
	if _, err := io.WriteString(stderr, output); err != nil {
		return fmt.Errorf("writing service startup: %w", err)
	}
	return nil
}

// readChildStartup reads the child's ready or error status from stdout.
func readChildStartup(reader *bufio.Reader) childStartup {
	status, err := reader.ReadByte()
	if err != nil {
		return childStartup{err: fmt.Errorf("reading service child startup: %w", err)}
	}
	switch status {
	case startupReady:
		proxyListener, err := reader.ReadString('\n')
		if err != nil {
			return childStartup{err: fmt.Errorf("reading service child startup: %w", err)}
		}
		// A child from a build older than the control listener's network
		// address ends its startup after the proxy listener line.
		controlListener, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return childStartup{err: fmt.Errorf("reading service child startup: %w", err)}
		}
		return childStartup{proxyListener: strings.TrimSuffix(proxyListener, "\n"), controlListener: strings.TrimSuffix(controlListener, "\n")}
	case startupError:
		message, err := io.ReadAll(reader)
		if err != nil {
			return childStartup{err: fmt.Errorf("reading service child startup error: %w", err)}
		}
		return childStartup{err: errors.New(string(message))}
	default:
		return childStartup{err: fmt.Errorf("service child sent invalid startup status %q", status)}
	}
}

// stopChild closes the child's stdin and waits for the process to exit.
func stopChild(input io.WriteCloser, processResult <-chan error) {
	_ = input.Close()
	<-processResult
}

// runServiceChild runs the in-process service and reports ready or error on stdout.
func runServiceChild(ctx context.Context, options serviceOptions) error {
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		signal, err := bufio.NewReader(os.Stdin).ReadByte()
		if err != nil || signal != detachChild {
			cancel()
		}
	}()

	ready := false
	err := startServiceInstance(serviceCtx, options, func(started serviceStarted) error {
		if _, err := fmt.Fprintf(os.Stdout, "%c%s\n%s\n", startupReady, started.proxyListener, started.controlListener); err != nil {
			return fmt.Errorf("reporting service startup: %w", err)
		}
		ready = true
		return os.Stdout.Close()
	})
	if !ready {
		_, _ = fmt.Fprintf(os.Stdout, "%c%v", startupError, err)
		_ = os.Stdout.Close()
	}
	return err
}
