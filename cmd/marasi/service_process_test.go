package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"testing"
)

func TestLaunchServiceChild(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("uses sh to stand in for a service child")
	}

	t.Run("should read the whole startup error of a child that exits at once", func(t *testing.T) {
		// A child that fails startup writes its error and exits, which used
		// to race the read: Wait closed a StdoutPipe while it was still
		// being read, and the parent reported "file already closed".
		for range 200 {
			command := exec.Command("sh", "-c", `printf 'Eopening instance log: boom'`)
			input, startup, processResult, err := launchServiceChild(command)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := <-startup
			stopChild(input, processResult)
			if got.err == nil || got.err.Error() != "opening instance log: boom" {
				t.Fatalf("\nwanted:\nopening instance log: boom\ngot:\n%v", got.err)
			}
		}
	})

	t.Run("should report a child that exits without a status", func(t *testing.T) {
		command := exec.Command("sh", "-c", "exit 0")
		input, startup, processResult, err := launchServiceChild(command)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := <-startup
		stopChild(input, processResult)
		if got.err == nil {
			t.Fatalf("\nwanted:\nan error for a child with no status\ngot:\nnil")
		}
	})

	t.Run("should replace startup EOF with the child exit error and stderr detail", func(t *testing.T) {
		eofErr := fmt.Errorf("reading service child startup: %w", io.EOF)
		exitErr := errors.New("exit status 1")
		realErr := errors.New("opening project: locked")

		for _, tc := range []struct {
			name    string
			startup error
			wait    error
			stderr  string
			want    string
		}{
			{name: "non-EOF startup error", startup: realErr, wait: exitErr, stderr: "ignored", want: "opening project: locked"},
			{name: "EOF with stderr and exit error", startup: eofErr, wait: exitErr, stderr: "Error: unknown flag: --no-password\n", want: "exit status 1: unknown flag: --no-password"},
			{name: "EOF with stderr only", startup: eofErr, stderr: "broken build\n", want: "broken build"},
			{name: "EOF with exit error only", startup: eofErr, wait: exitErr, want: "exit status 1"},
			{name: "EOF with neither", startup: eofErr, want: "reading service child startup: EOF"},
		} {
			got := describeChildEOF(tc.startup, tc.wait, tc.stderr)
			if got == nil || got.Error() != tc.want {
				t.Fatalf("\n%s:\nwanted:\n%s\ngot:\n%v", tc.name, tc.want, got)
			}
		}
	})
}
