// Package conformance scores how much Marasi changes the traffic between a
// client and an origin. Every case is recorded twice, directly (the direct
// baseline) and through Marasi, and passes when the two recordings match
// apart from entries in the suite's deviations file. Each tester is one
// Suite. See docs/adr/2026-11-16-conformance-is-scored-against-a-direct-baseline.md.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// RunName names a conformance run: one way of putting Marasi in a tester's path.
type RunName string

// Conformance runs. direct has no proxy and writes the baseline; the others
// go through Marasi and are compared to it.
const (
	RunDirect            RunName = "direct"
	RunProxy             RunName = "proxy"
	RunCheckpointForward RunName = "checkpoint-forward"
	RunCheckpointEdit    RunName = "checkpoint-edit"
	RunExtension         RunName = "extension"
)

// ProxiedRuns lists every conformance run that goes through Marasi, in report order.
var ProxiedRuns = []RunName{RunProxy, RunCheckpointForward, RunCheckpointEdit, RunExtension}

// Approach is a client approach: how a client's traffic reaches the proxy listener.
type Approach string

// Client approaches. A suite with no approaches, such as autobahn, uses
// ApproachNone and its case IDs carry no suffix.
const (
	ApproachNone         Approach = ""
	ApproachAbsolute     Approach = "absolute"
	ApproachConnectPlain Approach = "connect-plain"
	ApproachConnectTLS   Approach = "connect-tls"
)

// CaseID joins a tester's case ID and the client approach: "<id>@<approach>",
// or the bare ID for ApproachNone.
func CaseID(id string, approach Approach) string {
	if approach == ApproachNone {
		return id
	}
	return id + "@" + string(approach)
}

// CaseApproach returns the client approach in a case ID.
func CaseApproach(caseID string) Approach {
	if i := strings.LastIndexByte(caseID, '@'); i >= 0 {
		return Approach(caseID[i+1:])
	}
	return ApproachNone
}

// TrafficKind is what a suite sends through Marasi, so the harness can check
// that a proxied run recorded some of it.
type TrafficKind string

// Traffic kinds.
const (
	TrafficWebSocket TrafficKind = "websocket"
	TrafficHTTP      TrafficKind = "http" // request/response pairs
)

// CheckpointKind is a Checkpoint item kind a suite needs held in the
// Checkpoint runs.
type CheckpointKind string

// Checkpoint kinds.
const (
	CheckpointWebSocket CheckpointKind = "websocket"
)

// Setup is what a suite needs from Marasi in each conformance run.
type Setup struct {
	Traffic    TrafficKind      // checked before and after each target
	Checkpoint []CheckpointKind // held and resolved in the Checkpoint runs
	Extension  string           // Lua appended to the workshop extension in the extension run
	// Container runs Marasi in a Linux container (ContainerLauncher)
	// rather than on the host.
	Container bool
	// Hosts are names Marasi in its container resolves to loopback, where
	// the suite's in-process targets listen.
	Hosts []string
}

// Recordings maps a case ID to a suite's recording of that case, as JSON.
// The harness stores and merges them; only the suite reads inside one.
type Recordings map[string]json.RawMessage

// Job is one target of a suite under one conformance run.
type Job struct {
	Run        RunName
	Target     string     // one of Suite.Targets
	Proxy      string     // proxy listener host:port; empty in the direct run
	Cases      []string   // case ID patterns to run
	Exclude    []string   // case ID patterns to skip
	Approaches []Approach // client approaches to run; empty means all the suite supports
	Dir        string     // working directory for this target's tester output
	Log        io.Writer  // progress output
}

// Difference is one way a proxied recording differs from its direct
// baseline, or a leg rule it broke.
type Difference struct {
	Case  string `json:"case"`            // case ID, with the approach
	Field string `json:"field"`           // field path in the recording, a leg-rule ID, FieldMissing or FieldNoBaseline
	Want  string `json:"want,omitempty"`  // the baseline's value, or what the leg rule asks for, rendered
	Got   string `json:"got,omitempty"`   // the proxied value, rendered
	Level Level  `json:"level,omitempty"` // a broken leg rule's level; empty for a field path
}

// Level is a leg rule's requirement level. A broken MUST fails the gate
// unless the deviations file lists it; a broken SHOULD warns.
type Level string

// Requirement levels (RFC 2119).
const (
	LevelMust   Level = "MUST"
	LevelShould Level = "SHOULD"
)

// LegRule is an RFC requirement on what Marasi itself sends on one leg,
// which a direct baseline cannot show. Its ID is the field of the
// difference that reports it broken and of the deviations entries that list it.
type LegRule struct {
	ID    string `json:"id"`
	RFC   string `json:"rfc"`
	Level Level  `json:"level"`
	Text  string `json:"text"` // what the rule asks for
}

// LegCheck is one leg rule checked on one case.
type LegCheck struct {
	Case   string
	Rule   LegRule
	Broken bool
}

// Fields every suite reports for a case present on only one side.
const (
	FieldMissing    = "missing"     // in the baseline, not in the run
	FieldNoBaseline = "no baseline" // in the run, not in the baseline
)

// Comparison is a suite's comparison of one target's recordings with the baseline.
type Comparison struct {
	Cases       []string     // scored case IDs, in the suite's order
	Differences []Difference // in case order
	Notes       []string     // extra lines for the report, such as timing
	// LegChecks are the leg rules checked on each case. A broken one is
	// also in Differences, with the rule's ID as its field and its level.
	LegChecks []LegCheck
}

// Suite is one tester and its targets. It runs the selected cases directly
// or through a proxy and returns one recording per case ID, and compares
// recordings with the baseline. The harness does the rest.
type Suite interface {
	// Name is the suite's name and its baselines directory.
	Name() string
	// Runs lists the proxied conformance runs the suite supports.
	Runs() []RunName
	// Approaches lists the client approaches the suite's case IDs carry;
	// ApproachNone for a suite whose tester reaches the proxy on its own.
	Approaches() []Approach
	// Targets lists the selected targets, one baseline file each.
	Targets() []string
	// Setup is what the suite needs from Marasi.
	Setup() Setup
	// Execute runs the job and returns its recordings.
	Execute(ctx context.Context, job Job) (Recordings, error)
	// Compare compares a run's recordings for target with the baseline,
	// already narrowed to the cases the run was asked to execute.
	Compare(target string, baseline, got Recordings) (Comparison, error)
}

// Proxy is the proxy under test in one conformance run.
type Proxy interface {
	// Address is the proxy listener host:port.
	Address() string
	// Newest returns the ID of the newest item of kind the proxy recorded,
	// or "" when there is none.
	Newest(ctx context.Context, kind TrafficKind) (string, error)
	// Stop stops the proxy.
	Stop()
}

// Launcher starts the proxy for suite under run. dir is the run's report
// directory, where the proxy's log belongs.
type Launcher func(ctx context.Context, suite Suite, run RunName, dir string) (Proxy, error)

// ErrNotProxied reports a proxied run that sent no traffic through Marasi,
// so its recordings say nothing about the proxy.
var ErrNotProxied = errors.New("no traffic went through the proxy")

// checkProxied fails unless the newest item the proxy recorded changed
// during a target: before and after are its ID, or empty when there is none.
func checkProxied(before, after string) error {
	if after == "" || after == before {
		return ErrNotProxied
	}
	return nil
}
