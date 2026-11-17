package autobahn

import (
	"reflect"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// TestAllowList checks the committed deviations file accepts exactly the
// 6.4.1–6.4.4 allowance the Go allow-list used to hold.
func TestAllowList(t *testing.T) {
	deviations, err := conformance.ReadDeviations("../baselines/autobahn/deviations.json")
	if err != nil {
		t.Fatalf("reading deviations: %v", err)
	}
	want := Result{Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1007), WasClean: true}
	nonStrict := Result{Behavior: "NON-STRICT", BehaviorClose: "OK", RemoteCloseCode: code(1007), WasClean: true}
	unclean := Result{Behavior: "NON-STRICT", BehaviorClose: "OK", WasClean: false}
	failed := Result{Behavior: "FAILED", BehaviorClose: "OK", RemoteCloseCode: code(1007), WasClean: true}

	tests := []struct {
		name            string
		run             conformance.RunName
		id              string
		got             Result
		wantNew         []string // fields
		wantOnAllowList []string // fields
	}{
		{name: "should allow a NON-STRICT 6.4.3 in a Checkpoint run", run: conformance.RunCheckpointForward, id: "6.4.3", got: nonStrict, wantOnAllowList: []string{"behavior"}},
		{name: "should allow it in the edit run", run: conformance.RunCheckpointEdit, id: "6.4.3", got: nonStrict, wantOnAllowList: []string{"behavior"}},
		{name: "should allow it in the extension run", run: conformance.RunExtension, id: "6.4.3", got: nonStrict, wantOnAllowList: []string{"behavior"}},
		{name: "should allow it in the untouched run", run: conformance.RunProxy, id: "6.4.3", got: nonStrict, wantOnAllowList: []string{"behavior"}},
		{name: "should keep it in the direct run", run: conformance.RunDirect, id: "6.4.3", got: nonStrict, wantNew: []string{"behavior"}},
		{name: "should keep a deviation beyond the verdict", run: conformance.RunCheckpointForward, id: "6.4.4", got: unclean, wantNew: []string{"remoteCloseCode", "wasClean"}, wantOnAllowList: []string{"behavior"}},
		{name: "should keep any other case", run: conformance.RunCheckpointForward, id: "6.3.1", got: nonStrict, wantNew: []string{"behavior"}},
		{name: "should allow a NON-STRICT 6.4.1", run: conformance.RunCheckpointForward, id: "6.4.1", got: nonStrict, wantOnAllowList: []string{"behavior"}},
		{name: "should keep a 6.4.1 that failed", run: conformance.RunProxy, id: "6.4.1", got: failed, wantNew: []string{"behavior"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comparison := compare(t, Report{tt.id: want}, Report{tt.id: tt.got})
			sorted := deviations.Sort(tt.run, "fuzzingclient-strict", comparison.Differences)
			var gotNew, gotOnAllowList []string
			for _, d := range sorted.New {
				gotNew = append(gotNew, d.Field)
			}
			for _, m := range sorted.OnAllowList {
				if m.Entry.Reason == "" {
					t.Fatalf("\nwanted a reason for %s\ngot:\nnone", m.Case)
				}
				gotOnAllowList = append(gotOnAllowList, m.Field)
			}
			if !reflect.DeepEqual(gotNew, tt.wantNew) || !reflect.DeepEqual(gotOnAllowList, tt.wantOnAllowList) {
				t.Fatalf("\nwanted new %v, on the allow-list %v\ngot:\nnew %v, on the allow-list %v", tt.wantNew, tt.wantOnAllowList, gotNew, gotOnAllowList)
			}
		})
	}
}
