package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestLaunchpadDeleteCommand(t *testing.T) {
	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "launchpad delete", args: []string{"launchpad", "delete", bulkAsOf},
			status: http.StatusOK, response: `{"id":"` + bulkAsOf + `"}`,
			method: http.MethodDelete, path: "/launchpad/" + bulkAsOf,
			stderr: "launchpad " + bulkAsOf + " deleted successfully\n",
		},
	})
}

func TestLaunchpadDeleteUsage(t *testing.T) {
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), usageArgs(
		[]string{"launchpad", "delete"},
		[]string{"launchpad", "delete", "junk"},
		[]string{"launchpad", "delete", bulkAsOf, bulkIDA},
	))
}

func TestLaunchpadDeleteRefusals(t *testing.T) {
	binary := buildMarasi(t)
	for _, test := range []struct {
		name, response, want string
		status               int
	}{
		{"not found", `{"error":"not_found"}`, "deleting launchpad: not_found", http.StatusNotFound},
		{"launch pending", `{"error":"launch_pending","message":"launch pending: ` + bulkIDA + `","request_ids":["` + bulkIDA + `"]}`, "deleting launchpad: launch_pending: launch pending: " + bulkIDA, http.StatusConflict},
	} {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", test.status, test.response)
		stdout, stderr, err := runMarasiWithStdin(binary, "", "--config-dir", configDir, "--instance", "work", "launchpad", "delete", bulkAsOf)
		if err == nil || stdout != "" || !strings.Contains(stderr, test.want) {
			t.Fatalf("%s:\nwanted:\nexit 1 with %q\ngot:\nstdout %q, stderr %q, error %v", test.name, test.want, stdout, stderr, err)
		}
	}
}
