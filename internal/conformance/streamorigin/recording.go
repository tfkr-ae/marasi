package streamorigin

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// Sides of a case.
const (
	SideClient = "client"
	SideOrigin = "origin"
)

// Arrival kinds: what reached one side, or that nothing did in time.
const (
	KindHead     = "head"     // a header section: a request's, or a response's (interim ones too)
	KindMessage  = "message"  // one stream message, cut out by the direction's message framing
	KindBody     = "body"     // a body with no message framing, whole at its end
	KindPartial  = "partial"  // the bytes of a stream message cut short when the direction ended
	KindTrailers = "trailers" // a trailer section
	KindEnd      = "end"      // the direction ended: END_STREAM, or the end of an HTTP/1.1 message
	KindReset    = "reset"    // RST_STREAM, with its code
	KindClosed   = "closed"   // the connection ended before the direction did
	KindAnswer   = "answer"   // the client told the origin, outside the proxy, it saw what it waited for
	KindTimeout  = "timeout"  // the side waited CaseTimeout for Awaited and nothing came
)

// Recording is one case: the ALPN each connection negotiated and, in the
// order they happened, everything that reached the client or the origin.
// A side sends only after what it awaits arrives, so in a direct run the
// order is fixed by cause and effect, and a proxy that holds a stream
// message until its body ends shows up in the order: the waiting side
// times out before the message arrives.
type Recording struct {
	ClientALPN string    `json:"client_alpn"`
	OriginALPN []string  `json:"origin_alpn"` // one per connection the origin accepted
	Arrivals   []Arrival `json:"arrivals"`
}

// Arrival is one thing that reached a side.
type Arrival struct {
	Side    string     `json:"side"`
	Kind    string     `json:"kind"`
	Fields  []h2.Field `json:"fields,omitempty"`  // KindHead, KindTrailers; an HTTP/1.1 start line as pseudo-header fields
	Data    []byte     `json:"data,omitempty"`    // KindMessage, KindBody, KindPartial: the bytes, framing included
	Code    string     `json:"code,omitempty"`    // KindReset
	Awaited string     `json:"awaited,omitempty"` // KindTimeout: the kind awaited
}

// label is the arrival's line in the recording's order.
func (a Arrival) label() string {
	if a.Kind == KindTimeout {
		return fmt.Sprintf("%s %s (%s)", a.Side, a.Kind, a.Awaited)
	}
	return a.Side + " " + a.Kind
}

// compared lists the kinds whose content is compared, each n-th arrival
// of a kind on a side with the n-th in the baseline.
var compared = []string{KindHead, KindMessage, KindBody, KindPartial, KindTrailers, KindReset}

// Compare compares a recording with its baseline, both as JSON. Field
// paths are client.alpn, origin.alpn, order (every arrival's side and
// kind, in order), and side.kind[n] for the content of the n-th arrival
// of a kind on a side, such as client.message[1]: an arrival that came in
// another order still compares its content with its counterpart.
func Compare(caseID string, want, got json.RawMessage) ([]conformance.Difference, error) {
	var w, g Recording
	if err := json.Unmarshal(want, &w); err != nil {
		return nil, fmt.Errorf("decoding baseline: %w", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		return nil, fmt.Errorf("decoding recording: %w", err)
	}
	return CompareRecordings(caseID, w, g), nil
}

// CompareRecordings compares a recording with its baseline.
func CompareRecordings(caseID string, want, got Recording) []conformance.Difference {
	var differences []conformance.Difference
	add := func(field, w, g string) {
		if w != g {
			differences = append(differences, conformance.Difference{Case: caseID, Field: field, Want: w, Got: g})
		}
	}
	add("client.alpn", want.ClientALPN, got.ClientALPN)
	add("origin.alpn", strings.Join(want.OriginALPN, ", ")+" ("+strconv.Itoa(len(want.OriginALPN))+")",
		strings.Join(got.OriginALPN, ", ")+" ("+strconv.Itoa(len(got.OriginALPN))+")")
	add("order", order(want.Arrivals), order(got.Arrivals))
	for _, side := range []string{SideClient, SideOrigin} {
		for _, kind := range compared {
			w, g := of(want.Arrivals, side, kind), of(got.Arrivals, side, kind)
			for i := range max(len(w), len(g)) {
				add(fmt.Sprintf("%s.%s[%d]", side, kind, i), render(w, i), render(g, i))
			}
		}
	}
	return differences
}

func order(arrivals []Arrival) string {
	lines := make([]string, len(arrivals))
	for i, a := range arrivals {
		lines[i] = a.label()
	}
	return strings.Join(lines, "\n")
}

func of(arrivals []Arrival, side, kind string) []Arrival {
	var out []Arrival
	for _, a := range arrivals {
		if a.Side == side && a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// render renders the i-th arrival's content, or "none".
func render(arrivals []Arrival, i int) string {
	if i >= len(arrivals) {
		return "none"
	}
	a := arrivals[i]
	switch {
	case a.Kind == KindReset:
		return a.Code
	case a.Fields != nil:
		lines := make([]string, len(a.Fields))
		for j, f := range a.Fields {
			lines[j] = f.Name + ": " + f.Value
		}
		return strings.Join(lines, "\n")
	}
	return quote(a.Data)
}

// quote renders bytes for a difference, cut at 200 bytes.
func quote(data []byte) string {
	const limit = 200
	if len(data) > limit {
		return strconv.Quote(string(data[:limit])) + fmt.Sprintf("... (%d bytes)", len(data))
	}
	return strconv.Quote(string(data))
}

// transcript collects a case's arrivals from every goroutine, in order,
// and lets a side wait for an arrival.
type transcript struct {
	mu       sync.Mutex
	arrivals []Arrival
	changed  chan struct{} // closed and replaced on every arrival
}

func newTranscript() *transcript {
	return &transcript{changed: make(chan struct{})}
}

func (t *transcript) add(a Arrival) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.arrivals = append(t.arrivals, a)
	close(t.changed)
	t.changed = make(chan struct{})
}

// count counts the arrivals of kind on side.
func (t *transcript) count(side, kind string) (int, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, a := range t.arrivals {
		if a.Side == side && a.Kind == kind {
			n++
		}
	}
	return n, t.changed
}

// await waits until side has at least n arrivals of kind, and reports
// whether they came within timeout.
func (t *transcript) await(side, kind string, n int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		got, changed := t.count(side, kind)
		if got >= n {
			return true
		}
		select {
		case <-changed:
		case <-deadline.C:
			got, _ := t.count(side, kind)
			return got >= n
		}
	}
}

// snapshot is a copy of the arrivals so far, every field value passed
// through normalise.
func (t *transcript) snapshot(normalise func(string) string) []Arrival {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Arrival, len(t.arrivals))
	for i, a := range t.arrivals {
		if a.Fields != nil {
			fields := make([]h2.Field, len(a.Fields))
			for j, f := range a.Fields {
				fields[j] = h2.Field{Name: f.Name, Value: normalise(f.Value)}
			}
			a.Fields = fields
		}
		out[i] = a
	}
	return out
}
