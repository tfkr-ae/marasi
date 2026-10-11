// Command autobahn runs the Autobahn WebSocket testsuite through Marasi and
// reports every case whose verdict, close code, or clean close differs from
// the committed direct baseline. See `make autobahn`.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tfkr-ae/marasi/internal/autobahn"
)

func main() {
	var (
		config         autobahn.Config
		cases, exclude string
		modes, peers   string
		runs           string
		verbose        bool
	)
	flag.StringVar(&config.Binary, "marasi", "dist/marasi", "marasi binary")
	flag.StringVar(&config.Image, "image", autobahn.Image, "Autobahn testsuite image")
	flag.StringVar(&config.OutDir, "out", "dist/autobahn", "working directory for specs and reports")
	flag.StringVar(&config.BaselineDir, "baseline", "internal/autobahn/baseline", "direct baseline directory")
	flag.StringVar(&cases, "cases", "*", "comma-separated Autobahn case patterns, for example 3.*,7.9.*")
	flag.StringVar(&exclude, "exclude", "", "comma-separated Autobahn case patterns to skip")
	flag.StringVar(&modes, "modes", "fuzzingclient,fuzzingserver", "suite modes")
	flag.StringVar(&peers, "peers", "strict,lenient", "upstream peers")
	flag.StringVar(&runs, "runs", "proxy,checkpoint-forward,checkpoint-edit,extension", "runs; direct rewrites the baseline")
	flag.DurationVar(&config.SuiteLimit, "suite-limit", 45*time.Minute, "time limit per mode and peer")
	flag.BoolVar(&verbose, "verbose", false, "stream Autobahn output")
	flag.Parse()

	config.Cases = split(cases)
	config.Exclude = split(exclude)
	config.Modes = split(modes)
	config.Peers = split(peers)
	config.Runs = split(runs)
	for _, dir := range []*string{&config.Binary, &config.OutDir, &config.BaselineDir} {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			fail(err)
		}
		*dir = abs
	}

	logFile, err := openLog(config.OutDir)
	if err != nil {
		fail(err)
	}
	defer logFile.Close()
	config.Log = logFile
	if verbose {
		config.Log = io.MultiWriter(logFile, os.Stderr)
	}
	fmt.Fprintf(os.Stderr, "autobahn: log at %s\n", logFile.Name())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	results, err := autobahn.Run(ctx, config)
	autobahn.WriteTable(os.Stdout, results)
	fmt.Printf("\nfinished in %s\n", time.Since(started).Round(time.Second))
	if err != nil {
		fail(err)
	}
	for _, result := range results {
		if result.Err != nil || len(result.Deviations) > 0 {
			os.Exit(1)
		}
	}
}

func split(value string) []string {
	var parts []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func openLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.Create(filepath.Join(dir, "autobahn.log"))
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "autobahn: %v\n", err)
	os.Exit(2)
}
