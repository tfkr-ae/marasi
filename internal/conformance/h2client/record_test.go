package h2client

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

func TestCompare(t *testing.T) {
	t.Run("should report each field that differs from the baseline", func(t *testing.T) {
		baseline := conformance.Recordings{
			"http2-4.2-2@connect-tls": json.RawMessage(`{"verdict":"failed","end":"closed"}`),
			"generic-1-1@connect-tls": json.RawMessage(`{"verdict":"passed","end":"closed"}`),
		}
		got := conformance.Recordings{
			"http2-4.2-2@connect-tls": json.RawMessage(`{"verdict":"not-run","error":"Protocol negotiation failed","end":"closed"}`),
			"generic-1-1@connect-tls": json.RawMessage(`{"verdict":"passed","end":"closed"}`),
		}
		comparison, err := Suite{}.Compare(Target, baseline, got)
		if err != nil {
			t.Fatal(err)
		}
		want := []conformance.Difference{
			{Case: "http2-4.2-2@connect-tls", Field: "verdict", Want: "failed", Got: "not-run"},
			{Case: "http2-4.2-2@connect-tls", Field: "error", Want: "", Got: "Protocol negotiation failed"},
		}
		if !reflect.DeepEqual(comparison.Differences, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, comparison.Differences)
		}
		if len(comparison.Cases) != 2 {
			t.Fatalf("wanted 2 cases, got %v", comparison.Cases)
		}
	})
}

func TestRecord(t *testing.T) {
	test := Test{Group: "http2/4.2", Seq: 2, Desc: "Sends a large size DATA frame that exceeds the SETTINGS_MAX_FRAME_SIZE"}

	t.Run("should record the report's verdict and how the shim saw the connection end", func(t *testing.T) {
		results := []Result{{Group: "http2/4.2", Desc: test.Desc, Verdict: Failed}}
		got, err := record(test, results, "", []shim.Capture{{End: shim.EndClosed}})
		if err != nil {
			t.Fatal(err)
		}
		if want := (Recording{Verdict: Failed, End: shim.EndClosed}); got != want {
			t.Fatalf("wanted %+v, got %+v", want, got)
		}
	})

	t.Run("should record h2spec's error when it wrote no report", func(t *testing.T) {
		output := "  4.2. Frame Size\n    × 2: Sends a large size DATA frame\n\nError: Protocol negotiation failed\n"
		got, err := record(test, nil, output, []shim.Capture{{End: shim.EndClosed}})
		if err != nil {
			t.Fatal(err)
		}
		if want := (Recording{Verdict: NotRun, Error: "Protocol negotiation failed", End: shim.EndClosed}); got != want {
			t.Fatalf("wanted %+v, got %+v", want, got)
		}
	})

	t.Run("should refuse a report of another case", func(t *testing.T) {
		results := []Result{{Group: "http2/4.2", Desc: "Sends a DATA frame with 2^14 octets in length", Verdict: Passed}}
		if _, err := record(test, results, "", []shim.Capture{{End: shim.EndClosed}}); err == nil {
			t.Fatal("wanted an error")
		}
	})

	t.Run("should refuse a case that opened no connection", func(t *testing.T) {
		results := []Result{{Group: "http2/4.2", Desc: test.Desc, Verdict: Passed}}
		if _, err := record(test, results, "", nil); err == nil {
			t.Fatal("wanted an error for no connection")
		}
	})

	t.Run("should record the end of the case's first connection, not of the one h2spec opens to measure the response", func(t *testing.T) {
		results := []Result{{Group: "http2/4.2", Desc: test.Desc, Verdict: Passed}}
		got, err := record(test, results, "", []shim.Capture{{End: shim.EndClosed}, {End: shim.EndOpen}})
		if err != nil {
			t.Fatal(err)
		}
		if want := (Recording{Verdict: Passed, End: shim.EndClosed}); got != want {
			t.Fatalf("wanted %+v, got %+v", want, got)
		}
	})

	t.Run("should refuse a case whose connection the shim could not take to the target", func(t *testing.T) {
		results := []Result{{Group: "http2/4.2", Desc: test.Desc, Verdict: Failed}}
		if _, err := record(test, results, "", []shim.Capture{{End: shim.EndClosed, Err: "dialing 127.0.0.1:1: connection refused"}}); err == nil {
			t.Fatal("wanted an error")
		}
	})
}
