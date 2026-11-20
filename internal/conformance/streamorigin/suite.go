// Package streamorigin is the conformance harness's stream origin-leg
// suite: our own origin fixtures for streamed pairs, scored against the
// direct baseline like h2-origin. Its client and origin each follow a
// script, and the recording is everything that reached either side, in
// order. The wait-for-answer cases have each side send its next stream
// message only after the peer's last one arrived, so a proxy that holds a
// body until it ends deadlocks them: the waiting side times out, and the
// order shows the message arriving after that, not before the answer.
// No timing is scored.
//
// See docs/adr/2026-11-20-conformance-testers-for-grpc-and-streams.md. The
// suite is a required check on all four conformance runs.
package streamorigin

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// Name is the suite's name and its baselines directory.
const Name = "stream-origin"

// Target is the suite's one target and baseline file.
const Target = "fixtures"

// OriginHost is the name the client gives the origin. The direct run dials
// loopback; Marasi in its container resolves the name to loopback through
// an /etc/hosts entry.
const OriginHost = "origin.test"

// Suite is the stream-origin conformance suite.
type Suite struct {
	Cases      []Case        // fixture cases; nil means Catalogue
	OriginHost string        // name of the origin; empty means OriginHost
	Listen     string        // address the origins listen on; empty means 127.0.0.1
	Timeout    time.Duration // how long an Await or a handshake waits; zero means 5s
	// EndTimeout is how long an Await for the end of a direction waits,
	// so a proxy that delivers a body late still has it recorded; zero
	// means four times Timeout.
	EndTimeout time.Duration
	Settle     time.Duration // how long to keep recording after the client's script; zero means 250ms
}

var _ conformance.Suite = Suite{}

// Deterministic asks the direct run to record every case twice and refuse
// one whose two recordings differ.
func (s Suite) Deterministic() {}

// Name is "stream-origin".
func (s Suite) Name() string { return Name }

// Runs is every proxied run.
func (s Suite) Runs() []conformance.RunName { return conformance.ProxiedRuns }

// Approaches are every approach a case can use: an h2 case uses TLS
// inside CONNECT, with ALPN h2; an HTTP/1.1 case uses all three.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachAbsolute, conformance.ApproachConnectPlain, conformance.ApproachConnectTLS}
}

// Targets is the fixtures.
func (s Suite) Targets() []string { return []string{Target} }

// Setup is the HTTP setup of every proxied run, with Marasi in a container
// that resolves the origin's name to loopback: Checkpoint holds every
// request and response, and the extension run sets each body to itself.
func (s Suite) Setup() conformance.Setup {
	setup := conformance.HTTPSetup()
	setup.Container = true
	setup.Hosts = []string{s.originHost()}
	return setup
}

// run is one case on one approach.
type run struct {
	c        Case
	approach conformance.Approach
}

func (s Suite) runs() []run {
	var runs []run
	for _, c := range s.cases() {
		for _, approach := range s.Approaches() {
			if slices.Contains(c.approaches(), approach) {
				runs = append(runs, run{c, approach})
			}
		}
	}
	return runs
}

// caseIDs lists every case on every approach it uses, in catalogue order.
func (s Suite) caseIDs() []string {
	var ids []string
	for _, r := range s.runs() {
		ids = append(ids, conformance.CaseID(r.c.ID, r.approach))
	}
	return ids
}

// Execute runs every selected case, one at a time.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	runs := s.runs()
	return conformance.RecordCatalogue(ctx, job, s.caseIDs(), func(ctx context.Context, i int) (any, error) {
		return s.record(ctx, runs[i].c, runs[i].approach, job)
	})
}

// Compare compares the recordings with the baseline in catalogue order.
// Nothing that belongs to one leg (frames, chunks, flow control, HPACK) is
// recorded, so the suite needs no leg rule.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return conformance.CompareCases(baseline, got, conformance.CaseComparison{
		Less: conformance.CatalogueOrder(s.caseIDs()),
		Diff: Compare,
	})
}

func (s Suite) cases() []Case {
	if s.Cases == nil {
		return Catalogue
	}
	return s.Cases
}

func (s Suite) originHost() string { return cmp.Or(s.OriginHost, OriginHost) }

func (s Suite) listen() string { return cmp.Or(s.Listen, "127.0.0.1") }

func (s Suite) timeout() time.Duration { return cmp.Or(s.Timeout, conformance.CaseTimeout) }

func (s Suite) endTimeout() time.Duration { return cmp.Or(s.EndTimeout, 4*s.timeout()) }

func (s Suite) settle() time.Duration { return cmp.Or(s.Settle, conformance.CaseSettle) }
