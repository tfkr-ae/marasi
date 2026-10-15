// Command ledgerfixture builds the reference project for the ledger
// benchmarks.
//
//	go run ./internal/ledgerbench/cmd/ledgerfixture -pairs 100000 -seed 1 -out /path/to/fixture-100k.marasi
//
// -pairs is 1000000 for the reference project and 100000 for iterating. The
// same seed and size always give the same pairs. Keep fixtures out of the
// repository. The manifest is written next to the project as
// <out>.manifest.json.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"time"

	"github.com/tfkr-ae/marasi/internal/ledgerbench/fixture"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ledgerfixture: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	seed := flag.Uint64("seed", 1, "random seed")
	pairs := flag.Int("pairs", fixture.ReferencePairs, "request/response pairs to write (1000000 for the reference project, 100000 for iterating)")
	out := flag.String("out", "", "path of the .marasi project to write; must not exist")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile of the build to this file")
	flag.Parse()
	if *out == "" {
		return errors.New("-out is required")
	}
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}

	started := time.Now()
	last := map[string]time.Time{}
	progress := func(stage string, done, total int) {
		if done < total && time.Since(last[stage]) < 10*time.Second {
			return
		}
		last[stage] = time.Now()
		fmt.Fprintf(os.Stderr, "%7s %-10s %d/%d\n", time.Since(started).Round(time.Second), stage, done, total)
	}
	manifest, err := fixture.Build(*out, fixture.Config{Seed: *seed, Pairs: *pairs, Progress: progress})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "built %d pairs in %s: %s\nmanifest: %s\n", manifest.Pairs, time.Since(started).Round(time.Second), *out, fixture.ManifestPath(*out))
	return nil
}
