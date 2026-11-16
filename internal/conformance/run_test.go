package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// fakeSuite returns canned recordings for each run. Each value is a JSON
// number, and a case differs when its number does.
type fakeSuite struct {
	approaches []Approach
	got        map[RunName]Recordings
	traffic    *int // proxied traffic the stand-in proxy saw
	silent     bool // send nothing through the proxy
}

func (f *fakeSuite) Name() string           { return "fake" }
func (f *fakeSuite) Runs() []RunName        { return ProxiedRuns }
func (f *fakeSuite) Approaches() []Approach { return f.approaches }
func (f *fakeSuite) Targets() []string      { return []string{"t"} }
func (f *fakeSuite) Setup() Setup           { return Setup{Traffic: TrafficWebSocket} }

func (f *fakeSuite) Execute(_ context.Context, job Job) (Recordings, error) {
	if job.Proxy != "" && !f.silent {
		*f.traffic++
	}
	return f.got[job.Run].Select(job.Cases, job.Exclude, job.Approaches), nil
}

func (f *fakeSuite) Compare(_ string, baseline, got Recordings) (Comparison, error) {
	return CompareCases(baseline, got, nil, func(a, b string) bool { return a < b },
		func(id string, want, got json.RawMessage) ([]Difference, error) {
			if string(want) == string(got) {
				return nil, nil
			}
			return []Difference{{Case: id, Field: "value", Want: string(want), Got: string(got)}}, nil
		})
}

// standIn is an in-process proxy that counts the traffic the suite sends.
type standIn struct{ traffic *int }

func (s standIn) Address() string { return "127.0.0.1:1" }
func (s standIn) Stop()           {}
func (s standIn) Newest(context.Context, TrafficKind) (string, error) {
	if *s.traffic == 0 {
		return "", nil
	}
	return strconv.Itoa(*s.traffic), nil
}

func recordings(values map[string]int) Recordings {
	r := Recordings{}
	for id, v := range values {
		r[id] = json.RawMessage(strconv.Itoa(v))
	}
	return r
}

var (
	allowB   = Entry{Cases: "b", Runs: []RunName{RunProxy}, Field: "value", Kind: KindAccepted, Reason: "b may differ", RFC: "RFC 9110 §7.6.3"}
	knownC   = Entry{Cases: "c", Runs: []RunName{RunProxy}, Field: "value", Kind: KindKnown, Reason: "c differs today", Phase: 2}
	knownE   = Entry{Cases: "e", Runs: []RunName{RunProxy, RunExtension}, Field: "value", Kind: KindKnown, Reason: "e differed", Phase: 2}
	knownZ   = Entry{Cases: "z*", Runs: []RunName{RunProxy}, Field: "value", Kind: KindKnown, Reason: "no such case", Phase: 2}
	allowY   = Entry{Cases: "y", Runs: []RunName{RunProxy}, Field: "value", Kind: KindAccepted, Reason: "no such case"}
	baseline = map[string]int{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1}
	// a and e match; b is allowed, c is known, d is new; e's known entry went away.
	proxied = map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 1}
)

type fixture struct {
	settings Settings
	suite    *fakeSuite
}

func newFixture(t *testing.T, deviations Deviations) fixture {
	t.Helper()
	dir := t.TempDir()
	traffic := 0
	suite := &fakeSuite{traffic: &traffic, got: map[RunName]Recordings{RunProxy: recordings(proxied)}}
	settings := Settings{
		Suites:      []Suite{suite},
		Runs:        []RunName{RunProxy},
		Cases:       []string{"*"},
		BaselineDir: filepath.Join(dir, "baselines"),
		OutDir:      filepath.Join(dir, "out"),
		Launch: func(context.Context, Suite, RunName, string) (Proxy, error) {
			return standIn{traffic: &traffic}, nil
		},
		Log: &strings.Builder{},
	}
	if err := WriteBaseline(settings.BaselineFile("fake", "t"), recordings(baseline)); err != nil {
		t.Fatal(err)
	}
	if err := WriteDeviations(settings.DeviationsFile("fake"), deviations); err != nil {
		t.Fatal(err)
	}
	return fixture{settings: settings, suite: suite}
}

