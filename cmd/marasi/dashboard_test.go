package main

import (
	"net/http"
	"testing"
)

func TestDashboardCommand(t *testing.T) {
	t.Parallel()

	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "complete", args: []string{"dashboard"},
			status: http.StatusOK, response: `{"pairs":120,"notes":3,"launchpads":2,"intercepted":4,"intercepted_count":{"complete":true},"project_bytes":1468006}` + "\n",
			method: http.MethodGet, path: "/dashboard",
			stdout: "pairs          120\nnotes          3\nlaunchpads     2\nintercepted    4\nproject_bytes  1468006\n",
		},
		{
			name: "intercepted count still building", args: []string{"dashboard"},
			status: http.StatusOK, response: `{"pairs":120,"notes":0,"launchpads":0,"intercepted":1,"intercepted_count":{"complete":false},"project_bytes":null}` + "\n",
			method: http.MethodGet, path: "/dashboard",
			stdout: "pairs          120\nnotes          0\nlaunchpads     0\nintercepted    1\nproject_bytes  -\n",
			stderr: "notice: the intercepted count is still building; it may be incomplete\n",
		},
		{
			name: "older instance without project_bytes", args: []string{"dashboard"},
			status: http.StatusOK, response: `{"pairs":1,"notes":0,"launchpads":0,"intercepted":0,"intercepted_count":{"complete":true}}` + "\n",
			method: http.MethodGet, path: "/dashboard",
			stdout: "pairs          1\nnotes          0\nlaunchpads     0\nintercepted    0\nproject_bytes  -\n",
		},
	})
}

func TestDashboardUsage(t *testing.T) {
	t.Parallel()

	assertUsageErrorsBeforeRequest(t, buildMarasi(t), usageArgs(
		[]string{"dashboard", "extra"},
	))
}
