package main

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHumanListColumnsStayBounded(t *testing.T) {
	id := "01938032-1b17-7243-b035-e6a9f4645904"
	other := "0193802f-f0e7-73d9-a764-06d21e367809"
	truncated := strings.Repeat("A", 37) + "..."
	created := "2026-09-16T10:00:00Z"

	t.Run("finding list truncates the title before aligning the row", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"`+id+`","title":"`+strings.Repeat("A", 80)+`","severity":"High","cvss_score":8.1,"created_at":"`+created+`"},{"id":"`+other+`","title":"short","severity":"Low","cvss_score":1,"created_at":"`+created+`"}]}`)

		stdout, stderr, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "finding", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "ID" + strings.Repeat(" ", 34) + "  TITLE" + strings.Repeat(" ", 35) + "  SEVERITY  TEST CASE  CVSS  CREATED AT\n"
		want := header +
			id + "  " + truncated + "  High                 8.1   " + created + "\n" +
			other + "  short" + strings.Repeat(" ", 35) + "  Low                  1     " + created + "\n" +
			header
		if stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q stderr %q", want, stdout, stderr)
		}
	})

	t.Run("finding list keeps a title with breaks on one row", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"`+id+`","title":"line1\nline2\tok","severity":"High","cvss_score":8.1,"created_at":"`+created+`"}]}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "finding", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "ID" + strings.Repeat(" ", 34) + "  TITLE           SEVERITY  TEST CASE  CVSS  CREATED AT\n"
		want := header + id + "  line1 line2 ok  High                 8.1   " + created + "\n" + header
		// One row between the header and the footer: the breaks did not split it.
		if stdout != want || strings.Count(stdout, "\n") != 3 {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, stdout)
		}
	})

	t.Run("finding list truncates a title without splitting a rune", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"`+id+`","title":"`+strings.Repeat("A", 36)+`😀XYZQ","severity":"High","cvss_score":8.1,"created_at":"`+created+`"}]}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "finding", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "ID" + strings.Repeat(" ", 34) + "  TITLE" + strings.Repeat(" ", 35) + "  SEVERITY  TEST CASE  CVSS  CREATED AT\n"
		want := header + id + "  " + strings.Repeat("A", 36) + "😀...  High                 8.1   " + created + "\n" + header
		if stdout != want || !utf8.ValidString(stdout) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, stdout)
		}
	})

	t.Run("test case list truncates title category and tags before aligning", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		long := strings.Repeat("A", 80)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"`+id+`","title":"`+long+`","category":"`+long+`","tags":["`+long+`"],"created_at":"`+created+`"},{"id":"`+other+`","title":"short","category":"Web","tags":["auth"],"created_at":"`+created+`"}]}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "test-case", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "ID" + strings.Repeat(" ", 34) + "  TITLE" + strings.Repeat(" ", 35) + "  CATEGORY" + strings.Repeat(" ", 32) + "  TAGS" + strings.Repeat(" ", 36) + "  CREATED AT\n"
		want := header +
			id + "  " + truncated + "  " + truncated + "  " + truncated + "  " + created + "\n" +
			other + "  short" + strings.Repeat(" ", 35) + "  Web" + strings.Repeat(" ", 37) + "  auth" + strings.Repeat(" ", 36) + "  " + created + "\n" +
			header
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, stdout)
		}
	})

	t.Run("test case checklist truncates title and category before aligning", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		long := strings.Repeat("A", 80)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"title":"`+long+`","category":"`+long+`"},{"title":"short","category":"Web"}]}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "test-case", "checklist")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "TITLE" + strings.Repeat(" ", 35) + "  CATEGORY\n"
		want := header + truncated + "  " + truncated + "\nshort" + strings.Repeat(" ", 35) + "  Web\n" + header
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, stdout)
		}
	})

	t.Run("launchpad list truncates name and description before aligning", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		long := strings.Repeat("A", 80)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"`+id+`","name":"`+long+`","description":"`+long+`","entry_count":500,"last_status_code":200},{"id":"`+other+`","name":"short","description":"ok","entry_count":0,"last_status_code":null}]}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "launchpad", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "ID" + strings.Repeat(" ", 34) + "  NAME" + strings.Repeat(" ", 36) + "  DESCRIPTION" + strings.Repeat(" ", 29) + "  ENTRIES  LAST STATUS\n"
		want := header +
			id + "  " + truncated + "  " + truncated + "  500      200\n" +
			other + "  short" + strings.Repeat(" ", 35) + "  ok" + strings.Repeat(" ", 38) + "  0        -\n" +
			header
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, stdout)
		}
	})
}
