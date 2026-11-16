package autobahn

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

func TestLoadReport(t *testing.T) {
	t.Run("should read verdicts from the index and clean close from each case report", func(t *testing.T) {
		got, err := LoadReport("testdata/report", "strict")
		if err != nil {
			t.Fatalf("loading report: %v", err)
		}
		want := Report{
			"1.1.1": {Behavior: "OK", BehaviorClose: "UNCLEAN", RemoteCloseCode: nil, WasClean: false},
			"3.7":   {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should fail for an agent absent from the index", func(t *testing.T) {
		if _, err := LoadReport("testdata/report", "lenient"); err == nil {
			t.Fatal("\nwanted:\nan error for a missing agent\ngot:\nnone")
		}
	})
}

func TestBaselineRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "baseline", "fuzzingclient-strict.json")
	want := mustRecordings(t, Report{
		"3.7":   {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		"9.1.1": {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true, Duration: 42},
	})
	if err := conformance.WriteBaseline(file, want); err != nil {
		t.Fatalf("writing baseline: %v", err)
	}
	got, err := conformance.ReadBaseline(file)
	if err != nil {
		t.Fatalf("reading baseline: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
	}
}

// TestCommittedBaselinesRoundTrip pins the committed baselines' format: the
// harness rewrites a file it merges into byte for byte, so a direct run
// changes only the cases it ran.
func TestCommittedBaselinesRoundTrip(t *testing.T) {
	files, err := filepath.Glob("../baselines/autobahn/fuzzing*.json")
	if err != nil || len(files) != 4 {
		t.Fatalf("\nwanted:\nfour committed baselines\ngot:\n%v %v", files, err)
	}
	for _, file := range files {
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		recordings, err := conformance.ReadBaseline(file)
		if err != nil {
			t.Fatal(err)
		}
		copied := filepath.Join(t.TempDir(), filepath.Base(file))
		if err := conformance.WriteBaseline(copied, recordings); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(copied)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("\nwanted %s to round-trip unchanged\ngot:\na different file", file)
		}
	}
}
