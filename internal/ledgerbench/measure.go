// Package ledgerbench holds the harness for the ledger benchmarks: the
// measurement policy, the run environment record, and project cloning. The
// benchmarks themselves are in ledger_bench_test.go behind the bench1m build
// tag, and the reference project comes from the fixture package.
package ledgerbench

import (
	"math"
	"os"
	"slices"
	"testing"
	"time"
)

// Target is a benchmark case's pass mark.
type Target struct {
	// Max is the highest p95 that passes.
	Max time.Duration
	// Pathological marks a (P) case: a query shape that cannot be
	// index-driven, with a looser documented Max.
	Pathological bool
	// ReportOnly cases are measured and reported but never gated, such as
	// OS-cold runs.
	ReportOnly bool
}

// GateEnv, when set to 1, makes a case whose p95 misses its target fail the
// benchmark. Without it a miss is only reported, as pass 0.
const GateEnv = "MARASI_BENCH_GATE"

// Latencies are the measured durations of one case.
type Latencies []time.Duration

// Percentile returns the nearest-rank p-th percentile, for p in (0, 100].
func (l Latencies) Percentile(p float64) time.Duration {
	if len(l) == 0 {
		return 0
	}
	sorted := slices.Clone(l)
	slices.Sort(sorted)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}

// Measure is the warm policy: op runs against state that stays open, such as
// one Repository. The first run is a warm-up and is discarded; each of the
// b.N runs after it is timed.
func Measure(b *testing.B, target Target, op func() error) {
	b.Helper()
	MeasureCold(b, target, func() (func(), error) { return func() {}, op() })
}

// MeasureCold is the connection-cold policy: op opens what it needs, such as
// a new sql.DB and Repository, runs, and returns a cleanup that closes it.
// The cleanup is not timed. As with Measure, the first run is discarded, so
// the OS cache is warm.
func MeasureCold(b *testing.B, target Target, op func() (cleanup func(), err error)) {
	b.Helper()
	run := func() time.Duration {
		started := time.Now()
		cleanup, err := op()
		elapsed := time.Since(started)
		if cleanup != nil {
			cleanup()
		}
		if err != nil {
			b.Fatal(err)
		}
		return elapsed
	}
	run()
	b.ResetTimer()
	latencies := make(Latencies, 0, b.N)
	for range b.N {
		latencies = append(latencies, run())
	}
	b.StopTimer()
	report(b, target, latencies)
}

func report(b *testing.B, target Target, latencies Latencies) {
	b.Helper()
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	p95 := latencies.Percentile(95)
	b.ReportMetric(ms(latencies.Percentile(50)), "p50-ms")
	b.ReportMetric(ms(p95), "p95-ms")
	b.ReportMetric(ms(target.Max), "target-ms")
	pass := p95 <= target.Max
	if pass {
		b.ReportMetric(1, "pass")
	} else {
		b.ReportMetric(0, "pass")
	}
	if !pass && !target.ReportOnly && os.Getenv(GateEnv) == "1" {
		b.Errorf("p95 %v is over the target %v", p95, target.Max)
	}
}
