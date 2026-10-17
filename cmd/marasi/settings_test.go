package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

const testClientSetting = `{"client":"marasi-app","key":"ledger/view","value":{"columns":["host"]},"updated_at":"2026-10-15T00:00:00Z"}`

func TestSettingsCommands(t *testing.T) {
	valueFile := filepath.Join(t.TempDir(), "value.json")
	if err := os.WriteFile(valueFile, []byte("{\"columns\": [\"host\"]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCannedCommandCases(t, buildMarasi(t), []cannedCommandCase{
		{
			name: "settings list", args: []string{"settings", "list", "marasi-app", "--prefix", "ledger/"},
			status: http.StatusOK, response: `{"items":[` + testClientSetting + `]}`,
			method: http.MethodGet, path: "/client-settings/marasi-app", query: "prefix=ledger%2F",
			stdout: "KEY          VALUE\nledger/view  {\"columns\":[\"host\"]}\nKEY          VALUE\n",
		},
		{
			name: "settings get", args: []string{"settings", "get", "marasi-app", "ledger/view"},
			status: http.StatusOK, response: testClientSetting,
			method: http.MethodGet, path: "/client-settings/marasi-app/ledger/view",
			stdout: "{\"columns\":[\"host\"]}\n",
		},
		{
			name: "settings set", args: []string{"settings", "set", "marasi-app", "ledger/view", `{"columns": ["host"]}`},
			status: http.StatusOK, response: testClientSetting,
			method: http.MethodPut, path: "/client-settings/marasi-app/ledger/view", body: `{"value":{"columns":["host"]}}`,
			stderr: "setting marasi-app ledger/view set\n",
		},
		{
			name: "settings set from file", args: []string{"settings", "set", "marasi-app", "ledger/view", "--file", valueFile},
			status: http.StatusOK, response: testClientSetting,
			method: http.MethodPut, path: "/client-settings/marasi-app/ledger/view", body: `{"value":{"columns":["host"]}}`,
			stderr: "setting marasi-app ledger/view set\n",
		},
		{
			name: "settings set from stdin", args: []string{"settings", "set", "marasi-app", "ledger/view"}, stdin: "{\"columns\": [\"host\"]}\n",
			status: http.StatusOK, response: testClientSetting,
			method: http.MethodPut, path: "/client-settings/marasi-app/ledger/view", body: `{"value":{"columns":["host"]}}`,
			stderr: "setting marasi-app ledger/view set\n",
		},
		{
			name: "settings delete", args: []string{"settings", "delete", "marasi-app", "ledger/view"},
			status: http.StatusOK, response: `{"client":"marasi-app","key":"ledger/view"}`,
			method: http.MethodDelete, path: "/client-settings/marasi-app/ledger/view",
			stderr: "setting marasi-app ledger/view deleted\n",
		},
	})
}

func TestSettingsUsage(t *testing.T) {
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), usageArgs(
		[]string{"settings", "list"},
		[]string{"settings", "get", "marasi-app"},
		[]string{"settings", "set", "marasi-app", "k"},
		[]string{"settings", "set", "marasi-app", "k", "not json"},
		[]string{"settings", "set", "marasi-app", "k", "1", "--file", "/dev/null"},
		[]string{"settings", "delete", "marasi-app"},
	))
}

func TestSettingsSetInputUsage(t *testing.T) {
	const want = "settings set requires exactly one of text, --file, or piped stdin"
	assertUsageErrorsBeforeRequest(t, buildMarasi(t), []usageCase{
		{args: []string{"settings", "set", "marasi-app", "k"}, want: want},
		{args: []string{"settings", "set", "marasi-app", "k", "1"}, stdin: "2", want: want},
		{args: []string{"settings", "set", "marasi-app", "k", "--file", "/dev/null"}, stdin: "2", want: want},
	})
}
