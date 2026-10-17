package main

import (
	"net/http"
	"testing"
)

func TestJobsCommands(t *testing.T) {
	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "jobs list", args: []string{"jobs", "list"},
			status: http.StatusOK, response: `{"items":[{"name":"traffic_index","complete":true,"done":10,"total":10},{"name":"sitemap","complete":false,"done":3,"total":10}]}`,
			method: http.MethodGet, path: "/jobs",
			stdout: "traffic_index  complete  10/10\nsitemap        running   3/10\n",
		},
		{
			name: "jobs rebuild", args: []string{"jobs", "rebuild", "sitemap"},
			status: http.StatusAccepted, response: `{"name":"sitemap","complete":false,"done":0,"total":10}`,
			method: http.MethodPost, path: "/jobs/sitemap/rebuild",
			stderr: "job sitemap rebuild started\n",
		},
	})
}

func TestJobsUsage(t *testing.T) {
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), usageArgs(
		[]string{"jobs", "list", "extra"},
		[]string{"jobs", "rebuild"},
	))
}
