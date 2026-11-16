package h1origin_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1origin"
)

// trailerCase sends mixed-case request fields and gets mixed-case response
// fields and two trailers. The test's own table keeps the stand-ins'
// expectations fixed as the real catalogue grows.
var trailerCase = h1origin.Case{
	ID:  "chunked-trailers",
	RFC: "RFC 9112 §7.1.2 chunked trailer section",
	Requests: []string{
		"GET /chunked HTTP/1.1\r\nHost: {authority}\r\nX-Request-ID: abc\r\nx-lower-case: v\r\n\r\n",
	},
	Script: []h1origin.Step{
		h1origin.Await(),
		h1origin.Send("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nX-ORIGIN-case: Mixed\r\nx-lower: yes\r\n" +
			"Transfer-Encoding: chunked\r\nTrailer: X-Checksum, x-trailer-two\r\n\r\n" +
			"5\r\nhello\r\n0\r\nX-Checksum: abc\r\nx-trailer-two: 2\r\n\r\n"),
	},
}

type harness struct {
	settings conformance.Settings
	silent   *bool // the next stand-in reports no traffic
}

// newHarness records the direct baseline and returns settings for a proxy
// run that launches a fresh stand-in each time, as a run launches Marasi.
func newHarness(t *testing.T, broken bool, deviations conformance.Deviations) harness {
	t.Helper()
	dir := t.TempDir()
	silent := new(bool)
	settings := conformance.Settings{
		Suites:      []conformance.Suite{h1origin.Suite{Cases: []h1origin.Case{trailerCase}}},
		Cases:       []string{"*"},
		BaselineDir: filepath.Join(dir, "baselines"),
		OutDir:      filepath.Join(dir, "out"),
		Launch: func(context.Context, conformance.Suite, conformance.RunName, string) (conformance.Proxy, error) {
			proxy := startStandIn(t, broken)
			proxy.silent = *silent
			return proxy, nil
		},
		Log: &strings.Builder{},
	}
	if err := conformance.WriteDeviations(settings.DeviationsFile(h1origin.Name), deviations); err != nil {
		t.Fatal(err)
	}
	settings.Runs = []conformance.RunName{conformance.RunDirect}
	outcome := runHarness(t, settings)
	if outcome.Failed() {
		t.Fatalf("direct run failed: %+v", outcome.Results)
	}
	settings.Runs = []conformance.RunName{conformance.RunProxy}
	return harness{settings: settings, silent: silent}
}

func runHarness(t *testing.T, settings conformance.Settings) conformance.Outcome {
	t.Helper()
	outcome, err := conformance.Run(context.Background(), settings)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	return outcome
}

func onlyResult(t *testing.T, outcome conformance.Outcome) conformance.Result {
	t.Helper()
	if len(outcome.Results) != 1 || outcome.Results[0].Err != nil {
		t.Fatalf("\nwanted one result with no error\ngot:\n%+v", outcome.Results)
	}
	return outcome.Results[0]
}

func TestTransparentRelay(t *testing.T) {
	t.Run("should score every case on every approach as matching", func(t *testing.T) {
		h := newHarness(t, false, nil)
		outcome := runHarness(t, h.settings)

		result := onlyResult(t, outcome)
		wantCases := []string{"chunked-trailers@absolute", "chunked-trailers@connect-plain"}
		if !reflect.DeepEqual(result.Cases, wantCases) {
			t.Fatalf("\nwanted cases:\n%v\ngot:\n%v", wantCases, result.Cases)
		}
		if result.Matched() != 2 || len(result.New)+len(result.Known)+len(result.Allowed) != 0 {
			t.Fatalf("\nwanted 2 matched and no differences\ngot %d matched:\n%+v", result.Matched(), result.Sorted)
		}
		if outcome.Failed() {
			t.Fatal("wanted the gate to pass")
		}
	})
}

func TestEveryProxiedRun(t *testing.T) {
	t.Run("should score a transparent relay as matching in all four proxied runs", func(t *testing.T) {
		h := newHarness(t, false, nil)
		h.settings.Runs = conformance.ProxiedRuns
		outcome := runHarness(t, h.settings)

		var runs []conformance.RunName
		for _, result := range outcome.Results {
			if result.Err != nil || result.Matched() != 2 {
				t.Fatalf("\nwanted 2 matched and no error in %s\ngot:\n%+v", result.Run, result)
			}
			runs = append(runs, result.Run)
		}
		want := []conformance.RunName{conformance.RunProxy, conformance.RunCheckpointForward, conformance.RunCheckpointEdit, conformance.RunExtension}
		if !reflect.DeepEqual(runs, want) {
			t.Fatalf("\nwanted runs:\n%v\ngot:\n%v", want, runs)
		}
	})
}

// Every difference the broken stand-in makes, in case order.
var (
	responseCase = func(id string) conformance.Difference {
		return conformance.Difference{Case: id, Field: "client[0].responses[0].fields.name-case",
			Want: "X-ORIGIN-case, x-lower", Got: "X-Origin-Case, X-Lower"}
	}
	swallowed = func(id string) conformance.Difference {
		return conformance.Difference{Case: id, Field: "client[0].responses[0].trailers", Want: "X-Checksum: abc"}
	}
	requestCase = func(id string) conformance.Difference {
		return conformance.Difference{Case: id, Field: "origin[0].requests[0].fields.name-case",
			Want: "X-Request-ID, x-lower-case", Got: "X-Request-Id, X-Lower-Case"}
	}
)

