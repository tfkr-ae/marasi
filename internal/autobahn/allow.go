package autobahn

import "slices"

// AllowedDeviation is a deviation the spec accepts for a run, with the reason.
type AllowedDeviation struct {
	Deviation
	Reason string
}

// wholeMessageReason explains the 6.4.1–6.4.4 allowance.
const wholeMessageReason = "the proxy forwards whole messages, so the peer sees the invalid UTF-8 " +
	"only once the message completes; RFC 6455 does not require forwarding before then (spec: No streaming)"

// wholeMessageCases are the cases that send invalid UTF-8 partway through a
// message and expect the peer to fail fast.
var wholeMessageCases = []string{"6.4.1", "6.4.2", "6.4.3", "6.4.4"}

// allowList maps a run to the cases it may deviate on. Every proxied run
// forwards whole messages.
var allowList = map[string][]string{
	RunProxy:             wholeMessageCases,
	RunCheckpointForward: wholeMessageCases,
	RunCheckpointEdit:    wholeMessageCases,
	RunExtension:         wholeMessageCases,
}

// SplitAllowed separates the deviations the spec accepts for run from the
// rest. A case is allowed only when the verdict alone went NON-STRICT; the
// close code and clean close must still match.
func SplitAllowed(run string, deviations []Deviation) (kept []Deviation, allowed []AllowedDeviation) {
	for _, deviation := range deviations {
		if slices.Contains(allowList[run], deviation.Case) &&
			slices.Equal(deviation.Fields, []string{"behavior"}) &&
			deviation.Got.Behavior == "NON-STRICT" {
			allowed = append(allowed, AllowedDeviation{Deviation: deviation, Reason: wholeMessageReason})
			continue
		}
		kept = append(kept, deviation)
	}
	return kept, allowed
}
