package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestLaunchpadSearchCommand(t *testing.T) {
	// moveIDC sorts after bulkIDA, so its launchpad prints second.
	found := `{"` + moveIDC + `":["` + bulkIDB + `"],"` + bulkIDA + `":["` + bulkIDA + `","` + bulkIDB + `"]}`
	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "launchpad search", args: []string{"launchpad", "search", "-q", `"token"`},
			status: http.StatusOK, response: found,
			method: http.MethodGet, path: "/launchpad/search", query: "q=%22token%22",
			stdout: "LAUNCHPAD ID                          REQUEST ID\n" +
				bulkIDA + "  " + bulkIDA + "\n" +
				bulkIDA + "  " + bulkIDB + "\n" +
				moveIDC + "  " + bulkIDB + "\n" +
				"LAUNCHPAD ID                          REQUEST ID\n",
		},
		{
			name: "launchpad search with no matches", args: []string{"launchpad", "search", "-q", "status_code = 999"},
			status: http.StatusOK, response: `{}`,
			method: http.MethodGet, path: "/launchpad/search", query: "q=status_code+%3D+999",
			stdout: "",
		},
	})
}

func TestLaunchpadSearchInvalidQuery(t *testing.T) {
	configDir := serviceConfigDir(t)
	startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"invalid_query","message":"unknown field \"stauts\"","position":1}`)
	stdout, stderr, err := runMarasiWithStdin(buildMarasi(t), "", "--config-dir", configDir, "--instance", "work", "launchpad", "search", "-q", "stauts = 1")
	want := `searching launchpads: invalid_query: unknown field "stauts" at position 1`
	if err == nil || stdout != "" || !strings.Contains(stderr, want) {
		t.Fatalf("\nwanted:\nexit 1 with %s\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
	}
}
