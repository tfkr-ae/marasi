package h2client

import (
	"fmt"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// Recording is an h2-client case's recording: h2spec's verdict, as
// Autobahn's case recordings hold its verdict, and how the shim saw the
// case's connection end. The shim's bytes are TLS, so they are not used.
type Recording struct {
	Verdict Verdict `json:"verdict"`
	// Error is h2spec's error when it could not run the case (NotRun).
	Error string   `json:"error,omitempty"`
	End   shim.End `json:"end"`
}

// record builds the recording of one case from one h2spec run of only
// that case: the cases of its JUnit report (none when h2spec stopped
// before writing one), its console output, and the shim's captures of the
// connections it opened in order. The first is the case's own; a case
// that needs the length of nginx's response body opens a second to
// measure it (spec.ServerDataLength), which is not recorded.
func record(test Test, results []Result, output string, captures []shim.Capture) (Recording, error) {
	if len(captures) == 0 {
		return Recording{}, fmt.Errorf("%s: h2spec opened no connection", test.ID())
	}
	for _, c := range captures {
		if c.Err != "" {
			return Recording{}, fmt.Errorf("%s: the shim could not reach the target: %s", test.ID(), c.Err)
		}
	}
	recording := Recording{End: captures[0].End}
	switch len(results) {
	case 0:
		recording.Verdict = NotRun
		recording.Error = h2specError(output)
	case 1:
		if results[0].Group != test.Group || results[0].Desc != test.Desc {
			return Recording{}, fmt.Errorf("%s: h2spec's report is of %s %q, not %q", test.ID(), results[0].Group, results[0].Desc, test.Desc)
		}
		recording.Verdict = results[0].Verdict
	default:
		return Recording{}, fmt.Errorf("%s: h2spec's report lists %d cases, not one", test.ID(), len(results))
	}
	return recording, nil
}

// h2specError is the last "Error: " line h2spec printed, or all its output
// when it printed none.
func h2specError(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if message, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "Error: "); ok {
			return message
		}
	}
	return strings.TrimSpace(output)
}
