package main

import (
	"net/http"
	"testing"
)

func TestListTableNamesItsColumns(t *testing.T) {
	t.Run("jobs list names the columns above and below the rows", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"name":"sitemap","complete":true,"done":12,"total":12},{"name":"text_index","complete":false,"done":3,"total":40}]}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "jobs", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "NAME        STATE     PROGRESS\n" +
			"sitemap     complete  12/12\n" +
			"text_index  running   3/40\n" +
			"NAME        STATE     PROGRESS\n"
		if stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q stderr %q", want, stdout, stderr)
		}
	})

	t.Run("an empty list prints no header or footer", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[]}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "finding", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty stdout and stderr\ngot:\nstdout %q stderr %q", stdout, stderr)
		}
	})
}
