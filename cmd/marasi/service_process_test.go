package main

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestLaunchServiceChild(t *testing.T) {
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
}