func run(t *testing.T, settings Settings) Outcome {
	t.Helper()
	outcome, err := Run(context.Background(), settings)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	return outcome
}

func cases[T any](items []T, id func(T) string) []string {
	var ids []string
	for _, item := range items {
		ids = append(ids, id(item))
	}
	return ids
}

func matchCases(matches []Match) []string {
	return cases(matches, func(m Match) string { return m.Case })
}

func TestRunSortsDifferences(t *testing.T) {
	t.Run("should sort each difference into allowed, known or new", func(t *testing.T) {
		f := newFixture(t, Deviations{allowB, knownC, knownE})
		outcome := run(t, f.settings)

		if len(outcome.Results) != 1 || outcome.Results[0].Err != nil {
			t.Fatalf("\nwanted one result with no error\ngot:\n%+v", outcome.Results)
		}
		result := outcome.Results[0]
		wantNew := []Difference{{Case: "d", Field: "value", Want: "1", Got: "4"}}
		if !reflect.DeepEqual(result.New, wantNew) {
			t.Fatalf("\nwanted new:\n%+v\ngot:\n%+v", wantNew, result.New)
		}
		if got := matchCases(result.Known); !reflect.DeepEqual(got, []string{"c"}) {
			t.Fatalf("\nwanted known:\n[c]\ngot:\n%v", got)
		}
		if got := matchCases(result.Allowed); !reflect.DeepEqual(got, []string{"b"}) {
			t.Fatalf("\nwanted allowed:\n[b]\ngot:\n%v", got)
		}
		if result.Matched() != 2 || len(result.Cases) != 5 {
			t.Fatalf("\nwanted:\n2 of 5 cases matching\ngot:\n%d of %d", result.Matched(), len(result.Cases))
		}
		if !outcome.Failed() {
			t.Fatal("\nwanted:\na failed gate\ngot:\na pass")
		}
	})

	t.Run("should fail a known entry that went away in a run that covered its cases", func(t *testing.T) {
		f := newFixture(t, Deviations{allowB, knownC, knownE})
		outcome := run(t, f.settings)

		want := []WentAway{{Suite: "fake", Run: RunProxy, Entry: knownE, index: 2}}
		if !reflect.DeepEqual(outcome.WentAway, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, outcome.WentAway)
		}
	})

	t.Run("should pass when every difference is listed and no known entry went away", func(t *testing.T) {
		f := newFixture(t, Deviations{allowB, knownC, knownE})
		f.settings.Exclude = []string{"d", "e"}
		outcome := run(t, f.settings)

		if outcome.Failed() || len(outcome.WentAway) != 0 {
			t.Fatalf("\nwanted:\na pass\ngot:\n%+v", outcome)
		}
	})

	t.Run("should report an entry that matched nothing as stale only in a full run", func(t *testing.T) {
		f := newFixture(t, Deviations{allowB, knownC, knownZ, allowY})
		f.settings.Exclude = []string{"d"}
		if outcome := run(t, f.settings); len(outcome.Stale) != 0 || outcome.Failed() {
			t.Fatalf("\nwanted:\nno stale entries in a narrowed run\ngot:\n%+v", outcome.Stale)
		}

		f.settings.Full = true
		outcome := run(t, f.settings)
		want := []Stale{{Suite: "fake", Entry: knownZ}, {Suite: "fake", Entry: allowY}}
		if !reflect.DeepEqual(outcome.Stale, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, outcome.Stale)
		}
		if !outcome.Failed() {
			t.Fatal("\nwanted:\na failed gate\ngot:\na pass")
		}
	})

	t.Run("should refuse a known entry with no phase", func(t *testing.T) {
		noPhase := knownC
		noPhase.Phase = 0
		f := newFixture(t, Deviations{noPhase})
		if _, err := Run(context.Background(), f.settings); err == nil || !strings.Contains(err.Error(), "phase") {
			t.Fatalf("\nwanted:\nan error about the phase\ngot:\n%v", err)
		}
	})

	t.Run("should match an entry with a got value only on that value", func(t *testing.T) {
		onlyTwo := allowB
		onlyTwo.Got = "2"
		f := newFixture(t, Deviations{onlyTwo})
		f.suite.got[RunProxy]["b"] = json.RawMessage("5")
		outcome := run(t, f.settings)

		if got := cases(outcome.Results[0].New, func(d Difference) string { return d.Case }); !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
			t.Fatalf("\nwanted new:\n[b c d]\ngot:\n%v", got)
		}
	})
}

