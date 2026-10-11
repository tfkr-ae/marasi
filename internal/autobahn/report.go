package autobahn

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Result is what the harness keeps from one Autobahn case report.
type Result struct {
	Behavior        string `json:"behavior"`           // case verdict: OK, NON-STRICT, INFORMATIONAL, UNIMPLEMENTED, FAILED
	BehaviorClose   string `json:"behaviorClose"`      // closing-handshake verdict
	RemoteCloseCode *int   `json:"remoteCloseCode"`    // close code the peer under test sent, nil when none
	WasClean        bool   `json:"wasClean"`           // whether the TCP close followed a WebSocket close
	Duration        int    `json:"duration,omitempty"` // milliseconds; kept only for 9.x timing
}

// Report maps an Autobahn case id such as "3.1" or "12.1.1" to its result.
type Report map[string]Result

// IsPerformance reports whether id is a 9.x case. Those are tracked for
// timing and excluded from the verdict comparison.
func IsPerformance(id string) bool {
	return strings.HasPrefix(id, "9.")
}

// Cases returns the case ids in Autobahn order (numeric per dotted part).
func (r Report) Cases() []string {
	ids := make([]string, 0, len(r))
	for id := range r {
		ids = append(ids, id)
	}
	SortCases(ids)
	return ids
}

// Select returns the cases that match any include pattern and no exclude
// pattern. Patterns use Autobahn's spec syntax ("3.*", "12.1.1").
func (r Report) Select(include, exclude []string) Report {
	selected := Report{}
	for id, result := range r {
		if matchAny(include, id) && !matchAny(exclude, id) {
			selected[id] = result
		}
	}
	return selected
}

func matchAny(patterns []string, id string) bool {
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, id); ok {
			return true
		}
	}
	return false
}

// SortCases sorts case ids numerically per dotted part, as Autobahn lists them.
func SortCases(ids []string) {
	sort.Slice(ids, func(i, j int) bool { return caseLess(ids[i], ids[j]) })
}

func caseLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr != nil || berr != nil {
			if as[i] != bs[i] {
				return as[i] < bs[i]
			}
			continue
		}
		if an != bn {
			return an < bn
		}
	}
	return len(as) < len(bs)
}

// Timing sums the 9.x case durations of a run and of its baseline, in milliseconds.
type Timing struct {
	Run      int
	Baseline int
}

// PerformanceTiming sums the durations of the 9.x cases present in run, and
// of the same cases in baseline.
func PerformanceTiming(baseline, run Report) Timing {
	var timing Timing
	for id, result := range run {
		if IsPerformance(id) {
			timing.Run += result.Duration
			timing.Baseline += baseline[id].Duration
		}
	}
	return timing
}

// LoadReport reads the index.json Autobahn wrote to dir and the per-case
// reports it names, keeping the results for agent.
func LoadReport(dir, agent string) (Report, error) {
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("reading autobahn index: %w", err)
	}
	var index map[string]map[string]struct {
		Result
		ReportFile string `json:"reportfile"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("decoding autobahn index: %w", err)
	}
	cases, ok := index[agent]
	if !ok {
		return nil, fmt.Errorf("autobahn index in %s has no agent %q", dir, agent)
	}
	report := Report{}
	for id, entry := range cases {
		caseData, err := os.ReadFile(filepath.Join(dir, entry.ReportFile))
		if err != nil {
			return nil, fmt.Errorf("reading autobahn case %s: %w", id, err)
		}
		var detail struct {
			WasClean bool `json:"wasClean"`
		}
		if err := json.Unmarshal(caseData, &detail); err != nil {
			return nil, fmt.Errorf("decoding autobahn case %s: %w", id, err)
		}
		result := entry.Result
		result.WasClean = detail.WasClean
		if !IsPerformance(id) {
			result.Duration = 0
		}
		report[id] = result
	}
	return report, nil
}

// ReadBaseline reads a committed baseline report. A missing file is an empty report.
func ReadBaseline(file string) (Report, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return Report{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading baseline: %w", err)
	}
	report := Report{}
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("decoding baseline %s: %w", file, err)
	}
	return report, nil
}

// WriteBaseline writes report as indented JSON with sorted keys.
func WriteBaseline(file string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding baseline: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("creating baseline directory: %w", err)
	}
	return os.WriteFile(file, append(data, '\n'), 0o644)
}
