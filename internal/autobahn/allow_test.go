package autobahn

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitAllowed(t *testing.T) {
	nonStrict := Deviation{
		Case:   "6.4.3",
		Want:   Result{Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1007), WasClean: true},
		Got:    Result{Behavior: "NON-STRICT", BehaviorClose: "OK", RemoteCloseCode: code(1007), WasClean: true},
		Fields: []string{"behavior"},
	}
	unclean := Deviation{
		Case:   "6.4.4",
		Want:   Result{Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1007), WasClean: true},
		Got:    Result{Behavior: "NON-STRICT", BehaviorClose: "OK", WasClean: false},
		Fields: []string{"behavior", "remoteCloseCode", "wasClean"},
	}
	other := nonStrict
	other.Case = "6.3.1"
	early := nonStrict
	early.Case = "6.4.1"

	tests := []struct {
		name        string
		run         string
		deviations  []Deviation
		wantKept    []Deviation
		wantAllowed []string
	}{
		{name: "should allow a NON-STRICT 6.4.3 in a Checkpoint run", run: RunCheckpointForward, deviations: []Deviation{nonStrict}, wantAllowed: []string{"6.4.3"}},
		{name: "should allow it in the edit run", run: RunCheckpointEdit, deviations: []Deviation{nonStrict}, wantAllowed: []string{"6.4.3"}},
		{name: "should allow it in the extension run", run: RunExtension, deviations: []Deviation{nonStrict}, wantAllowed: []string{"6.4.3"}},
		{name: "should keep it in the untouched run", run: RunProxy, deviations: []Deviation{nonStrict}, wantKept: []Deviation{nonStrict}},
		{name: "should keep a deviation beyond the verdict", run: RunCheckpointForward, deviations: []Deviation{unclean}, wantKept: []Deviation{unclean}},
		{name: "should keep any other case", run: RunCheckpointForward, deviations: []Deviation{other}, wantKept: []Deviation{other}},
		{name: "should allow a NON-STRICT 6.4.1 held across a pause", run: RunCheckpointForward, deviations: []Deviation{early}, wantAllowed: []string{"6.4.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, allowed := SplitAllowed(tt.run, tt.deviations)
			if !reflect.DeepEqual(kept, tt.wantKept) {
				t.Fatalf("\nwanted kept:\n%+v\ngot:\n%+v", tt.wantKept, kept)
			}
			var gotAllowed []string
			for _, a := range allowed {
				if a.Reason == "" {
					t.Fatalf("\nwanted a reason for %s\ngot:\nnone", a.Case)
				}
				gotAllowed = append(gotAllowed, a.Case)
			}
			if !reflect.DeepEqual(gotAllowed, tt.wantAllowed) {
				t.Fatalf("\nwanted allowed:\n%v\ngot:\n%v", tt.wantAllowed, gotAllowed)
			}
		})
	}
}

func TestWriteTableListsAllowedDeviations(t *testing.T) {
	var out strings.Builder
	WriteTable(&out, []SuiteResult{{
		Run: RunCheckpointForward, Mode: ModeFuzzingClient, Peer: PeerStrict, Cases: 2,
		Allowed: []AllowedDeviation{{Deviation: Deviation{Case: "6.4.3"}, Reason: "held messages are not streamed"}},
	}})
	if !strings.Contains(out.String(), "checkpoint-forward  fuzzingclient/strict  6.4.3    held messages are not streamed") ||
		!strings.Contains(out.String(), "2 cases, 0 deviations, 1 allowed") {
		t.Fatalf("\nwanted the allowed case and its reason\ngot:\n%s", out.String())
	}
}
