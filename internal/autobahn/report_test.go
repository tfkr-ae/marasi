package autobahn

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadReport(t *testing.T) {
	t.Run("reads verdicts from the index and clean close from each case report", func(t *testing.T) {
		got, err := LoadReport("testdata/report", "strict")
		if err != nil {
			t.Fatalf("loading report: %v", err)
		}
		want := Report{
			"1.1.1": {Behavior: "OK", BehaviorClose: "UNCLEAN", RemoteCloseCode: nil, WasClean: false},
			"3.7":   {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wanted: %+v\ngot: %+v", want, got)
		}
	})

	t.Run("an agent absent from the index is an error", func(t *testing.T) {
		if _, err := LoadReport("testdata/report", "lenient"); err == nil {
			t.Fatal("wanted an error for a missing agent")
		}
	})
}

func TestBaselineRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "baseline", "fuzzingclient-strict.json")
	want := Report{
		"3.7":   {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		"9.1.1": {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true, Duration: 42},
	}
	if err := WriteBaseline(file, want); err != nil {
		t.Fatalf("writing baseline: %v", err)
	}
	got, err := ReadBaseline(file)
	if err != nil {
		t.Fatalf("reading baseline: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted: %+v\ngot: %+v", want, got)
	}
}
