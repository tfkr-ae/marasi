package main

import (
	"net/http"
	"strings"
	"testing"
)

const moveIDC = "0193802f-f0e7-73d9-a764-06d21e36780b"

func TestLaunchpadMoveCommand(t *testing.T) {
	moved := func(ids string) string {
		return `{"source_launchpad_id":"` + bulkAsOf + `","target_launchpad_id":"` + bulkAsOf + `","request_ids":[` + ids + `]}`
	}
	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "launchpad move before an entry", args: []string{"launchpad", "move", bulkAsOf, bulkIDB, bulkIDA, "--before", moveIDC},
			status: http.StatusOK, response: moved(`"` + bulkIDA + `","` + bulkIDB + `"`),
			method: http.MethodPost, path: "/launchpad/" + bulkAsOf + "/move",
			body:   `{"request_ids":["` + bulkIDB + `","` + bulkIDA + `"],"target_launchpad_id":"` + bulkAsOf + `","before_request_id":"` + moveIDC + `"}`,
			stderr: "2 entries moved to launchpad " + bulkAsOf + " successfully\n",
		},
		{
			name: "launchpad move to the end", args: []string{"launchpad", "move", bulkAsOf, bulkIDA},
			status: http.StatusOK, response: moved(`"` + bulkIDA + `"`),
			method: http.MethodPost, path: "/launchpad/" + bulkAsOf + "/move",
			body:   `{"request_ids":["` + bulkIDA + `"],"target_launchpad_id":"` + bulkAsOf + `"}`,
			stderr: "1 entry moved to launchpad " + bulkAsOf + " successfully\n",
		},
	})
}

func TestLaunchpadMoveUsage(t *testing.T) {
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), usageArgs(
		[]string{"launchpad", "move", bulkAsOf},
		[]string{"launchpad", "move", "junk", bulkIDA},
		[]string{"launchpad", "move", bulkAsOf, "junk"},
		[]string{"launchpad", "move", bulkAsOf, bulkIDA, "--before", "junk"},
		[]string{"launchpad", "move", bulkAsOf, bulkIDA, "--target", "junk"},
	))
}

func TestLaunchpadMoveRefusedPlacement(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"invalid_placement"}`)
	args := []string{"--config-dir", configDir, "--instance", "work", "launchpad", "move", bulkAsOf, bulkIDA, "--before", bulkIDA}

	stdout, stderr, err := runMarasiWithStdin(binary, "", args...)
	if err == nil || stdout != "" || !strings.Contains(stderr, "moving launchpad entries: 400 Bad Request") {
		t.Fatalf("\nwanted:\n400 Bad Request on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasiWithStdin(binary, "", append(args, "--json")...)
	if err == nil || stdout != `{"error":"moving launchpad entries: invalid_placement"}`+"\n" || stderr != "" {
		t.Fatalf("\nwanted:\nJSON invalid_placement error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}
