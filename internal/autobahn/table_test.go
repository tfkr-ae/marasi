package autobahn

import (
	"strings"
	"testing"
)

func TestWriteTable(t *testing.T) {
	var out strings.Builder
	WriteTable(&out, []SuiteResult{
		{Run: "proxy", Mode: ModeFuzzingClient, Peer: PeerStrict, Cases: 2, Deviations: []Deviation{
			{
				Case:   "3.1",
				Want:   Result{Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1002), WasClean: true},
				Got:    Result{Behavior: "FAILED", BehaviorClose: "FAILED"},
				Fields: []string{"behavior", "behaviorClose", "remoteCloseCode", "wasClean"},
			},
			{Case: "12.1.1", Want: Result{Behavior: "OK", BehaviorClose: "OK", RemoteCloseCode: code(1000), WasClean: true}, Fields: []string{"missing"}},
		}},
		{Run: "proxy", Mode: ModeFuzzingServer, Peer: PeerLenient, Cases: 5, Timing: Timing{Run: 2400, Baseline: 1800}},
	})
	want := strings.Join([]string{
		"RUN    SUITE                 CASE    DIFFERS                                          BASELINE          GOT",
		"proxy  fuzzingclient/strict  3.1     behavior,behaviorClose,remoteCloseCode,wasClean  OK OK 1002 clean  FAILED FAILED - unclean",
		"proxy  fuzzingclient/strict  12.1.1  missing                                          OK OK 1000 clean  -",
		"",
		"proxy  fuzzingclient/strict  2 cases, 2 deviations",
		"proxy  fuzzingserver/lenient  5 cases, 0 deviations, 9.x 2400ms (baseline 1800ms)",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("wanted:\n%s\ngot:\n%s", want, out.String())
	}
}
