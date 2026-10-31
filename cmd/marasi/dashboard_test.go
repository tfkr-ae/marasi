package main

import (
	"net/http"
	"testing"
)

func TestDashboardCommand(t *testing.T) {
	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "complete", args: []string{"dashboard"},
			status: http.StatusOK, response: `{"pairs":120,"notes":3,"launchpads":2,"intercepted":4,"intercepted_count":{"complete":true}}` + "\n",
			method: http.MethodGet, path: "/dashboard",
			stdout: "pairs        120\nnotes        3\nlaunchpads   2\nintercepted  4\n",
		},
		{
			name: "intercepted count still building", args: []string{"dashboard"},
			status: http.StatusOK, response: `{"pairs":120,"notes":0,"launchpads":0,"intercepted":1,"intercepted_count":{"complete":false}}` + "\n",
			method: http.MethodGet, path: "/dashboard",
			stdout: "pairs        120\nnotes        0\nlaunchpads   0\nintercepted  1\n",
			stderr: "notice: the intercepted count is still building; it may be incomplete\n",
		},
	})
}

func TestDashboardUsage(t *testing.T) {
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), usageArgs(
		[]string{"dashboard", "extra"},
	))
}
