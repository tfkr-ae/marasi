package h1origin_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1"
	"github.com/tfkr-ae/marasi/internal/conformance/h1origin"
)

// TestCatalogue records every catalogue case directly, which records it
// twice, so a case whose recording depends on timing fails here before it
// reaches a baseline. Cases run in parallel, each with its own origin.
func TestCatalogue(t *testing.T) {
	var _ conformance.Deterministic = h1origin.Suite{}
	ids := map[string]bool{}
	for _, c := range h1origin.Catalogue {
		if ids[c.ID] {
			t.Fatalf("case ID %s is used twice", c.ID)
		}
		ids[c.ID] = true
		t.Run(c.ID, func(t *testing.T) {
			t.Parallel()
			if !strings.HasPrefix(c.RFC, "RFC ") {
				t.Fatalf("wanted the RFC rule the case exercises, got %q", c.RFC)
			}
			recordings := recordDirect(t, c)
			if len(recordings) != 3 {
				t.Fatalf("wanted the case on all three approaches, got %d recordings", len(recordings))
			}
			for id, data := range recordings {
				var recording h1.Recording
				if err := json.Unmarshal(data, &recording); err != nil {
					t.Fatal(err)
				}
				if len(recording.Origin) == 0 || len(recording.Client) == 0 {
					t.Fatalf("%s: wanted both legs recorded, got %+v", id, recording)
				}
				responses := recording.Client[0].Responses
				if len(responses) == 0 {
					t.Fatalf("%s: the client received no response", id)
				}
			}
		})
	}
}

// recordDirect runs the direct run for one case and returns the baseline
// it wrote.
func recordDirect(t *testing.T, c h1origin.Case) conformance.Recordings {
	t.Helper()
	dir := t.TempDir()
	ca, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	settings := conformance.Settings{
		CA:          ca,
		Suites:      []conformance.Suite{h1origin.Suite{Cases: []h1origin.Case{c}}},
		Runs:        []conformance.RunName{conformance.RunDirect},
		Cases:       []string{"*"},
		BaselineDir: filepath.Join(dir, "baselines"),
		OutDir:      filepath.Join(dir, "out"),
		Log:         &strings.Builder{},
	}
	outcome, err := conformance.Run(context.Background(), settings)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Err != nil {
		t.Fatalf("direct run failed: %+v", outcome.Results)
	}
	recordings, err := conformance.ReadBaseline(settings.BaselineFile(h1origin.Name, h1origin.Target))
	if err != nil {
		t.Fatal(err)
	}
	return recordings
}