const (
	absolute     = "chunked-trailers@absolute"
	connectPlain = "chunked-trailers@connect-plain"
)

var (
	knownResponseCase = conformance.Entry{Cases: "chunked-trailers@*", Runs: []conformance.RunName{conformance.RunProxy},
		Field: "client[0].responses[0].fields.name-case", Kind: conformance.KindKnown, Reason: "Martian canonicalises", Phase: 2}
	acceptedRequestCase = conformance.Entry{Cases: absolute, Runs: []conformance.RunName{conformance.RunProxy},
		Field: "origin[0].requests[0].fields.name-case", Kind: conformance.KindAccepted, Reason: "test allow-list entry"}
	knownGone = conformance.Entry{Cases: "*", Runs: []conformance.RunName{conformance.RunProxy},
		Field: "client[0].responses[0].body", Kind: conformance.KindKnown, Reason: "never happens", Phase: 2}
)

func TestBrokenStandIn(t *testing.T) {
	t.Run("should report exactly the differences it makes, sorted against deviations.json", func(t *testing.T) {
		h := newHarness(t, true, conformance.Deviations{knownResponseCase, acceptedRequestCase, knownGone})
		outcome := runHarness(t, h.settings)

		result := onlyResult(t, outcome)
		wantNew := []conformance.Difference{swallowed(absolute), swallowed(connectPlain), requestCase(connectPlain)}
		if !reflect.DeepEqual(result.New, wantNew) {
			t.Fatalf("\nwanted new:\n%+v\ngot:\n%+v", wantNew, result.New)
		}
		wantKnown := []conformance.Difference{responseCase(absolute), responseCase(connectPlain)}
		if got := differences(result.Known); !reflect.DeepEqual(got, wantKnown) {
			t.Fatalf("\nwanted known:\n%+v\ngot:\n%+v", wantKnown, got)
		}
		wantAllowed := []conformance.Difference{requestCase(absolute)}
		if got := differences(result.Allowed); !reflect.DeepEqual(got, wantAllowed) {
			t.Fatalf("\nwanted allowed:\n%+v\ngot:\n%+v", wantAllowed, got)
		}
		if len(outcome.WentAway) != 1 || outcome.WentAway[0].Entry.Field != knownGone.Field {
			t.Fatalf("\nwanted the body entry to have gone away\ngot:\n%+v", outcome.WentAway)
		}
		if !outcome.Failed() {
			t.Fatal("wanted the gate to fail")
		}
	})

	t.Run("should write the summary and a diff with both raw captures for each failing case", func(t *testing.T) {
		h := newHarness(t, true, nil)
		runHarness(t, h.settings)

		dir := filepath.Join(h.settings.OutDir, h1origin.Name, string(conformance.RunProxy))
		summary, err := os.ReadFile(filepath.Join(dir, "summary.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(summary), "client[0].responses[0].trailers") {
			t.Fatalf("wanted the swallowed trailer in the summary\ngot:\n%s", summary)
		}
		data, err := os.ReadFile(filepath.Join(dir, "diffs", "fixtures", connectPlain+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var diff struct {
			Baseline, Got struct {
				Client []struct {
					Raw struct {
						ToClient []byte `json:"to_client"`
					} `json:"raw"`
				} `json:"client"`
			}
		}
		if err := json.Unmarshal(data, &diff); err != nil {
			t.Fatal(err)
		}
		if len(diff.Baseline.Client) != 1 || len(diff.Got.Client) != 1 ||
			!strings.Contains(string(diff.Baseline.Client[0].Raw.ToClient), "X-Checksum: abc") ||
			strings.Contains(string(diff.Got.Client[0].Raw.ToClient), "X-Checksum: abc") {
			t.Fatalf("wanted both raw captures, the trailer only in the baseline\ngot:\n%s", data)
		}
	})

	t.Run("should list nothing new after update-known, keeping accepted entries", func(t *testing.T) {
		h := newHarness(t, true, conformance.Deviations{acceptedRequestCase})
		h.settings.UpdateKnown, h.settings.Phase = true, 2
		runHarness(t, h.settings)
		h.settings.UpdateKnown = false

		result := onlyResult(t, runHarness(t, h.settings))
		if len(result.New) != 0 || len(result.Allowed) != 1 || len(result.Known) != 5 {
			t.Fatalf("\nwanted 5 known, 1 allowed, none new\ngot:\n%+v", result.Sorted)
		}
		deviations, err := conformance.ReadDeviations(h.settings.DeviationsFile(h1origin.Name))
		if err != nil {
			t.Fatal(err)
		}
		if len(deviations) != 6 || !reflect.DeepEqual(deviations[0], acceptedRequestCase) {
			t.Fatalf("\nwanted the accepted entry first and unchanged\ngot:\n%+v", deviations[0])
		}
	})
}

func TestNoTrafficThroughProxy(t *testing.T) {
	t.Run("should fail a proxied run that sent nothing through the proxy", func(t *testing.T) {
		h := newHarness(t, false, nil)
		*h.silent = true

		result := runHarness(t, h.settings).Results[0]
		if !errors.Is(result.Err, conformance.ErrNotProxied) {
			t.Fatalf("\nwanted %v\ngot: %v", conformance.ErrNotProxied, result.Err)
		}
	})
}

func differences(matches []conformance.Match) []conformance.Difference {
	var ds []conformance.Difference
	for _, m := range matches {
		ds = append(ds, m.Difference)
	}
	return ds
}
