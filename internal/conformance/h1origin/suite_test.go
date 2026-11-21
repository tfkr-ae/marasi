package h1origin_test

import (
	"context"
	"crypto/x509"
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

// The leg-rule cases. The broken stand-in adds Via to each, forwards the
// fields Connection names on /hop, re-frames the chunked body to a matching
// Content-Length on /reframe, and sends Content-Length with chunked on /cl-te.
var (
	hopCase = h1origin.Case{
		ID:  "hop-by-hop",
		RFC: "RFC 9110 §7.6.1 Connection",
		Requests: []string{
			"GET /hop HTTP/1.1\r\nHost: {authority}\r\nConnection: X-Hop\r\nX-Hop: 1\r\n\r\n",
		},
		Script: []h1origin.Step{
			h1origin.Await(),
			h1origin.Send("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: X-Back\r\nX-Back: 1\r\n\r\nok"),
		},
	}
	reframeCase = h1origin.Case{
		ID:       "reframe",
		RFC:      "RFC 9112 §6 framing",
		Requests: []string{"GET /reframe HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []h1origin.Step{
			h1origin.Await(),
			h1origin.Send("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n7\r\n, world\r\n0\r\n\r\n"),
		},
	}
	clteCase = h1origin.Case{
		ID:       "cl-te",
		RFC:      "RFC 9112 §6.1 Content-Length with Transfer-Encoding",
		Requests: []string{"GET /cl-te HTTP/1.1\r\nHost: {authority}\r\n\r\n"},
		Script: []h1origin.Step{
			h1origin.Await(),
			h1origin.Send("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n"),
		},
	}
	acceptVia = conformance.Entry{Cases: "*", Runs: []conformance.RunName{conformance.RunProxy}, Field: "leg:via",
		Kind: conformance.KindAccepted, Reason: "a testing tool does not announce itself to the origin", RFC: "RFC 9110 §7.6.3"}
)

type harness struct {
	settings conformance.Settings
	silent   *bool // the next stand-in reports no traffic
}

// newHarness records the direct baseline and returns settings for a proxy
// run that launches a fresh stand-in each time, as a run launches Marasi.
// cases default to trailerCase alone.
func newHarness(t *testing.T, broken bool, deviations conformance.Deviations, cases ...h1origin.Case) harness {
	t.Helper()
	if len(cases) == 0 {
		cases = []h1origin.Case{trailerCase}
	}
	dir := t.TempDir()
	silent := new(bool)
	ca, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	settings := conformance.Settings{
		CA:          ca,
		Suites:      []conformance.Suite{h1origin.Suite{Cases: cases}},
		Cases:       []string{"*"},
		BaselineDir: filepath.Join(dir, "baselines"),
		OutDir:      filepath.Join(dir, "out"),
		Launch: func(context.Context, conformance.Suite, conformance.RunName, string) (conformance.Proxy, error) {
			proxy := startStandIn(t, broken, ca)
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
	t.Run("should score every case on every approach as matching, with every leg rule but Via passing", func(t *testing.T) {
		h := newHarness(t, false, conformance.Deviations{acceptVia}, trailerCase, hopCase, reframeCase, clteCase)
		outcome := runHarness(t, h.settings)

		result := onlyResult(t, outcome)
		if len(result.Cases) != 12 || result.Matched() != 12 || len(result.New)+len(result.Known) != 0 {
			t.Fatalf("\nwanted 12 of 12 matched and nothing new or known\ngot %d of %d:\n%+v", result.Matched(), len(result.Cases), result.Sorted)
		}
		for _, m := range result.OnAllowList {
			if m.Field != "leg:via" {
				t.Fatalf("wanted only Via on the allow-list\ngot:\n%+v", m)
			}
		}
		want := map[string][]string{}
		for _, c := range []string{"chunked-trailers", "hop-by-hop", "reframe", "cl-te"} {
			for _, a := range []string{"absolute", "connect-plain", "connect-tls"} {
				want[c+"@"+a] = []string{"leg:via"}
			}
		}
		if got := brokenRules(result); !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted only Via broken\ngot:\n%v", got)
		}
		if outcome.Failed() {
			t.Fatal("wanted the gate to pass")
		}
	})
}

// brokenRules maps each case to the leg rules broken on it, and fails
// unless every rule was checked on every case.
func brokenRules(result conformance.Result) map[string][]string {
	broken := map[string][]string{}
	for _, check := range result.LegChecks {
		if _, ok := broken[check.Case]; !ok {
			broken[check.Case] = []string{}
		}
		if check.Broken {
			broken[check.Case] = append(broken[check.Case], check.Rule.ID)
		}
	}
	return broken
}

func TestEveryProxiedRun(t *testing.T) {
	t.Run("should score a transparent relay as matching in all four proxied runs, on every approach and leg rule", func(t *testing.T) {
		viaEverywhere := acceptVia
		viaEverywhere.Runs = conformance.ProxiedRuns
		h := newHarness(t, false, conformance.Deviations{viaEverywhere}, trailerCase, hopCase, reframeCase, clteCase)
		h.settings.Runs = conformance.ProxiedRuns
		outcome := runHarness(t, h.settings)

		var runs []conformance.RunName
		for _, result := range outcome.Results {
			if result.Err != nil || len(result.Cases) != 12 || result.Matched() != 12 || len(result.New)+len(result.Known) != 0 {
				t.Fatalf("\nwanted 12 of 12 matched, nothing new or known, and no error in %s\ngot:\n%+v", result.Run, result)
			}
			for c, rules := range brokenRules(result) {
				if !reflect.DeepEqual(rules, []string{"leg:via"}) {
					t.Fatalf("\nwanted only Via broken on %s in %s\ngot: %v", c, result.Run, rules)
				}
			}
			runs = append(runs, result.Run)
		}
		want := []conformance.RunName{conformance.RunProxy, conformance.RunCheckpointForward, conformance.RunCheckpointEdit, conformance.RunExtension}
		if !reflect.DeepEqual(runs, want) {
			t.Fatalf("\nwanted runs:\n%v\ngot:\n%v", want, runs)
		}
		if outcome.Failed() {
			t.Fatal("wanted the gate to pass")
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
	via = func(id string) conformance.Difference {
		return conformance.Difference{Case: id, Field: "leg:via", Want: "send Via", Level: conformance.LevelMust,
			Got: "origin[0].requests[0]: no Via; client[0].responses[0]: no Via"}
	}
	requestCase = func(id string) conformance.Difference {
		return conformance.Difference{Case: id, Field: "origin[0].requests[0].fields.name-case",
			Want: "X-Request-ID, x-lower-case", Got: "X-Request-Id, X-Lower-Case"}
	}
)

const (
	absolute     = "chunked-trailers@absolute"
	connectPlain = "chunked-trailers@connect-plain"
	connectTLS   = "chunked-trailers@connect-tls"
)

var (
	knownResponseCase = conformance.Entry{Cases: "chunked-trailers@*", Runs: []conformance.RunName{conformance.RunProxy},
		Field: "client[0].responses[0].fields.name-case", Kind: conformance.KindKnown, Reason: "Martian canonicalises"}
	acceptedRequestCase = conformance.Entry{Cases: absolute, Runs: []conformance.RunName{conformance.RunProxy},
		Field: "origin[0].requests[0].fields.name-case", Kind: conformance.KindAccepted, Reason: "test allow-list entry"}
	knownGone = conformance.Entry{Cases: "*", Runs: []conformance.RunName{conformance.RunProxy},
		Field: "client[0].responses[0].body", Kind: conformance.KindKnown, Reason: "never happens"}
)

func TestBrokenStandIn(t *testing.T) {
	t.Run("should report exactly the differences it makes, sorted against deviations.json", func(t *testing.T) {
		h := newHarness(t, true, conformance.Deviations{knownResponseCase, acceptedRequestCase, knownGone, acceptVia})
		outcome := runHarness(t, h.settings)

		result := onlyResult(t, outcome)
		wantNew := []conformance.Difference{swallowed(absolute), swallowed(connectPlain), requestCase(connectPlain),
			swallowed(connectTLS), requestCase(connectTLS)}
		if !reflect.DeepEqual(result.New, wantNew) {
			t.Fatalf("\nwanted new:\n%+v\ngot:\n%+v", wantNew, result.New)
		}
		wantKnown := []conformance.Difference{responseCase(absolute), responseCase(connectPlain), responseCase(connectTLS)}
		if got := differences(result.Known); !reflect.DeepEqual(got, wantKnown) {
			t.Fatalf("\nwanted known:\n%+v\ngot:\n%+v", wantKnown, got)
		}
		wantOnAllowList := []conformance.Difference{requestCase(absolute), via(absolute), via(connectPlain), via(connectTLS)}
		if got := differences(result.OnAllowList); !reflect.DeepEqual(got, wantOnAllowList) {
			t.Fatalf("\nwanted on the allow-list:\n%+v\ngot:\n%+v", wantOnAllowList, got)
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
		h := newHarness(t, true, conformance.Deviations{acceptedRequestCase, acceptVia})
		h.settings.UpdateKnown = true
		runHarness(t, h.settings)
		h.settings.UpdateKnown = false

		result := onlyResult(t, runHarness(t, h.settings))
		if len(result.New) != 0 || len(result.OnAllowList) != 4 || len(result.Known) != 8 {
			t.Fatalf("\nwanted 8 known, 4 on the allow-list, none new\ngot:\n%+v", result.Sorted)
		}
		deviations, err := conformance.ReadDeviations(h.settings.DeviationsFile(h1origin.Name))
		if err != nil {
			t.Fatal(err)
		}
		if len(deviations) != 10 || !reflect.DeepEqual(deviations[0], acceptedRequestCase) {
			t.Fatalf("\nwanted the accepted entry first and unchanged\ngot:\n%+v", deviations[0])
		}
	})
}

func TestBrokenStandInLegRules(t *testing.T) {
	t.Run("should break each leg rule the stand-in breaks and pass a matching re-frame", func(t *testing.T) {
		h := newHarness(t, true, nil, hopCase, reframeCase, clteCase)
		outcome := runHarness(t, h.settings)

		result := onlyResult(t, outcome)
		want := map[string][]string{
			"hop-by-hop@absolute": {"leg:connection"}, "hop-by-hop@connect-plain": {"leg:connection"}, "hop-by-hop@connect-tls": {"leg:connection"},
			"reframe@absolute": {}, "reframe@connect-plain": {}, "reframe@connect-tls": {},
			"cl-te@absolute": {"leg:cl-and-te"}, "cl-te@connect-plain": {"leg:cl-and-te"}, "cl-te@connect-tls": {"leg:cl-and-te"},
		}
		if got := brokenRules(result); !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted broken:\n%v\ngot:\n%v", want, got)
		}
		var newRules []string
		for _, d := range result.New {
			if d.Level != "" {
				newRules = append(newRules, d.Case+" "+d.Field)
			}
		}
		wantNew := []string{"hop-by-hop@absolute leg:connection", "hop-by-hop@connect-plain leg:connection",
			"hop-by-hop@connect-tls leg:connection", "cl-te@absolute leg:cl-and-te", "cl-te@connect-plain leg:cl-and-te",
			"cl-te@connect-tls leg:cl-and-te"}
		if !reflect.DeepEqual(newRules, wantNew) {
			t.Fatalf("\nwanted new leg-rule failures:\n%v\ngot:\n%v", wantNew, newRules)
		}
		if !outcome.Failed() {
			t.Fatal("wanted the gate to fail")
		}
	})

	t.Run("should count leg rules in score.json and list them in the summary", func(t *testing.T) {
		h := newHarness(t, true, nil, hopCase, reframeCase, clteCase)
		runHarness(t, h.settings)

		score, err := conformance.ReadScore(filepath.Join(h.settings.OutDir, h1origin.Name, "score.json"), h1origin.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range score.Rows {
			if row.LegRules == nil || *row.LegRules != (conformance.LegScore{Passed: 13, Failed: 2}) {
				t.Fatalf("\nwanted 13 passed and 2 failed on each approach\ngot:\n%+v", row.LegRules)
			}
		}
		summary, err := os.ReadFile(filepath.Join(h.settings.OutDir, h1origin.Name, string(conformance.RunProxy), "summary.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(summary), "| leg:connection | RFC 9110 §7.6.1 | MUST | 6 | 3 |") {
			t.Fatalf("wanted the Connection rule's counts in the summary\ngot:\n%s", summary)
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

func TestConnectTLS(t *testing.T) {
	t.Run("should fail a connect-tls case when the proxy gives no CA certificate to trust", func(t *testing.T) {
		h := newHarness(t, false, nil)
		launch := h.settings.Launch
		h.settings.Launch = func(ctx context.Context, suite conformance.Suite, run conformance.RunName, dir string) (conformance.Proxy, error) {
			proxy, err := launch(ctx, suite, run, dir)
			return noCertificate{proxy}, err
		}
		h.settings.Approaches = []conformance.Approach{conformance.ApproachConnectTLS}

		result := runHarness(t, h.settings).Results[0]
		if result.Err == nil || !strings.Contains(result.Err.Error(), "CA certificate") {
			t.Fatalf("wanted an error naming the missing CA certificate\ngot: %v", result.Err)
		}
	})

	t.Run("should fail a connect-tls case when the proxy's certificate is not from its CA", func(t *testing.T) {
		h := newHarness(t, false, nil)
		other, err := conformance.NewCA()
		if err != nil {
			t.Fatal(err)
		}
		launch := h.settings.Launch
		h.settings.Launch = func(ctx context.Context, suite conformance.Suite, run conformance.RunName, dir string) (conformance.Proxy, error) {
			proxy, err := launch(ctx, suite, run, dir)
			return otherCertificate{Proxy: proxy, pem: other.PEM()}, err
		}
		h.settings.Approaches = []conformance.Approach{conformance.ApproachConnectTLS}

		result := runHarness(t, h.settings).Results[0]
		var unknown x509.UnknownAuthorityError
		if !errors.As(result.Err, &unknown) {
			t.Fatalf("wanted an unknown-authority error\ngot: %v", result.Err)
		}
	})
}

// noCertificate hides the stand-in's CA certificate.
type noCertificate struct{ conformance.Proxy }

// otherCertificate gives a CA certificate the stand-in did not sign with.
type otherCertificate struct {
	conformance.Proxy
	pem []byte
}

func (o otherCertificate) Certificate(context.Context) ([]byte, error) { return o.pem, nil }

func differences(matches []conformance.Match) []conformance.Difference {
	var ds []conformance.Difference
	for _, m := range matches {
		ds = append(ds, m.Difference)
	}
	return ds
}
