package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	startupReady = byte('R')
	startupError = byte('E')
	detachChild  = byte('D')
)

var serviceChild bool

type childStartup struct {
	proxyListener string
	err           error
}

func init() {
	startCmd.Flags().BoolVar(&serviceChild, "service-child", false, "")
	_ = startCmd.Flags().MarkHidden("service-child")
}

func startServiceProcess(ctx context.Context, configDir, projectName, instancePath, address string, port uint16, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("opening service executable: %w", err)
	}
	instanceName := filepath.Base(instancePath)
	command := exec.Command(executable,
		"--config-dir", configDir,
		"--instance", instanceName,
		"service", "start",
		"--project", projectName,
		"--address", address,
		"--port", strconv.FormatUint(uint64(port), 10),
		"--service-child",
	)
	configureDetachedProcess(command)
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening discarded child output: %w", err)
	}
	defer discard.Close()
	command.Stderr = discard
	childInput, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("opening service child input: %w", err)
	}
	childOutput, err := command.StdoutPipe()
	if err != nil {
		childInput.Close()
		return fmt.Errorf("opening service child output: %w", err)
	}
	if err := command.Start(); err != nil {
		childInput.Close()
		childOutput.Close()
		return fmt.Errorf("starting service process: %w", err)
	}
	processResult := make(chan error, 1)
	go func() { processResult <- command.Wait() }()
	stdout := bufio.NewReader(childOutput)
	startupResult := make(chan childStartup, 1)
	go func() { startupResult <- readChildStartup(stdout) }()

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
		if _, err := fmt.Fprintf(stderr, "instance %s started\nproxy listener started on %s\n", instanceName, startup.proxyListener); err != nil {
			stopChild(childInput, processResult)
			return fmt.Errorf("writing service startup: %w", err)
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
		return childStartup{proxyListener: strings.TrimSuffix(proxyListener, "\n")}
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

func stopChild(input io.WriteCloser, processResult <-chan error) {
	_ = input.Close()
	<-processResult
}

func runServiceChild(ctx context.Context, configDir, projectPath, instancePath, address string, port uint16) error {
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		signal, err := bufio.NewReader(os.Stdin).ReadByte()
		if err != nil || signal != detachChild {
			cancel()
		}
	}()

	ready := false
	err := startServiceReady(serviceCtx, configDir, projectPath, instancePath, address, port, func(proxyListener string) error {
		if _, err := fmt.Fprintf(os.Stdout, "%c%s\n", startupReady, proxyListener); err != nil {
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
