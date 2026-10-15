package main

import (
	"net/http"
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
