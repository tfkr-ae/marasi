// Package h1origin is the conformance harness's HTTP/1.1 origin-leg suite.
// The harness's own client sends each origin fixture case's requests,
// directly or through Marasi on each client approach, and the case's
// recording holds what the client received and what the origin received.
package h1origin

import (
	"context"
	"encoding/json"
	"fmt"
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

// Name is "h1-origin".
func (s Suite) Name() string { return Name }

// Runs is the proxy run. The Checkpoint and extension runs need HTTP setup
// in the container launcher first.
func (s Suite) Runs() []conformance.RunName { return []conformance.RunName{conformance.RunProxy} }

// Approaches are absolute form and plain HTTP inside CONNECT.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachAbsolute, conformance.ApproachConnectPlain}
}

// Targets is the fixtures.
func (s Suite) Targets() []string { return []string{Target} }

// Setup checks HTTP traffic and runs Marasi in a container that resolves
// the origin's name to loopback.
func (s Suite) Setup() conformance.Setup {
	return conformance.Setup{Traffic: conformance.TrafficHTTP, Container: true, Hosts: []string{s.originHost()}}
}

// Execute runs every selected case on every selected approach, one at a time.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	recordings := conformance.Recordings{}
	for _, c := range s.cases() {
		for _, approach := range s.Approaches() {
			id := conformance.CaseID(c.ID, approach)
			if !conformance.Selected(id, job.Cases, job.Exclude, job.Approaches) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return recordings, err
			}
			recording, err := s.record(ctx, c, approach, job.Proxy)
			if err != nil {
				return recordings, fmt.Errorf("case %s: %w", id, err)
			}
			data, err := json.Marshal(recording)
			if err != nil {
				return recordings, err
			}
			recordings[id] = data
			if job.Log != nil {
				fmt.Fprintf(job.Log, "%s: recorded\n", id)
			}
		}
	}
	return recordings, nil
}

// Compare compares the recordings with the baseline in catalogue order and
// checks the leg rules on what Marasi sent on both legs of each case.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	order := map[string]int{}
	for i, c := range s.cases() {
		for j, approach := range s.Approaches() {
			order[conformance.CaseID(c.ID, approach)] = i*len(s.Approaches()) + j
		}
	}
	rank := func(id string) int {
		if i, ok := order[id]; ok {
			return i
		}
		return len(order)
	}
	return conformance.CompareCases(baseline, got, nil, func(a, b string) bool {
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return a < b
	}, h1.Compare, h1.Check)
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

func (s Suite) timeout() time.Duration {
	if s.Timeout == 0 {
		return 5 * time.Second
	}
	return s.Timeout
}

func (s Suite) settle() time.Duration {
	if s.Settle == 0 {
		return 250 * time.Millisecond
	}
	return s.Settle
}
