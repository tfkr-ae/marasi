package connectclient

import (
	"bufio"
	"bytes"
	"strings"
)

// Verdict is a case's outcome in the runner.
type Verdict string

// Verdicts.
const (
	Passed Verdict = "passed"
	Failed Verdict = "failed"
	// TimedOut: the reference client gave no result within the wrapper's
	// deadline, so the wrapper answered the case with an error.
	TimedOut Verdict = "timed-out"
	// NotRun: the case never reached the client, or the client gave no
	// result and the runner reported none, as after it killed the client.
	NotRun Verdict = "not-run"
)

// Verdicts gives each case in names its verdict from the wrapper's
// events and the runner's output, which prints a FAILED line for every
// case that failed and nothing for one that passed.
func Verdicts(names []string, events []Event, output []byte) map[string]Verdict {
	failed := failures(output)
	last := map[string]EventKind{}
	for _, e := range events {
		last[e.Case] = e.Kind
	}
	verdicts := make(map[string]Verdict, len(names))
	for _, name := range names {
		switch {
		case failed[name] && last[name] == EventDeadline:
			verdicts[name] = TimedOut
		case failed[name]:
			verdicts[name] = Failed
		case last[name] == EventAnswered:
			verdicts[name] = Passed
		default:
			verdicts[name] = NotRun
		}
	}
	return verdicts
}

// failures returns the names of the cases the runner's output reports
// FAILED: "FAILED: <name>:" or "FAILED: <name> was expected to fail but did not".
func failures(output []byte) map[string]bool {
	failed := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		line, ok := strings.CutPrefix(scanner.Text(), "FAILED: ")
		if !ok {
			continue
		}
		if name, ok := strings.CutSuffix(line, " was expected to fail but did not"); ok {
			failed[name] = true
			continue
		}
		failed[strings.TrimSuffix(line, ":")] = true
	}
	return failed
}
