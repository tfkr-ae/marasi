package autobahn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance"
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
func PerformanceTiming(baseline, run conformance.Recordings) (Timing, error) {
	var timing Timing
	for id, recording := range run {
		if !IsPerformance(id) {
			continue
		}
		var got, want Result
		if err := json.Unmarshal(recording, &got); err != nil {
			return Timing{}, fmt.Errorf("decoding case %s: %w", id, err)
		}
		if recorded, ok := baseline[id]; ok {
			if err := json.Unmarshal(recorded, &want); err != nil {
				return Timing{}, fmt.Errorf("decoding baseline case %s: %w", id, err)
			}
		}
		timing.Run += got.Duration
		timing.Baseline += want.Duration
	}
	return timing, nil
}

// Recordings converts a report to the harness's recordings.
func (r Report) Recordings() (conformance.Recordings, error) {
	recordings := conformance.Recordings{}
	for id, result := range r {
		data, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("encoding case %s: %w", id, err)
		}
		recordings[id] = data
	}
	return recordings, nil
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
