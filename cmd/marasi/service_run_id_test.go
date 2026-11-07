package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServiceStatusRunID(t *testing.T) {
	t.Run("should keep run_id for one process and change it on every start", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("finding the detached service process uses pgrep")
		}
		configDir := serviceConfigDir(t)
		binary := buildMarasi(t)
		args := []string{"--config-dir", configDir, "--instance", "run-id"}
		t.Cleanup(func() { runMarasi(binary, append(args, "service", "stop")...) })

		start := func() {
			t.Helper()
			if _, stderr, err := runMarasi(binary, append(args, "service", "start", "--port", "0", "--json")...); err != nil {
				t.Fatalf("start: %v %s", err, stderr)
			}
		}
		readRunID := func() string {
			t.Helper()
			stdout, stderr, err := runMarasi(binary, append(args, "service", "status", "--json")...)
			if err != nil {
				t.Fatalf("status: %v %s", err, stderr)
			}
			var status struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal([]byte(stdout), &status); err != nil || status.RunID == "" || strings.Contains(status.RunID, ".") {
				t.Fatalf("\nwanted:\nnon-empty run_id without '.'\ngot:\n%s, %v", stdout, err)
			}
			return status.RunID
		}
		servicePIDs := func() []int {
			t.Helper()
			output, err := exec.Command("pgrep", "-f", "--", "--config-dir "+configDir+" ").Output()
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
					return nil
				}
				t.Fatalf("pgrep: %v", err)
			}
			var pids []int
			for _, field := range strings.Fields(string(output)) {
				pid, err := strconv.Atoi(field)
				if err != nil {
					t.Fatalf("pgrep output %q: %v", output, err)
				}
				pids = append(pids, pid)
			}
			return pids
		}

		start()
		first := readRunID()
		if again := readRunID(); again != first {
			t.Fatalf("\nwanted:\nthe same run_id from one process\ngot:\n%q, then %q", first, again)
		}

		if _, stderr, err := runMarasi(binary, append(args, "service", "stop")...); err != nil {
			t.Fatalf("stop: %v %s", err, stderr)
		}
		start()
		restarted := readRunID()
		if restarted == first {
			t.Fatalf("\nwanted:\na new run_id after stop and start\ngot:\n%q again", restarted)
		}

		pids := servicePIDs()
		if len(pids) != 1 {
			t.Fatalf("\nwanted:\none service process\ngot:\n%v", pids)
		}
		process, err := os.FindProcess(pids[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Kill(); err != nil {
			t.Fatalf("killing service: %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for len(servicePIDs()) != 0 {
			if time.Now().After(deadline) {
				t.Fatal("killed service process did not exit")
			}
			time.Sleep(50 * time.Millisecond)
		}
		start()
		if recovered := readRunID(); recovered == restarted || recovered == first {
			t.Fatalf("\nwanted:\na new run_id after kill and restart\ngot:\n%q (earlier %q, %q)", recovered, first, restarted)
		}
	})
}
