// Package h1origin is the conformance harness's HTTP/1.1 origin-leg suite.
// The harness's own client sends each origin fixture case's requests,
// directly or through Marasi on each client approach, and the case's
// recording holds what the client received and what the origin received.
package h1origin

import (
	"cmp"
	"context"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1"
)

// Name is the suite's name and its baselines directory.
const Name = "h1-origin"

// Target is the suite's one target and baseline file.
const Target = "fixtures"

// OriginHost is the name the client gives the origin in request targets,
// Host and CONNECT. The direct run dials loopback; Marasi in its container
// resolves the name to loopback through an /etc/hosts entry.
const OriginHost = "origin.test"

// Suite is the h1-origin conformance suite.
type Suite struct {
	Cases      []Case        // fixture cases; nil means Catalogue
	OriginHost string        // name of the origin; empty means OriginHost
	Listen     string        // address the origins listen on; empty means 127.0.0.1
	Timeout    time.Duration // how long to wait for a response or request; zero means 5s
	Settle     time.Duration // how long to watch a connection after the last response; zero means 250ms
}

var _ conformance.Suite = Suite{}

// Deterministic asks the direct run to record every case twice and refuse
// one whose two recordings differ.
func (s Suite) Deterministic() {}

// Name is "h1-origin".
func (s Suite) Name() string { return Name }

// Runs is every proxied run.
func (s Suite) Runs() []conformance.RunName { return conformance.ProxiedRuns }

// Approaches are absolute form, plain HTTP inside CONNECT and TLS inside
// CONNECT.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachAbsolute, conformance.ApproachConnectPlain, conformance.ApproachConnectTLS}
}

// Targets is the fixtures.
func (s Suite) Targets() []string { return []string{Target} }

// Setup is the HTTP setup of every proxied run, with Marasi in a container
// that resolves the origin's name to loopback.
func (s Suite) Setup() conformance.Setup {
	setup := conformance.HTTPSetup()
	setup.Container = true
	setup.Hosts = []string{s.originHost()}
	return setup
}

// Execute runs every selected case on every selected approach, one at a time.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	approaches := s.Approaches()
	return conformance.RecordCatalogue(ctx, job, s.caseIDs(), func(ctx context.Context, i int) (any, error) {
		return s.record(ctx, s.cases()[i/len(approaches)], approaches[i%len(approaches)], job)
	})
}

// Compare compares the recordings with the baseline in catalogue order and
// checks the leg rules on what Marasi sent on both legs of each case.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return conformance.CompareCases(baseline, got, conformance.CaseComparison{
		Less: conformance.CatalogueOrder(s.caseIDs()),
		Diff: h1.Compare,
		Legs: []conformance.LegChecker{h1.Check},
	})
}

// caseIDs lists every case on every approach, in catalogue order.
func (s Suite) caseIDs() []string {
	var ids []string
	for _, c := range s.cases() {
		for _, approach := range s.Approaches() {
			ids = append(ids, conformance.CaseID(c.ID, approach))
		}
	}
	return ids
}

func (s Suite) cases() []Case {
	if s.Cases == nil {
		return Catalogue
	}
	return s.Cases
}

func (s Suite) originHost() string {
	if s.OriginHost == "" {
		return OriginHost
	}
	return s.OriginHost
}

func (s Suite) listen() string {
	if s.Listen == "" {
		return "127.0.0.1"
	}
	return s.Listen
}

func (s Suite) timeout() time.Duration { return cmp.Or(s.Timeout, conformance.CaseTimeout) }

func (s Suite) settle() time.Duration { return cmp.Or(s.Settle, conformance.CaseSettle) }
