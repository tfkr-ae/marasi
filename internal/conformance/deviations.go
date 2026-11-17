package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
)

// EntryKind says whether a deviations entry is on the allow-list or a known deviation.
type EntryKind string

// Entry kinds. Accepted entries are the allow-list and are edited only by
// hand; known entries are measured and rewritten by update-known.
const (
	KindAccepted EntryKind = "accepted"
	KindKnown    EntryKind = "known"
)

// Entry is one entry of a suite's deviations.json.
type Entry struct {
	Cases string `json:"cases"` // glob over case IDs, including the approach
	// Targets are globs over the suite's targets the entry applies to, such
	// as "fuzzingserver-*"; empty means every target.
	Targets []string  `json:"targets,omitempty"`
	Runs    []RunName `json:"runs"`          // conformance runs the entry applies to
	Field   string    `json:"field"`         // field path in the recording, or a leg-rule ID
	Got     string    `json:"got,omitempty"` // when set, the only proxied value the entry matches
	Kind    EntryKind `json:"kind"`
	Reason  string    `json:"reason"`
	RFC     string    `json:"rfc,omitempty"`
	Phase   int       `json:"phase,omitempty"` // the phase expected to remove a known entry
}

// appliesTo reports whether the entry applies to target in run.
func (e Entry) appliesTo(run RunName, target string) bool {
	return slices.Contains(e.Runs, run) && (len(e.Targets) == 0 || matchAny(e.Targets, target))
}

// covers reports whether the entry applies to caseID of target in run.
func (e Entry) covers(run RunName, target, caseID string) bool {
	if !e.appliesTo(run, target) {
		return false
	}
	ok, _ := path.Match(e.Cases, caseID)
	return ok
}

// matches reports whether the entry lists difference d of target in run.
func (e Entry) matches(run RunName, target string, d Difference) bool {
	return e.covers(run, target, d.Case) && e.Field == d.Field && (e.Got == "" || e.Got == d.Got)
}

func (e Entry) validate() error {
	switch {
	case e.Cases == "":
		return errors.New("no cases")
	case len(e.Runs) == 0:
		return errors.New("no runs")
	case e.Field == "":
		return errors.New("no field")
	case e.Reason == "":
		return errors.New("no reason")
	case e.Kind != KindAccepted && e.Kind != KindKnown:
		return fmt.Errorf("kind %q is neither accepted nor known", e.Kind)
	case e.Kind == KindKnown && e.Phase <= 0:
		return errors.New("a known entry needs a phase")
	}
	for _, pattern := range append([]string{e.Cases}, e.Targets...) {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("matching pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// Deviations is a suite's deviations.json: the allow-list and the known deviations.
type Deviations []Entry

// ReadDeviations reads and checks a deviations file. A missing file has no entries.
func ReadDeviations(file string) (Deviations, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return Deviations{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading deviations: %w", err)
	}
	var deviations Deviations
	if err := json.Unmarshal(data, &deviations); err != nil {
		return nil, fmt.Errorf("decoding deviations %s: %w", file, err)
	}
	for i, entry := range deviations {
		if err := entry.validate(); err != nil {
			return nil, fmt.Errorf("deviations %s entry %d: %w", file, i+1, err)
		}
	}
	return deviations, nil
}

// WriteDeviations writes deviations as indented JSON, in entry order.
func WriteDeviations(file string, deviations Deviations) error {
	if deviations == nil {
		deviations = Deviations{}
	}
	return writeJSON(file, deviations)
}

// Match is a difference and the entry that lists it.
type Match struct {
	Difference
	Entry Entry
}

// Sorted is a run's differences sorted against the deviations file.
type Sorted struct {
	New         []Difference // listed nowhere: the gate fails
	Known       []Match
	OnAllowList []Match
	Warnings    []Difference // broken SHOULD leg rules listed nowhere: reported, not failed
}

// Sort sorts the differences of target in run. An accepted entry wins over
// a known one; a difference neither lists is new, or a warning when it is a
// broken SHOULD leg rule.
func (d Deviations) Sort(run RunName, target string, differences []Difference) Sorted {
	sorted, _ := d.sort(run, target, differences)
	return sorted
}

// sort is Sort that also returns which entries matched something.
func (d Deviations) sort(run RunName, target string, differences []Difference) (Sorted, []bool) {
	matched := make([]bool, len(d))
	var sorted Sorted
	for _, difference := range differences {
		if i := d.find(run, target, difference, KindAccepted); i >= 0 {
			matched[i] = true
			sorted.OnAllowList = append(sorted.OnAllowList, Match{difference, d[i]})
		} else if i := d.find(run, target, difference, KindKnown); i >= 0 {
			matched[i] = true
			sorted.Known = append(sorted.Known, Match{difference, d[i]})
		} else if difference.Level == LevelShould {
			sorted.Warnings = append(sorted.Warnings, difference)
		} else {
			sorted.New = append(sorted.New, difference)
		}
	}
	return sorted, matched
}

func (d Deviations) find(run RunName, target string, difference Difference, kind EntryKind) int {
	for i, entry := range d {
		if entry.Kind == kind && entry.matches(run, target, difference) {
			return i
		}
	}
	return -1
}

// writeJSON writes value as indented JSON. HTML is not escaped, so a reason
// such as "a -> b" stays readable in the committed file.
func writeJSON(file string, value any) error {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encoding %s: %w", filepath.Base(file), err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(file), err)
	}
	return os.WriteFile(file, data.Bytes(), 0o644)
}
