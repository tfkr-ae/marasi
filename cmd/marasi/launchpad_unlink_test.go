package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestLaunchpadUnlinkCommand(t *testing.T) {
	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "launchpad unlink", args: []string{"launchpad", "unlink", bulkAsOf, bulkIDA},
			status: http.StatusOK, response: `{"launchpad_id":"` + bulkAsOf + `","request_id":"` + bulkIDA + `"}`,
			method: http.MethodDelete, path: "/launchpad/" + bulkAsOf + "/link/" + bulkIDA,
			stderr: "request " + bulkIDA + " unlinked from launchpad " + bulkAsOf + " successfully\n",
		},
	})
}

func TestLaunchpadUnlinkUsage(t *testing.T) {
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), usageArgs(
		[]string{"launchpad", "unlink", bulkAsOf},
		[]string{"launchpad", "unlink", "junk", bulkIDA},
		[]string{"launchpad", "unlink", bulkAsOf, "junk"},
	))
}

func TestLaunchPendingRefusalCommands(t *testing.T) {
	refused := `{"error":"launch_pending","message":"launch pending: ` + bulkIDA + `","request_ids":["` + bulkIDA + `"]}`
	binary := buildMarasi(t)
	for _, args := range [][]string{
		{"launchpad", "unlink", bulkAsOf, bulkIDA},
		{"traffic", "bulk", "unlink", "--launchpad", bulkAsOf, "--id", bulkIDA},
		{"traffic", "bulk", "delete", "--id", bulkIDA},
	} {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusConflict, refused)
		command := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
		stdout, stderr, err := runMarasiWithStdin(binary, "", command...)
		if err == nil || stdout != "" || !strings.Contains(stderr, "launch_pending: launch pending: "+bulkIDA) {
			t.Fatalf("\nwanted:\nexit 1 naming launch_pending and %s for %v\ngot:\nstdout %q, stderr %q, error %v", bulkIDA, args, stdout, stderr, err)
		}
	}
}
