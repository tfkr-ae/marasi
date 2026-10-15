package main

import (
	"bytes"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// cannedCommandCase is one CLI command run against a canned control API: the
// response it gets, the request it must send, and the human output it must
// print. In --json mode it must print the response as it is.
type cannedCommandCase struct {
	name     string
	args     []string
	stdin    string
	status   int
	response string
	method   string
	path     string
	query    string
	body     string
	stdout   string
	stderr   string
}

func runCannedCommandCases(t *testing.T, binary string, cases []cannedCommandCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", test.status, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasiWithStdin(binary, test.stdin, args...)
			got := sent.snapshot()
			if err != nil || stdout != test.stdout || stderr != test.stderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q, error %v", test.stdout, test.stderr, stdout, stderr, err)
			}
			if got.Method != test.method || got.Path != test.path || got.RawQuery != test.query || got.Body != test.body {
				t.Fatalf("\nwanted:\n%s %s?%s body %q\ngot:\n%s %s?%s body %q", test.method, test.path, test.query, test.body, got.Method, got.Path, got.RawQuery, got.Body)
			}
			stdout, stderr, err = runMarasiWithStdin(binary, test.stdin, append(args, "--json")...)
			if err != nil || stdout != test.response || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q\ngot:\nstdout %q, stderr %q, error %v", test.response, stdout, stderr, err)
			}
		})
	}
}

// usageCase is one invalid invocation, with optional piped stdin and the
// text its error must contain. An empty stdin leaves stdin at /dev/null, a
// character device, so it reads as no piped input.
type usageCase struct {
	args  []string
	stdin string
	want  string
}

// assertUsageErrorsBeforeRequest runs each invocation and requires a failure
// with empty stdout, stderr containing the case's want, and no request to the
// control API.
func assertUsageErrorsBeforeRequest(t *testing.T, binary string, cases []usageCase) {
	t.Helper()
	configDir := serviceConfigDir(t)
	sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{}`)
	for _, test := range cases {
		commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
		if stdout, stderr, err := runMarasiWithStdin(binary, test.stdin, commandArgs...); err == nil || stdout != "" || !strings.Contains(stderr, test.want) {
			t.Fatalf("\nwanted:\nusage error %q for %v\ngot:\nstdout %q, stderr %q", test.want, test.args, stdout, stderr)
		}
		if got := sent.snapshot(); got.Method != "" {
			t.Fatalf("\nwanted:\nno request for %v\ngot:\n%s %s", test.args, got.Method, got.Path)
		}
	}
}

// usageArgs turns argument lists into usage cases with no piped stdin.
func usageArgs(argLists ...[]string) []usageCase {
	cases := make([]usageCase, len(argLists))
	for i, args := range argLists {
		cases[i] = usageCase{args: args}
	}
	return cases
}

// runMarasiWithStdin runs the binary with stdin piped from text, or from
// /dev/null when text is empty.
func runMarasiWithStdin(binary, stdin string, args ...string) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	command := exec.Command(binary, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	command.Stdout = &outBuf
	command.Stderr = &errBuf
	err = command.Run()
	return outBuf.String(), errBuf.String(), err
}
