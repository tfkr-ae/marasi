package h1

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// hopByHop are the fields the comparison leaves out of every message, with
// the fields its Connection names. They and the framing are checked by leg
// rules instead (RFC 9110 §7.6.1, RFC 9112 §6).
var hopByHop = []string{"connection", "keep-alive", "proxy-connection", "te", "transfer-encoding", "upgrade", "content-length"}

// Compare compares a recording with its baseline, both as JSON, and returns
// one difference for each field path that differs. Field paths look like
// client[0].responses[0].status or origin[0].requests[0].fields.name-case.
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

// CompareRecordings compares a recording with its baseline. On a client
// connection it compares the responses received, and on an origin
// connection the requests received: what each side sent is the harness's
// own script.
func CompareRecordings(caseID string, want, got Recording) []conformance.Difference {
	c := comparer{caseID: caseID}
	if want.Verdict != got.Verdict {
		c.add("verdict", want.Verdict, got.Verdict)
	}
	c.connections("client", want.Client, got.Client, false)
	c.connections("origin", want.Origin, got.Origin, true)
	return c.differences
}

type comparer struct {
	caseID      string
	differences []conformance.Difference
}

func (c *comparer) add(field, want, got string) {
	c.differences = append(c.differences, conformance.Difference{Case: c.caseID, Field: field, Want: want, Got: got})
}

func (c *comparer) connections(path string, want, got []Connection, requests bool) {
	if len(want) != len(got) {
		c.add(path+".count", strconv.Itoa(len(want)), strconv.Itoa(len(got)))
	}
	for i := range min(len(want), len(got)) {
		prefix := fmt.Sprintf("%s[%d]", path, i)
		w, g := want[i], got[i]
		if requests {
			c.messages(prefix+".requests", w.Requests, g.Requests, true)
			if string(w.Leftover.ToServer) != string(g.Leftover.ToServer) {
				c.add(prefix+".leftover", quote(w.Leftover.ToServer), quote(g.Leftover.ToServer))
			}
		} else {
			c.messages(prefix+".responses", w.Responses, g.Responses, false)
			if string(w.Leftover.ToClient) != string(g.Leftover.ToClient) {
				c.add(prefix+".leftover", quote(w.Leftover.ToClient), quote(g.Leftover.ToClient))
			}
		}
		if w.End != g.End {
			c.add(prefix+".end", string(w.End), string(g.End))
		}
	}
}

func (c *comparer) messages(path string, want, got []Message, requests bool) {
	if len(want) != len(got) {
		c.add(path+".count", strconv.Itoa(len(want)), strconv.Itoa(len(got)))
	}
	for i := range min(len(want), len(got)) {
		prefix := fmt.Sprintf("%s[%d]", path, i)
		w, g := want[i], got[i]
		if requests {
			if w.Method() != g.Method() {
				c.add(prefix+".method", w.Method(), g.Method())
			}
			if w.Target() != g.Target() {
				c.add(prefix+".target", w.Target(), g.Target())
			}
		} else {
			if w.Status() != g.Status() {
				c.add(prefix+".status", strconv.Itoa(w.Status()), strconv.Itoa(g.Status()))
			}
			if w.Reason() != g.Reason() {
				c.add(prefix+".reason", w.Reason(), g.Reason())
			}
		}
		c.fields(prefix+".fields", EndToEnd(w), EndToEnd(g))
		if string(w.Body) != string(g.Body) {
			c.add(prefix+".body", quote(w.Body), quote(g.Body))
		}
		c.fields(prefix+".trailers", w.Trailers, g.Trailers)
		if w.Truncated != g.Truncated {
			c.add(prefix+".truncated", strconv.FormatBool(w.Truncated), strconv.FormatBool(g.Truncated))
		}
	}
}

// fields compares two field sections three ways, each its own difference so
// a known deviation in one does not hide another:
//   - path: lines present on one side only (name in any case, exact value)
//   - path.name-case: a name spelled differently on the two sides
//   - path.order: the order of the lines both sides have
func (c *comparer) fields(path string, want, got []Field) {
	key := func(f Field) string { return strings.ToLower(f.Name) + ":" + f.Value }
	onlyWant, onlyGot := subtract(want, got, key), subtract(got, want, key)
	if len(onlyWant) > 0 || len(onlyGot) > 0 {
		c.add(path, render(onlyWant), render(onlyGot))
	}

	spellings := func(fields []Field) map[string][]string {
		names := map[string][]string{}
		for _, f := range fields {
			lower := strings.ToLower(f.Name)
			if !slices.Contains(names[lower], f.Name) {
				names[lower] = append(names[lower], f.Name)
			}
		}
		return names
	}
	wantNames, gotNames := spellings(want), spellings(got)
	var caseWant, caseGot []string
	seen := map[string]bool{}
	for _, f := range want {
		lower := strings.ToLower(f.Name)
		g, ok := gotNames[lower]
		if !ok || seen[lower] || slices.Equal(wantNames[lower], g) {
			continue
		}
		seen[lower] = true
		caseWant = append(caseWant, wantNames[lower]...)
		caseGot = append(caseGot, g...)
	}
	if len(caseWant) > 0 {
		c.add(path+".name-case", strings.Join(caseWant, ", "), strings.Join(caseGot, ", "))
	}

	common := func(fields []Field, drop []Field) []string {
		var keys []string
		for _, f := range subtract(fields, drop, key) {
			keys = append(keys, strings.ToLower(f.Name))
		}
		return keys
	}
	if w, g := common(want, onlyWant), common(got, onlyGot); !slices.Equal(w, g) {
		c.add(path+".order", strings.Join(w, ", "), strings.Join(g, ", "))
	}
}

// subtract returns the fields of a that are not in b, as a multiset by key.
func subtract(a, b []Field, key func(Field) string) []Field {
	count := map[string]int{}
	for _, f := range b {
		count[key(f)]++
	}
	var rest []Field
	for _, f := range a {
		if count[key(f)] > 0 {
			count[key(f)]--
			continue
		}
		rest = append(rest, f)
	}
	return rest
}

// EndToEnd returns a message's fields without the hop-by-hop fields and
// framing fields the comparison leaves out.
func EndToEnd(message Message) []Field {
	excluded := slices.Clone(hopByHop)
	for _, value := range message.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				excluded = append(excluded, name)
			}
		}
	}
	var fields []Field
	for _, f := range message.Fields {
		if !slices.Contains(excluded, strings.ToLower(strings.TrimSpace(f.Name))) {
			fields = append(fields, f)
		}
	}
	return fields
}

func render(fields []Field) string {
	lines := make([]string, len(fields))
	for i, f := range fields {
		lines[i] = f.Name + ":" + f.Value
	}
	return strings.Join(lines, "\n")
}

// quote renders a body for a difference, cut at 200 bytes.
func quote(body []byte) string {
	const limit = 200
	if len(body) > limit {
		return strconv.Quote(string(body[:limit])) + fmt.Sprintf("... (%d bytes)", len(body))
	}
	return strconv.Quote(string(body))
}
