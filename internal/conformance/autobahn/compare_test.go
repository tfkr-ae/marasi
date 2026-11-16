package autobahn

import (
	"reflect"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

func code(c int) *int { return &c }

func mustRecordings(t *testing.T, report Report) conformance.Recordings {
	t.Helper()
	recordings, err := report.Recordings()
	if err != nil {
		t.Fatal(err)
	}
	return recordings
}

func compare(t *testing.T, baseline, run Report) conformance.Comparison {
	t.Helper()
	comparison, err := Compare(mustRecordings(t, baseline), mustRecordings(t, run))
	if err != nil {
		t.Fatalf("comparing: %v", err)
	}
	return comparison
}

func TestCompare(t *testing.T) {
	baseline := Report{
		"1.1.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
		"3.1":    {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		"7.9.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		"9.1.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true, Duration: 10},
		"12.1.1": {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
	}

	t.Run("should find no differences in a run that matches the baseline", func(t *testing.T) {
		got := compare(t, baseline, baseline)
		if len(got.Differences) != 0 {
			t.Fatalf("\nwanted:\nno differences\ngot:\n%+v", got.Differences)
		}
		want := []string{"1.1.1", "3.1", "7.9.1", "12.1.1"}
		if !reflect.DeepEqual(got.Cases, want) {
			t.Fatalf("\nwanted scored cases:\n%v\ngot:\n%v", want, got.Cases)
		}
	})

	t.Run("should report verdict, close code and clean close but not timing or 9.x", func(t *testing.T) {
		run := Report{
			"1.1.1":  {Behavior: "OK", BehaviorClose: "UNCLEAN", RemoteCloseCode: nil, WasClean: false, Duration: 99},
			"3.1":    {Behavior: "FAILED", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
			"7.9.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
			"9.1.1":  {Behavior: "FAILED", BehaviorClose: "UNCLEAN", WasClean: false, Duration: 5000},
			"12.1.1": baseline["12.1.1"],
		}
		got := compare(t, baseline, run)
		want := []conformance.Difference{
			{Case: "1.1.1", Field: "behaviorClose", Want: "OK", Got: "UNCLEAN"},
			{Case: "1.1.1", Field: "remoteCloseCode", Want: "1000", Got: "-"},
			{Case: "1.1.1", Field: "wasClean", Want: "true", Got: "false"},
			{Case: "3.1", Field: "behavior", Want: "OK", Got: "FAILED"},
			{Case: "7.9.1", Field: "remoteCloseCode", Want: "1002", Got: "1000"},
		}
		if !reflect.DeepEqual(got.Differences, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got.Differences)
		}
		if wantNotes := []string{"9.x 5000ms (baseline 10ms)"}; !reflect.DeepEqual(got.Notes, wantNotes) {
			t.Fatalf("\nwanted notes:\n%v\ngot:\n%v", wantNotes, got.Notes)
		}
	})

	t.Run("should report a case missing from either side", func(t *testing.T) {
		run := Report{
			"1.1.1":  baseline["1.1.1"],
			"3.1":    baseline["3.1"],
			"7.9.1":  baseline["7.9.1"],
			"13.1.1": {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
		}
		got := compare(t, baseline, run)
		want := []conformance.Difference{
			{Case: "12.1.1", Field: conformance.FieldMissing},
			{Case: "13.1.1", Field: conformance.FieldNoBaseline},
		}
		if !reflect.DeepEqual(got.Differences, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got.Differences)
		}
	})
}

func TestReportSelect(t *testing.T) {
	report := Report{"1.1.1": {}, "1.2.1": {}, "12.1.1": {}, "3.1": {}, "3.2": {}, "7.9.1": {}}

	selected := mustRecordings(t, report).Select([]string{"12.*", "1.*", "3.*"}, []string{"3.2"}, nil)
	var got []string
	for id := range selected {
		got = append(got, id)
	}
	SortCases(got)
	want := []string{"1.1.1", "1.2.1", "3.1", "12.1.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
	}
}
