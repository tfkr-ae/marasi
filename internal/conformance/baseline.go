package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"slices"
)

// Select returns the recordings whose case ID matches any include pattern
// and no exclude pattern, with an approach in approaches when that is not
// empty. Patterns use path.Match syntax ("3.*", "chunked-*@absolute").
func (r Recordings) Select(include, exclude []string, approaches []Approach) Recordings {
	selected := Recordings{}
	for id, recording := range r {
		if Selected(id, include, exclude, approaches) {
			selected[id] = recording
		}
	}
	return selected
}

// Selected reports whether caseID is selected by include, exclude and approaches.
func Selected(caseID string, include, exclude []string, approaches []Approach) bool {
	if len(approaches) > 0 && !slices.Contains(approaches, CaseApproach(caseID)) {
		return false
	}
	return matchAny(include, caseID) && !matchAny(exclude, caseID)
}

func matchAny(patterns []string, caseID string) bool {
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, caseID); ok {
			return true
		}
	}
	return false
}

// ReadBaseline reads a committed direct baseline. A missing file has no recordings.
func ReadBaseline(file string) (Recordings, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return Recordings{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading baseline: %w", err)
	}
	recordings := Recordings{}
	if err := json.Unmarshal(data, &recordings); err != nil {
		return nil, fmt.Errorf("decoding baseline %s: %w", file, err)
	}
	// Compact, as a suite returns them, so a recording reads the same
	// whether it came from a file or a tester.
	for id, recording := range recordings {
		var compact bytes.Buffer
		if err := json.Compact(&compact, recording); err != nil {
			return nil, fmt.Errorf("decoding baseline %s case %s: %w", file, id, err)
		}
		recordings[id] = compact.Bytes()
	}
	return recordings, nil
}

// WriteBaseline writes recordings as indented JSON with sorted case IDs.
func WriteBaseline(file string, recordings Recordings) error {
	return writeJSON(file, recordings)
}

// mergeBaseline replaces the selected cases in the baseline file with got.
func mergeBaseline(file string, job Job, got Recordings) error {
	baseline, err := ReadBaseline(file)
	if err != nil {
		return err
	}
	for id := range baseline.Select(job.Cases, job.Exclude, job.Approaches) {
		delete(baseline, id)
	}
	for id, recording := range got {
		baseline[id] = recording
	}
	return WriteBaseline(file, baseline)
}

// LegChecker checks the leg rules on one proxied recording. It returns a
// difference for each broken rule and a check for each rule it checked.
type LegChecker func(caseID string, got json.RawMessage) ([]Difference, []LegCheck, error)

// CompareCases compares every case in baseline or got, in the order less
// gives, skipping the cases skip names. A case on one side only is a
// FieldMissing or FieldNoBaseline difference; diff compares the rest.
// Each legs, such as h1.Check, then checks the leg rules on every case the
// run recorded, after the case's comparison differences. Suites build
// Compare on it.
func CompareCases(baseline, got Recordings, skip func(caseID string) bool, less func(a, b string) bool,
	diff func(caseID string, want, got json.RawMessage) ([]Difference, error), legs ...LegChecker) (Comparison, error) {
	ids := map[string]struct{}{}
	for id := range baseline {
		ids[id] = struct{}{}
	}
	for id := range got {
		ids[id] = struct{}{}
	}
	var comparison Comparison
	for id := range ids {
		if skip == nil || !skip(id) {
			comparison.Cases = append(comparison.Cases, id)
		}
	}
	slices.SortFunc(comparison.Cases, func(a, b string) int {
		switch {
		case less(a, b):
			return -1
		case less(b, a):
			return 1
		}
		return 0
	})
	for _, id := range comparison.Cases {
		want, inBaseline := baseline[id]
		recording, inRun := got[id]
		switch {
		case !inRun:
			comparison.Differences = append(comparison.Differences, Difference{Case: id, Field: FieldMissing})
		case !inBaseline:
			comparison.Differences = append(comparison.Differences, Difference{Case: id, Field: FieldNoBaseline})
		default:
			differences, err := diff(id, want, recording)
			if err != nil {
				return Comparison{}, fmt.Errorf("comparing case %s: %w", id, err)
			}
			comparison.Differences = append(comparison.Differences, differences...)
		}
		if !inRun {
			continue
		}
		for _, check := range legs {
			differences, checks, err := check(id, recording)
			if err != nil {
				return Comparison{}, fmt.Errorf("checking leg rules on case %s: %w", id, err)
			}
			comparison.Differences = append(comparison.Differences, differences...)
			comparison.LegChecks = append(comparison.LegChecks, checks...)
		}
	}
	return comparison, nil
}
