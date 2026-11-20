package connectclient

import (
	"reflect"
	"testing"
)

func TestVerdicts(t *testing.T) {
	t.Run("should give each listed case the verdict the runner and the wrapper show", func(t *testing.T) {
		names := []string{"A/ok", "A/bad", "A/hung", "A/never", "A/sideband"}
		events := []Event{
			{Case: "A/ok", Kind: EventSent}, {Case: "A/ok", Kind: EventAnswered},
			{Case: "A/bad", Kind: EventSent}, {Case: "A/bad", Kind: EventAnswered},
			{Case: "A/hung", Kind: EventSent}, {Case: "A/hung", Kind: EventDeadline},
			{Case: "A/sideband", Kind: EventSent}, {Case: "A/sideband", Kind: EventAnswered},
		}
		output := []byte(`Running 5 tests with reference-server for server config #1 {HTTP_VERSION_1, PROTOCOL_CONNECT, TLS:false}...
FAILED: A/bad:
	expected 1 response, got 0
FAILED: A/hung:
	conformance wrapper: no result from the reference client within 10s
FAILED: A/sideband:
	server reported: something: odd

Total cases: 4
1 passed, 3 failed
`)
		want := map[string]Verdict{
			"A/ok": Passed, "A/bad": Failed, "A/hung": TimedOut, "A/never": NotRun, "A/sideband": Failed,
		}
		if got := Verdicts(names, events, output); !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
}
