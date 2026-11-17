package autobahn

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// Compare returns the cases where got differs from baseline in verdict,
// closing verdict, close code, or clean close, in Autobahn case order, one
// difference per field. 9.x cases are not compared; their timing is a note.
// Callers pass a baseline already narrowed to the cases the run was asked
// to execute, so a baseline case absent from got is a difference.
func Compare(baseline, got conformance.Recordings) (conformance.Comparison, error) {
	comparison, err := conformance.CompareCases(withoutPerformance(baseline), withoutPerformance(got),
		conformance.CaseComparison{Less: caseLess, Diff: differences})
	if err != nil {
		return conformance.Comparison{}, err
	}
	timing, err := PerformanceTiming(baseline, got)
	if err != nil {
		return conformance.Comparison{}, err
	}
	if timing.Run > 0 {
		comparison.Notes = append(comparison.Notes, fmt.Sprintf("9.x %dms (baseline %dms)", timing.Run, timing.Baseline))
	}
	return comparison, nil
}

// withoutPerformance returns the recordings that are not 9.x cases.
func withoutPerformance(recordings conformance.Recordings) conformance.Recordings {
	kept := conformance.Recordings{}
	for id, recording := range recordings {
		if !IsPerformance(id) {
			kept[id] = recording
		}
	}
	return kept
}

func differences(id string, wantJSON, gotJSON json.RawMessage) ([]conformance.Difference, error) {
	var want, got Result
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		return nil, fmt.Errorf("decoding baseline: %w", err)
	}
	if err := json.Unmarshal(gotJSON, &got); err != nil {
		return nil, fmt.Errorf("decoding result: %w", err)
	}
	var found []conformance.Difference
	add := func(field, want, got string) {
		if want != got {
			found = append(found, conformance.Difference{Case: id, Field: field, Want: want, Got: got})
		}
	}
	add("behavior", want.Behavior, got.Behavior)
	add("behaviorClose", want.BehaviorClose, got.BehaviorClose)
	add("remoteCloseCode", closeCode(want.RemoteCloseCode), closeCode(got.RemoteCloseCode))
	add("wasClean", strconv.FormatBool(want.WasClean), strconv.FormatBool(got.WasClean))
	return found, nil
}

func closeCode(code *int) string {
	if code == nil {
		return "-"
	}
	return strconv.Itoa(*code)
}
