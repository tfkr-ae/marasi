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

// Kind says whether a deviations entry is on the allow-list or a known deviation.
type Kind string

// Entry kinds. Accepted entries are the allow-list and are edited only by
// hand; known entries are measured and rewritten by update-known.
const (
	KindAccepted Kind = "accepted"
	KindKnown    Kind = "known"
)

// Entry is one entry of a suite's deviations.json.
type Entry struct {
	Cases  string    `json:"cases"`         // glob over case IDs, including the approach
	Runs   []RunName `json:"runs"`          // conformance runs the entry applies to
	Field  string    `json:"field"`         // field path in the recording, or a leg-rule ID
	Got    string    `json:"got,omitempty"` // when set, the only proxied value the entry matches
	Kind   Kind      `json:"kind"`
	Reason string    `json:"reason"`
	RFC    string    `json:"rfc,omitempty"`
	Phase  int       `json:"phase,omitempty"` // the phase expected to remove a known entry
}

// covers reports whether the entry applies to caseID in run.
func (e Entry) covers(run RunName, caseID string) bool {
	if !slices.Contains(e.Runs, run) {
		return false
	}
	ok, _ := path.Match(e.Cases, caseID)
	return ok
}

// matches reports whether the entry lists difference d in run.
func (e Entry) matches(run RunName, d Difference) bool {
	return e.covers(run, d.Case) && e.Field == d.Field && (e.Got == "" || e.Got == d.Got)
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
	if _, err := path.Match(e.Cases, ""); err != nil {
		return fmt.Errorf("cases %q: %w", e.Cases, err)
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
	New     []Difference // listed nowhere: the gate fails
	Known   []Match
	Allowed []Match
}

// Sort sorts the differences of run. An accepted entry wins over a known
// one; a difference neither lists is new.
func (d Deviations) Sort(run RunName, differences []Difference) Sorted {
	sorted, _ := d.sort(run, differences)
	return sorted
}

// sort is Sort that also returns which entries matched something.
func (d Deviations) sort(run RunName, differences []Difference) (Sorted, []bool) {
	matched := make([]bool, len(d))
	var sorted Sorted
	for _, difference := range differences {
		if i := d.find(run, difference, KindAccepted); i >= 0 {
			matched[i] = true
			sorted.Allowed = append(sorted.Allowed, Match{difference, d[i]})
		} else if i := d.find(run, difference, KindKnown); i >= 0 {
			matched[i] = true
			sorted.Known = append(sorted.Known, Match{difference, d[i]})
		} else {
			sorted.New = append(sorted.New, difference)
		}
	}
	return sorted, matched
}

func (d Deviations) find(run RunName, difference Difference, kind Kind) int {
	for i, entry := range d {
		if entry.Kind == kind && entry.matches(run, difference) {
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
