package conformance

import (
	"strings"
	"testing"
)

func TestWriteTable(t *testing.T) {
	var out strings.Builder
	WriteTable(&out, Outcome{Results: []Result{
		{Suite: "autobahn", Run: RunProxy, Target: "fuzzingclient-strict", Cases: []string{"3.1", "12.1.1"}, Sorted: Sorted{New: []Difference{
			{Case: "3.1", Field: "behavior", Want: "OK", Got: "FAILED"},
			{Case: "12.1.1", Field: FieldMissing},
		}}},
		{Suite: "autobahn", Run: RunProxy, Target: "fuzzingserver-lenient", Cases: []string{"1", "2", "3", "4", "5"}, Notes: []string{"9.x 2400ms (baseline 1800ms)"}},
	}})
	want := strings.Join([]string{
		"RUN    SUITE     TARGET                CASE    FIELD     BASELINE  GOT",
		"proxy  autobahn  fuzzingclient-strict  3.1     behavior  OK        FAILED",
		"proxy  autobahn  fuzzingclient-strict  12.1.1  missing   -         -",
		"",
		"proxy  autobahn  fuzzingclient-strict  2 cases, 0 match, 2 new",
		"proxy  autobahn  fuzzingserver-lenient  5 cases, 5 match, 0 new, 9.x 2400ms (baseline 1800ms)",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, out.String())
	}
}

func TestWriteTableListsTheAllowList(t *testing.T) {
	var out strings.Builder
	WriteTable(&out, Outcome{Results: []Result{{
		Suite: "autobahn", Run: RunCheckpointForward, Target: "fuzzingclient-strict", Cases: []string{"6.4.3", "6.4.4"},
		Sorted: Sorted{OnAllowList: []Match{{Difference: Difference{Case: "6.4.3", Field: "behavior"}, Entry: Entry{Reason: "whole messages"}}}},
	}}})
	if !strings.Contains(out.String(), "checkpoint-forward  autobahn  fuzzingclient-strict  6.4.3          behavior  whole messages") ||
		!strings.Contains(out.String(), "2 cases, 1 match, 0 new, 1 on the allow-list") {
		t.Fatalf("\nwanted:\nthe case on the allow-list and its reason\ngot:\n%s", out.String())
	}
}

func TestWriteTableListsEntriesThatWentAwayOrAreStale(t *testing.T) {
	var out strings.Builder
	WriteTable(&out, Outcome{
		WentAway: []WentAway{{Suite: "h1-origin", Run: RunProxy, Entry: Entry{Cases: "te-*", Field: "connection", Phase: 2}}},
		Stale:    []Stale{{Suite: "h1-origin", Entry: Entry{Cases: "via-*", Field: "via", Kind: KindAccepted}}},
	})
	for _, line := range []string{
		"proxy  h1-origin  went away: known entry te-* connection (phase 2); remove it or run update-known",
		"h1-origin  stale: accepted entry via-* via matched nothing although a run and target it lists ran",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", line, out.String())
		}
	}
}
