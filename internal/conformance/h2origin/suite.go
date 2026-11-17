// Package h2origin is the conformance harness's HTTP/2 origin-leg suite.
// The harness's own client sends each h2 origin fixture case's requests
// over TLS inside CONNECT with ALPN h2, and the case's recording holds what
// the client received and what the origin received, decoded to stream
// meaning (package h2).
//
// The suite is report only in Phase 0: it gates like any suite when run, so
// its own job shows a new deviation, and CI runs it in jobs that do not
// block a merge (continue-on-error). Run it apart from the h1 suites.
package h2origin

import (
	"cmp"
	"context"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// Name is the suite's name and its baselines directory.
const Name = "h2-origin"

// Target is the suite's one target and baseline file.
const Target = "fixtures"

// OriginHost is the name the client gives the origin in :authority and
// CONNECT. The direct run dials loopback; Marasi in its container resolves
// the name to loopback through an /etc/hosts entry.
const OriginHost = "origin.test"

// Suite is the h2-origin conformance suite.
type Suite struct {
	Cases      []Case        // fixture cases; nil means Catalogue
	OriginHost string        // name of the origin; empty means OriginHost
	Listen     string        // address the origins listen on; empty means 127.0.0.1
	Timeout    time.Duration // how long to wait for a handshake, request or response; zero means 5s
	Settle     time.Duration // how long to watch a connection after every stream ended; zero means 250ms
}

var _ conformance.Suite = Suite{}

// Name is "h2-origin".
func (s Suite) Name() string { return Name }

// Runs is only proxy: the h2 Checkpoint and extension runs come with
// HTTP/2 itself (Phase 5).
func (s Suite) Runs() []conformance.RunName { return []conformance.RunName{conformance.RunProxy} }

// Approaches is TLS inside CONNECT, with ALPN h2.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachConnectTLS}
}

// Targets is the fixtures.
func (s Suite) Targets() []string { return []string{Target} }

// Setup runs Marasi in a container that resolves the origin's name to
// loopback. The went-through check is the intercepted TLS: through the
// proxy the client trusts only the proxy's CA, which a proxy speaking only
// HTTP/1.1, and so recording no h2 traffic, still shows.
func (s Suite) Setup() conformance.Setup {
	return conformance.Setup{Traffic: conformance.TrafficTLS, Container: true, Hosts: []string{s.originHost()}}
}

// Execute runs every selected case, one at a time.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	approaches := s.Approaches()
	return conformance.RecordCatalogue(ctx, job, s.caseIDs(), func(ctx context.Context, i int) (any, error) {
		return s.record(ctx, s.cases()[i/len(approaches)], job)
	})
}

// Compare compares the recordings with the baseline in catalogue order.
// h2 has no leg rules yet (Phase 5).
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return conformance.CompareCases(baseline, got, conformance.CaseComparison{
		Less: conformance.CatalogueOrder(s.caseIDs()),
		Diff: h2.Compare,
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
