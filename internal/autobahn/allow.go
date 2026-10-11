package autobahn

import "slices"

// AllowedDeviation is a deviation the spec accepts for a run, with the reason.
type AllowedDeviation struct {
	Deviation
	Reason string
}

// heldMessageReason explains the 6.4.1–6.4.4 allowance.
const heldMessageReason = "held messages are not streamed: the proxy buffers the whole message " +
	"for Checkpoint or the extension, so the peer sees the invalid UTF-8 late (spec: Streaming when not held)"

// allowList maps a run to the cases it may deviate on.
var allowList = map[string][]string{
	RunCheckpointForward: {"6.4.1", "6.4.2", "6.4.3", "6.4.4"},
	RunCheckpointEdit:    {"6.4.1", "6.4.2", "6.4.3", "6.4.4"},
	RunExtension:         {"6.4.1", "6.4.2", "6.4.3", "6.4.4"},
}

// SplitAllowed separates the deviations the spec accepts for run from the
// rest. A case is allowed only when the verdict alone went NON-STRICT; the
// close code and clean close must still match.
func SplitAllowed(run string, deviations []Deviation) (kept []Deviation, allowed []AllowedDeviation) {
	for _, deviation := range deviations {
		if slices.Contains(allowList[run], deviation.Case) &&
			slices.Equal(deviation.Fields, []string{"behavior"}) &&
			deviation.Got.Behavior == "NON-STRICT" {
			allowed = append(allowed, AllowedDeviation{Deviation: deviation, Reason: heldMessageReason})
			continue
		}
		kept = append(kept, deviation)
	}
	return kept, allowed
}
