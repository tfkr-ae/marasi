package autobahn

import (
	"reflect"
	"testing"
)

func code(c int) *int { return &c }

func TestCompare(t *testing.T) {
	baseline := Report{
		"1.1.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
		"3.1":    {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		"7.9.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
		"9.1.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true, Duration: 10},
		"12.1.1": {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
	}

	t.Run("a run that matches the baseline has no deviations", func(t *testing.T) {
		got := Compare(baseline, baseline)
		if len(got) != 0 {
			t.Fatalf("wanted no deviations\ngot: %+v", got)
		}
	})

	t.Run("verdict, close code, and clean close each deviate; timing and 9.x do not", func(t *testing.T) {
		run := Report{
			"1.1.1":  {Behavior: "OK", BehaviorClose: "UNCLEAN", RemoteCloseCode: nil, WasClean: false, Duration: 99},
			"3.1":    {Behavior: "FAILED", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
			"7.9.1":  {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
			"9.1.1":  {Behavior: "FAILED", BehaviorClose: "UNCLEAN", WasClean: false, Duration: 5000},
			"12.1.1": baseline["12.1.1"],
		}
		got := Compare(baseline, run)
		want := []Deviation{
			{Case: "1.1.1", Want: baseline["1.1.1"], Got: run["1.1.1"], Fields: []string{"behaviorClose", "remoteCloseCode", "wasClean"}},
			{Case: "3.1", Want: baseline["3.1"], Got: run["3.1"], Fields: []string{"behavior"}},
			{Case: "7.9.1", Want: baseline["7.9.1"], Got: run["7.9.1"], Fields: []string{"remoteCloseCode"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wanted: %+v\ngot: %+v", want, got)
		}
	})

	t.Run("a case missing from either side deviates", func(t *testing.T) {
		run := Report{
			"1.1.1":  baseline["1.1.1"],
			"3.1":    baseline["3.1"],
			"7.9.1":  baseline["7.9.1"],
			"13.1.1": {Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true},
		}
		got := Compare(baseline, run)
		want := []Deviation{
			{Case: "12.1.1", Want: baseline["12.1.1"], Fields: []string{"missing"}},
			{Case: "13.1.1", Got: run["13.1.1"], Fields: []string{"no baseline"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wanted: %+v\ngot: %+v", want, got)
		}
	})
}

func TestReportSelect(t *testing.T) {
	report := Report{"1.1.1": {}, "1.2.1": {}, "12.1.1": {}, "3.1": {}, "3.2": {}, "7.9.1": {}}

	got := report.Select([]string{"12.*", "1.*", "3.*"}, []string{"3.2"}).Cases()
	want := []string{"1.1.1", "1.2.1", "3.1", "12.1.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted: %v\ngot: %v", want, got)
	}
}
