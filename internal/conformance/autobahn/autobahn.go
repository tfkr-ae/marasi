// Package autobahn is the conformance harness's WebSocket suite: the
// Autobahn testsuite in Docker, run against Autobahn's strict testee and the
// harness's lenient echo peer, in both fuzzing modes.
package autobahn

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// Name is the suite's name and its baselines directory.
const Name = "autobahn"

// Image is the Autobahn testsuite image, pinned by digest so a new upstream
// push cannot change the verdicts under the committed baseline.
const Image = "crossbario/autobahn-testsuite@sha256:519915fb568b04c9383f70a1c405ae3ff44ab9e35835b085239c258b6fac3074"

// echoExtension sets every message payload to itself in processWebSocketMessage.
const echoExtension = `

-- Conformance harness: a no-op edit through the extension path.
function processWebSocketMessage(message)
	message:set_payload(message:payload())
end
`

// Suite is the autobahn conformance suite. Each target is one mode and
// peer, named "<mode>-<peer>" like its baseline file.
type Suite struct {
	Image      string        // Autobahn testsuite image
	Modes      []string      // fuzzingclient, fuzzingserver
	Peers      []string      // strict, lenient
	SuiteLimit time.Duration // limit per target; it only catches a hung suite
}

var _ conformance.Suite = Suite{}

// Name is "autobahn".
func (s Suite) Name() string { return Name }

// Runs is every proxied conformance run.
func (s Suite) Runs() []conformance.RunName { return conformance.ProxiedRuns }

// Approaches is none: Autobahn's client has its own proxy option.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachNone}
}

// Targets lists each selected mode and peer.
func (s Suite) Targets() []string {
	var targets []string
	for _, mode := range s.Modes {
		for _, peer := range s.Peers {
			targets = append(targets, mode+"-"+peer)
		}
	}
	return targets
}

// Setup holds WebSocket messages in the Checkpoint runs and echoes each
// payload in the extension run.
func (s Suite) Setup() conformance.Setup {
	return conformance.Setup{
		Traffic:    conformance.TrafficWebSocket,
		Checkpoint: []conformance.CheckpointKind{conformance.CheckpointWebSocket},
		Extension:  echoExtension,
	}
}

// Execute runs one mode against one peer and returns each case's verdicts.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	mode, peer, ok := strings.Cut(job.Target, "-")
	if !ok {
		return nil, fmt.Errorf("autobahn target %q is not <mode>-<peer>", job.Target)
	}
	ctx, cancel := context.WithTimeout(ctx, s.SuiteLimit)
	defer cancel()
	log := job.Log
	if log == nil {
		log = io.Discard
	}
	tester := testsuite{Image: s.Image, Dir: job.Dir, Cases: job.Cases, Exclude: job.Exclude, Log: log}
	report, err := tester.Run(ctx, mode, peer, job.Proxy)
	if err != nil {
		return nil, err
	}
	return report.Recordings()
}

// Compare compares one target's verdicts with the baseline.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return Compare(baseline, got)
}
