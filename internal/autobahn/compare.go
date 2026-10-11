package autobahn

// Deviation is one case whose proxied result differs from the direct baseline.
type Deviation struct {
	Case   string   // Autobahn case id
	Want   Result   // direct baseline result; zero when the case has no baseline
	Got    Result   // proxied result; zero when the case did not run
	Fields []string // what differs: behavior, behaviorClose, remoteCloseCode, wasClean, missing, no baseline
}

// Compare returns the cases where run differs from baseline in verdict,
// closing verdict, close code, or clean close, in Autobahn case order.
// 9.x cases are skipped. Callers pass a baseline already narrowed to the
// cases the run was asked to execute, so a baseline case absent from run
// is a deviation.
func Compare(baseline, run Report) []Deviation {
	ids := map[string]struct{}{}
	for id := range baseline {
		ids[id] = struct{}{}
	}
	for id := range run {
		ids[id] = struct{}{}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		if !IsPerformance(id) {
			ordered = append(ordered, id)
		}
	}
	SortCases(ordered)

	var deviations []Deviation
	for _, id := range ordered {
		want, inBaseline := baseline[id]
		got, inRun := run[id]
		switch {
		case !inRun:
			deviations = append(deviations, Deviation{Case: id, Want: want, Fields: []string{"missing"}})
		case !inBaseline:
			deviations = append(deviations, Deviation{Case: id, Got: got, Fields: []string{"no baseline"}})
		default:
			if fields := differences(want, got); len(fields) > 0 {
				deviations = append(deviations, Deviation{Case: id, Want: want, Got: got, Fields: fields})
			}
		}
	}
	return deviations
}

func differences(want, got Result) []string {
	var fields []string
	if want.Behavior != got.Behavior {
		fields = append(fields, "behavior")
	}
	if want.BehaviorClose != got.BehaviorClose {
		fields = append(fields, "behaviorClose")
	}
	if !sameCode(want.RemoteCloseCode, got.RemoteCloseCode) {
		fields = append(fields, "remoteCloseCode")
	}
	if want.WasClean != got.WasClean {
		fields = append(fields, "wasClean")
	}
	return fields
}

func sameCode(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
