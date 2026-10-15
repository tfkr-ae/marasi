package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	bulkIDA  = "0193802f-f0e7-73d9-a764-06d21e367809"
	bulkIDB  = "0193802f-f0e7-73d9-a764-06d21e36780a"
	bulkAsOf = "0193802f-f0e7-73d9-a764-06d21e3678ff"
)

func TestTrafficBulkCommands(t *testing.T) {
	binary := buildMarasi(t)
	idsFile := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(idsFile, []byte(bulkIDA+"\n\n"+bulkIDB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noteFile := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(noteFile, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	idsSelection := `{"ids":["` + bulkIDA + `","` + bulkIDB + `"]}`
	querySelection := `{"query":"host = \"a.test\"","exclusion":["image/*"],"as_of":"` + bulkAsOf + `"}`

	for _, test := range []struct {
		name   string
		args   []string
		stdin  string
		path   string
		body   string
		stderr string
	}{
		{"highlight ids", []string{"traffic", "bulk", "highlight", "3", "--id", bulkIDA, "--id", bulkIDB}, "", "/traffic/bulk/highlight", `{"selection":` + idsSelection + `,"highlight":3}`, "2 pairs highlighted\n"},
		{"highlight none from ids file", []string{"traffic", "bulk", "highlight", "none", "--ids-file", idsFile}, "", "/traffic/bulk/highlight", `{"selection":` + idsSelection + `,"highlight":null}`, "2 pairs unhighlighted\n"},
		{"highlight query", []string{"traffic", "bulk", "highlight", "8", "-q", `host = "a.test"`, "--exclude", "image/*", "--as-of", bulkAsOf}, "", "/traffic/bulk/highlight", `{"selection":` + querySelection + `,"highlight":8}`, "2 pairs highlighted\n"},
		{"link launchpad", []string{"traffic", "bulk", "link", "--launchpad", bulkAsOf, "--id", bulkIDA, "--id", bulkIDB}, "", "/traffic/bulk/link", `{"selection":` + idsSelection + `,"launchpad_id":"` + bulkAsOf + `"}`, "2 pairs linked\n"},
		{"link finding", []string{"traffic", "bulk", "link", "--finding", bulkAsOf, "--ids-file", idsFile}, "", "/traffic/bulk/link", `{"selection":` + idsSelection + `,"finding_id":"` + bulkAsOf + `"}`, "2 pairs linked\n"},
		{"unlink test case", []string{"traffic", "bulk", "unlink", "--test-case", bulkAsOf, "--ids-file", idsFile}, "", "/traffic/bulk/unlink", `{"selection":` + idsSelection + `,"test_case_id":"` + bulkAsOf + `"}`, "2 pairs unlinked\n"},
		{"metadata set", []string{"traffic", "bulk", "metadata", "set", "triage.state", `{"seen": true}`, "--ids-file", idsFile}, "", "/traffic/bulk/metadata", `{"selection":` + idsSelection + `,"key":"triage.state","value":{"seen":true}}`, "2 pairs updated\n"},
		{"metadata set null", []string{"traffic", "bulk", "metadata", "set", "triage", "null", "--ids-file", idsFile}, "", "/traffic/bulk/metadata", `{"selection":` + idsSelection + `,"key":"triage","value":null}`, "2 pairs updated\n"},
		{"metadata clear", []string{"traffic", "bulk", "metadata", "clear", "triage", "--ids-file", idsFile}, "", "/traffic/bulk/metadata", `{"selection":` + idsSelection + `,"key":"triage","clear":true}`, "2 pairs updated\n"},
		{"note append", []string{"traffic", "bulk", "note", "append", "seen twice", "--ids-file", idsFile}, "", "/traffic/bulk/note", `{"selection":` + idsSelection + `,"append":"seen twice"}`, "2 pairs noted\n"},
		{"note append from file", []string{"traffic", "bulk", "note", "append", "--file", noteFile, "--ids-file", idsFile}, "", "/traffic/bulk/note", `{"selection":` + idsSelection + `,"append":"line one\nline two\n"}`, "2 pairs noted\n"},
		{name: "note append from stdin", args: []string{"traffic", "bulk", "note", "append", "--id", bulkIDA, "--id", bulkIDB}, stdin: "piped note\n", path: "/traffic/bulk/note", body: `{"selection":` + idsSelection + `,"append":"piped note\n"}`, stderr: "2 pairs noted\n"},
		{name: "note append with ids from stdin", args: []string{"traffic", "bulk", "note", "append", "seen", "--ids-file", "-"}, stdin: bulkIDA + "\n" + bulkIDB + "\n", path: "/traffic/bulk/note", body: `{"selection":` + idsSelection + `,"append":"seen"}`, stderr: "2 pairs noted\n"},
		{"note clear", []string{"traffic", "bulk", "note", "clear", "--ids-file", idsFile}, "", "/traffic/bulk/note", `{"selection":` + idsSelection + `,"clear":true}`, "2 pairs cleared\n"},
		{"delete", []string{"traffic", "bulk", "delete", "-q", "", "--as-of", bulkAsOf}, "", "/traffic/bulk/delete", `{"selection":{"query":"","as_of":"` + bulkAsOf + `"}}`, "2 pairs deleted\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"count":2}`+"\n")
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasiWithStdin(binary, test.stdin, args...)
			got := sent.snapshot()
			if err != nil || stdout != "" || stderr != test.stderr {
				t.Fatalf("\nwanted:\nstderr %q\ngot:\nstdout %q, stderr %q, error %v", test.stderr, stdout, stderr, err)
			}
			if got.Method != http.MethodPost || got.Path != test.path || got.Body != test.body || got.ContentType != "application/json" {
				t.Fatalf("\nwanted:\nPOST %s body %s\ngot:\n%s %s body %s type %q", test.path, test.body, got.Method, got.Path, got.Body, got.ContentType)
			}

			jsonDir := serviceConfigDir(t)
			startCannedControlAPI(t, jsonDir, "work", http.StatusOK, " {\"count\": 2} ")
			jsonArgs := append([]string{"--config-dir", jsonDir, "--instance", "work", "--json"}, test.args...)
			stdout, stderr, err = runMarasiWithStdin(binary, test.stdin, jsonArgs...)
			if err != nil || stdout != " {\"count\": 2} " || stderr != "" {
				t.Fatalf("\nwanted:\nbody passed through\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
			}
		})
	}
}

func TestTrafficBulkSelectionUsage(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"count":1}`)
	for _, args := range [][]string{
		{"traffic", "bulk", "highlight", "3"},
		{"traffic", "bulk", "highlight", "3", "--id", bulkIDA, "-q", "a", "--as-of", bulkAsOf},
		{"traffic", "bulk", "highlight", "3", "-q", "host = \"a\""},
		{"traffic", "bulk", "highlight", "3", "--as-of", bulkAsOf},
		{"traffic", "bulk", "highlight", "3", "--id", bulkIDA, "--exclude", "image/*"},
		{"traffic", "bulk", "highlight", "3", "--id", "junk"},
		{"traffic", "bulk", "highlight", "3", "-q", "a", "--as-of", "junk"},
		{"traffic", "bulk", "highlight", "3", "--id", bulkIDA, "--ids-file", "-"},
		{"traffic", "bulk", "highlight", "red", "--id", bulkIDA},
		{"traffic", "bulk", "highlight", "--id", bulkIDA},
		{"traffic", "bulk", "link", "--id", bulkIDA},
		{"traffic", "bulk", "link", "--launchpad", bulkAsOf, "--finding", bulkAsOf, "--id", bulkIDA},
		{"traffic", "bulk", "unlink", "--test-case", "junk", "--id", bulkIDA},
		{"traffic", "bulk", "metadata", "set", "k", "not json", "--id", bulkIDA},
		{"traffic", "bulk", "metadata", "set", "k", "--id", bulkIDA},
		{"traffic", "bulk", "metadata", "clear", "--id", bulkIDA},
		{"traffic", "bulk", "note", "append", "--id", bulkIDA},
		{"traffic", "bulk", "note", "append", "", "--id", bulkIDA},
		{"traffic", "bulk", "note", "append", "text", "--file", "/dev/null", "--id", bulkIDA},
		{"traffic", "bulk", "note", "clear", "extra", "--id", bulkIDA},
		{"traffic", "bulk", "delete"},
		{"traffic", "bulk", "delete", "-q", "a"},
		{"traffic", "bulk", "delete", "extra", "--id", bulkIDA},
	} {
		commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
		stdout, stderr, err := runMarasi(binary, commandArgs...)
		if err == nil || stdout != "" {
			t.Fatalf("\nwanted:\nusage error for %v\ngot:\nstdout %q, stderr %q, error %v", args, stdout, stderr, err)
		}
		if got := sent.snapshot(); got.Method != "" {
			t.Fatalf("\nwanted:\nno request for %v\ngot:\n%s %s", args, got.Method, got.Path)
		}
		commandArgs = append(commandArgs, "--json")
		stdout, stderr, err = runMarasi(binary, commandArgs...)
		if err == nil || stderr != "" || !strings.HasPrefix(stdout, `{"error":`) {
			t.Fatalf("\nwanted:\nJSON usage error for %v\ngot:\nstdout %q, stderr %q, error %v", args, stdout, stderr, err)
		}
	}
}

func TestTrafficBulkNoteAppendInputUsage(t *testing.T) {
	const want = "traffic bulk note append requires exactly one of text, --file, or piped stdin"
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), []usageCase{
		{args: []string{"traffic", "bulk", "note", "append", "--id", bulkIDA}, want: want},
		{args: []string{"traffic", "bulk", "note", "append", "text", "--id", bulkIDA}, stdin: "piped", want: want},
		{args: []string{"traffic", "bulk", "note", "append", "--file", "/dev/null", "--id", bulkIDA}, stdin: "piped", want: want},
		{args: []string{"traffic", "bulk", "note", "append", "--ids-file", "-"}, stdin: bulkIDA + "\n", want: "traffic bulk note append requires exactly one of text or --file when --ids-file is -"},
	})
}