func TestRunChecksTrafficWentThroughTheProxy(t *testing.T) {
	t.Run("should fail a proxied run that sent nothing through the proxy", func(t *testing.T) {
		f := newFixture(t, nil)
		f.suite.silent = true
		outcome := run(t, f.settings)

		if len(outcome.Results) != 1 || !errors.Is(outcome.Results[0].Err, ErrNotProxied) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%+v", ErrNotProxied, outcome.Results)
		}
		if !outcome.Failed() {
			t.Fatal("\nwanted:\na failed gate\ngot:\na pass")
		}
	})
}

func TestRunDirectMergesTheBaseline(t *testing.T) {
	t.Run("should replace only the selected cases in the baseline", func(t *testing.T) {
		f := newFixture(t, nil)
		f.suite.got[RunDirect] = recordings(map[string]int{"a": 9, "f": 9})
		f.settings.Runs = []RunName{RunDirect}
		f.settings.Cases = []string{"a", "f"}
		run(t, f.settings)

		got, err := ReadBaseline(f.settings.BaselineFile("fake", "t"))
		if err != nil {
			t.Fatal(err)
		}
		want := recordings(map[string]int{"a": 9, "b": 1, "c": 1, "d": 1, "e": 1, "f": 9})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})
}

func TestRunUpdateKnown(t *testing.T) {
	t.Run("should add new differences as known, drop runs that went away and keep accepted entries", func(t *testing.T) {
		f := newFixture(t, Deviations{allowB, knownC, knownE, allowY})
		f.settings.UpdateKnown = true
		f.settings.Phase = 2
		run(t, f.settings)

		got, err := ReadDeviations(f.settings.DeviationsFile("fake"))
		if err != nil {
			t.Fatal(err)
		}
		stillE := knownE
		stillE.Runs = []RunName{RunExtension}
		want := Deviations{allowB, knownC, stillE, allowY, {
			Cases: "d", Runs: []RunName{RunProxy}, Field: "value", Kind: KindKnown, Phase: 2,
			Reason: `measured by conformance-update-known: baseline "1", got "4"`,
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}

		// The rewritten file passes the same run.
		f.settings.UpdateKnown = false
		if outcome := run(t, f.settings); outcome.Failed() {
			t.Fatalf("\nwanted:\na pass after update-known\ngot:\n%+v", outcome)
		}
	})

	t.Run("should remove a known entry left with no runs", func(t *testing.T) {
		f := newFixture(t, Deviations{knownE})
		f.settings.Runs = []RunName{RunProxy, RunExtension}
		f.suite.got[RunExtension] = recordings(proxied)
		f.settings.Exclude = []string{"b", "c", "d"}
		f.settings.UpdateKnown = true
		f.settings.Phase = 2
		run(t, f.settings)

		got, err := ReadDeviations(f.settings.DeviationsFile("fake"))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("\nwanted:\nno entries\ngot:\n%+v", got)
		}
	})

	t.Run("should merge the score into the committed score.json", func(t *testing.T) {
		f := newFixture(t, Deviations{allowB, knownC})
		kept := ScoreRow{Run: RunExtension, Target: "t", Matched: 7, Cases: 9}
		replaced := ScoreRow{Run: RunProxy, Target: "t", Matched: 0, Cases: 1}
		if err := WriteScore(f.settings.ScoreFile("fake"), Score{Suite: "fake", Rows: []ScoreRow{kept, replaced}}); err != nil {
			t.Fatal(err)
		}
		f.settings.UpdateKnown = true
		f.settings.Phase = 2
		run(t, f.settings)

		got, err := ReadScore(f.settings.ScoreFile("fake"), "fake")
		if err != nil {
			t.Fatal(err)
		}
		want := Score{Suite: "fake", Rows: []ScoreRow{{Run: RunProxy, Target: "t", Matched: 2, Cases: 5}, kept}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})
}

func TestRunScoresEachApproach(t *testing.T) {
	t.Run("should score each client approach on its own row", func(t *testing.T) {
		f := newFixture(t, nil)
		f.suite.approaches = []Approach{ApproachAbsolute, ApproachConnectPlain}
		both := recordings(map[string]int{"a@absolute": 1, "a@connect-plain": 1, "b@absolute": 1})
		if err := WriteBaseline(f.settings.BaselineFile("fake", "t"), both); err != nil {
			t.Fatal(err)
		}
		f.suite.got[RunProxy] = recordings(map[string]int{"a@absolute": 1, "a@connect-plain": 2, "b@absolute": 1})
		run(t, f.settings)

		got, err := ReadScore(filepath.Join(f.settings.OutDir, "fake", "score.json"), "fake")
		if err != nil {
			t.Fatal(err)
		}
		want := Score{Suite: "fake", Rows: []ScoreRow{
			{Run: RunProxy, Target: "t", Approach: ApproachAbsolute, Matched: 2, Cases: 2},
			{Run: RunProxy, Target: "t", Approach: ApproachConnectPlain, Matched: 0, Cases: 1},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should skip a suite with none of the selected approaches", func(t *testing.T) {
		f := newFixture(t, nil)
		f.suite.approaches = []Approach{ApproachNone}
		f.settings.Approaches = []Approach{ApproachConnectTLS}
		if outcome := run(t, f.settings); len(outcome.Results) != 0 {
			t.Fatalf("\nwanted:\nno results\ngot:\n%+v", outcome.Results)
		}
	})
}

func TestRunWritesReports(t *testing.T) {
	t.Run("should write the summary most urgent first and a diff for each failing case", func(t *testing.T) {
		f := newFixture(t, Deviations{allowB, knownC, knownE})
		var step strings.Builder
		f.settings.Summary = &step
		run(t, f.settings)

		dir := filepath.Join(f.settings.OutDir, "fake", "proxy")
		data, err := os.ReadFile(filepath.Join(dir, "summary.md"))
		if err != nil {
			t.Fatal(err)
		}
		summary := string(data)
		last := -1
		for _, heading := range []string{"### Score", "### New deviations", "### Known deviations that went away", "### Known deviations\n", "### Allowed deviations"} {
			at := strings.Index(summary, heading)
			if at <= last {
				t.Fatalf("\nwanted %q after the previous section\ngot:\n%s", heading, summary)
			}
			last = at
		}
		for _, row := range []string{"| t | - | 2 | 5 |", "| t | d | value | 1 | 4 |", "| e | value | 2 | e differed |", "| t | b | value | 1 | 2 | b may differ | RFC 9110 §7.6.3 |"} {
			if !strings.Contains(summary, row) {
				t.Fatalf("\nwanted row:\n%s\ngot:\n%s", row, summary)
			}
		}
		if step.String() != summary {
			t.Fatalf("\nwanted the step summary to be the summary\ngot:\n%s", step.String())
		}

		var diff struct {
			Case        string          `json:"case"`
			Differences []Difference    `json:"differences"`
			Baseline    json.RawMessage `json:"baseline"`
			Got         json.RawMessage `json:"got"`
		}
		data, err = os.ReadFile(filepath.Join(dir, "diffs", "t", "d.json"))
		if err != nil {
			t.Fatalf("\nwanted a diff for the failing case\ngot:\n%v", err)
		}
		if err := json.Unmarshal(data, &diff); err != nil {
			t.Fatal(err)
		}
		if diff.Case != "d" || string(diff.Baseline) != "1" || string(diff.Got) != "4" || len(diff.Differences) != 1 {
			t.Fatalf("\nwanted:\ncase d, baseline 1, got 4, one difference\ngot:\n%s", data)
		}
		if _, err := os.Stat(filepath.Join(dir, "diffs", "t", "c.json")); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nno diff for a known case\ngot:\n%v", err)
		}
	})
}
