package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommittedEntriesCoverABaselineCase guards the stale check of every
// CI job, each of which runs one conformance run on every case: an entry
// whose cases glob covers no baseline case of a target it applies to is
// stale in each job that runs one of its runs.
func TestCommittedEntriesCoverABaselineCase(t *testing.T) {
	suites, err := os.ReadDir("baselines")
	if err != nil {
		t.Fatal(err)
	}
	for _, suite := range suites {
		dir := filepath.Join("baselines", suite.Name())
		t.Run("should cover a baseline case with every "+suite.Name()+" entry", func(t *testing.T) {
			deviations, err := ReadDeviations(filepath.Join(dir, "deviations.json"))
			if err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(dir, "*.json"))
			if err != nil {
				t.Fatal(err)
			}
			baselines := map[string]Recordings{}
			for _, file := range files {
				target := strings.TrimSuffix(filepath.Base(file), ".json")
				if target == "deviations" || target == "score" {
					continue
				}
				if baselines[target], err = ReadBaseline(file); err != nil {
					t.Fatal(err)
				}
			}
			for i, entry := range deviations {
				for _, run := range entry.Runs {
					if !coversBaseline(entry, run, baselines) {
						t.Errorf("\nwanted:\nentry %d to cover a baseline case in the %s run\ngot:\n%+v", i+1, run, entry)
					}
				}
			}
		})
	}
}

func coversBaseline(entry Entry, run RunName, baselines map[string]Recordings) bool {
	for target, baseline := range baselines {
		for id := range baseline {
			if entry.covers(run, target, id) {
				return true
			}
		}
	}
	return false
}
