package ledgerbench_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/ledgerbench"
)

func TestLatencies(t *testing.T) {
	t.Run("should take nearest-rank percentiles", func(t *testing.T) {
		var latencies ledgerbench.Latencies
		for ms := 20; ms >= 1; ms-- {
			latencies = append(latencies, time.Duration(ms)*time.Millisecond)
		}

		if got := latencies.Percentile(50); got != 10*time.Millisecond {
			t.Fatalf("\nwanted:\n10ms\ngot:\n%v", got)
		}
		if got := latencies.Percentile(95); got != 19*time.Millisecond {
			t.Fatalf("\nwanted:\n19ms\ngot:\n%v", got)
		}
		if got := latencies.Percentile(100); got != 20*time.Millisecond {
			t.Fatalf("\nwanted:\n20ms\ngot:\n%v", got)
		}
	})
}

func TestMeasure(t *testing.T) {
	t.Run("should discard one warm-up run and report p50, p95 and the target", func(t *testing.T) {
		calls := 0
		var iterations int
		result := testing.Benchmark(func(b *testing.B) {
			calls = 0
			ledgerbench.Measure(b, ledgerbench.Target{Max: time.Second}, func() error {
				calls++
				return nil
			})
			iterations = b.N
		})

		if calls != iterations+1 {
			t.Fatalf("\nwanted:\n%d calls\ngot:\n%d", iterations+1, calls)
		}
		for _, metric := range []string{"p50-ms", "p95-ms", "target-ms", "pass"} {
			if _, ok := result.Extra[metric]; !ok {
				t.Fatalf("\nwanted:\nmetric %s\ngot:\n%v", metric, result.Extra)
			}
		}
		if result.Extra["target-ms"] != 1000 || result.Extra["pass"] != 1 {
			t.Fatalf("\nwanted:\ntarget-ms 1000 and pass 1\ngot:\n%v", result.Extra)
		}
	})

	t.Run("should report a miss when p95 is over the target", func(t *testing.T) {
		result := testing.Benchmark(func(b *testing.B) {
			ledgerbench.Measure(b, ledgerbench.Target{Max: time.Microsecond}, func() error {
				time.Sleep(time.Millisecond)
				return nil
			})
		})

		if result.Extra["pass"] != 0 {
			t.Fatalf("\nwanted:\npass 0\ngot:\n%v", result.Extra)
		}
	})

	t.Run("should stop sampling a slow case once its time budget is spent", func(t *testing.T) {
		t.Setenv(ledgerbench.BudgetEnv, "100ms")
		iterations := 0
		result := testing.Benchmark(func(b *testing.B) {
			ledgerbench.Measure(b, ledgerbench.Target{Max: time.Second}, func() error {
				time.Sleep(20 * time.Millisecond)
				return nil
			})
			iterations = b.N
		})

		samples := int(result.Extra["samples"])
		if samples < 5 || samples > 10 || samples >= iterations {
			t.Fatalf("\nwanted:\n5 to 10 samples, fewer than %d iterations\ngot:\n%d", iterations, samples)
		}
	})

	t.Run("should report no pass mark for a case without a target", func(t *testing.T) {
		result := testing.Benchmark(func(b *testing.B) {
			ledgerbench.Measure(b, ledgerbench.Target{}, func() error { return nil })
		})

		_, hasTarget := result.Extra["target-ms"]
		_, hasPass := result.Extra["pass"]
		if hasTarget || hasPass || result.Extra["p95-ms"] < 0 {
			t.Fatalf("\nwanted:\np50 and p95 only\ngot:\n%v", result.Extra)
		}
	})

	t.Run("should prepare each run outside the measurement", func(t *testing.T) {
		prepared, calls := 0, 0
		result := testing.Benchmark(func(b *testing.B) {
			prepared, calls = 0, 0
			ledgerbench.MeasurePrepared(b, ledgerbench.Target{Max: time.Second}, func() error {
				prepared++
				time.Sleep(20 * time.Millisecond)
				return nil
			}, func() (func(), error) {
				calls++
				return nil, nil
			})
		})

		if prepared != calls || calls < 2 {
			t.Fatalf("\nwanted:\none preparation per run\ngot:\n%d preparations, %d runs", prepared, calls)
		}
		if result.Extra["p95-ms"] >= 20 {
			t.Fatalf("\nwanted:\npreparation outside the measurement\ngot:\np95 %vms", result.Extra["p95-ms"])
		}
	})

	t.Run("should run a cold operation's cleanup outside the measurement", func(t *testing.T) {
		cleanups, calls, iterations := 0, 0, 0
		result := testing.Benchmark(func(b *testing.B) {
			cleanups, calls = 0, 0
			ledgerbench.MeasureCold(b, ledgerbench.Target{Max: time.Second}, func() (func(), error) {
				calls++
				return func() { cleanups++; time.Sleep(20 * time.Millisecond) }, nil
			})
			iterations = b.N
		})

		if calls != iterations+1 || cleanups != calls {
			t.Fatalf("\nwanted:\n%d calls and cleanups\ngot:\n%d calls, %d cleanups", iterations+1, calls, cleanups)
		}
		if result.Extra["p95-ms"] >= 20 {
			t.Fatalf("\nwanted:\ncleanup outside the measurement\ngot:\np95 %vms", result.Extra["p95-ms"])
		}
	})

	t.Run("should fail the benchmark when the operation fails", func(t *testing.T) {
		failed := false
		testing.Benchmark(func(b *testing.B) {
			defer func() { failed = b.Failed() }()
			ledgerbench.Measure(b, ledgerbench.Target{Max: time.Second}, func() error {
				return errors.New("boom")
			})
		})

		if !failed {
			t.Fatalf("\nwanted:\nfailed benchmark\ngot:\npassed")
		}
	})
}
