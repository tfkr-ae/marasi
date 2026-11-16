package h2

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// Compare compares a recording with its baseline, both as JSON, and returns
// one difference for each field path that differs. Field paths look like
// client[0].alpn, client[0].streams[0].headers[1] or origin[0].goaway.code.
// Everything in the recording is compared; what it leaves out (SETTINGS,
// flow control, PING, PRIORITY, DATA splitting, padding, HPACK encoding and
// stream IDs) never reaches it.
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
	c := comparer{caseID: caseID}
	c.connections("client", want.Client, got.Client)
	c.connections("origin", want.Origin, got.Origin)
	return c.differences
}

type comparer struct {
	caseID      string
	differences []conformance.Difference
}

func (c *comparer) add(field, want, got string) {
	c.differences = append(c.differences, conformance.Difference{Case: c.caseID, Field: field, Want: want, Got: got})
}

func (c *comparer) count(path string, want, got int) {
	if want != got {
		c.add(path+".count", strconv.Itoa(want), strconv.Itoa(got))
	}
}

func (c *comparer) connections(path string, want, got []Connection) {
	c.count(path, len(want), len(got))
	for i := range min(len(want), len(got)) {
		prefix := fmt.Sprintf("%s[%d]", path, i)
		w, g := want[i], got[i]
		if w.ALPN != g.ALPN {
			c.add(prefix+".alpn", w.ALPN, g.ALPN)
		}
		if preface(w.Preface) != preface(g.Preface) {
			c.add(prefix+".preface", preface(w.Preface), preface(g.Preface))
		}
		c.count(prefix+".streams", len(w.Streams), len(g.Streams))
		for j := range min(len(w.Streams), len(g.Streams)) {
			c.stream(fmt.Sprintf("%s.streams[%d]", prefix, j), w.Streams[j], g.Streams[j])
		}
		c.goAway(prefix+".goaway", w.GoAway, g.GoAway)
	}
}

func (c *comparer) stream(path string, want, got Stream) {
	c.count(path+".headers", len(want.Headers), len(got.Headers))
	for i := range min(len(want.Headers), len(got.Headers)) {
		if w, g := render(want.Headers[i]), render(got.Headers[i]); w != g {
			c.add(fmt.Sprintf("%s.headers[%d]", path, i), w, g)
		}
	}
	if string(want.Data) != string(got.Data) {
		c.add(path+".data", quote(want.Data), quote(got.Data))
	}
	if w, g := render(want.Trailers), render(got.Trailers); w != g {
		c.add(path+".trailers", w, g)
	}
	if want.EndStream != got.EndStream {
		c.add(path+".end-stream", want.EndStream, got.EndStream)
	}
	if want.Reset != got.Reset {
		c.add(path+".reset", want.Reset, got.Reset)
	}
}

func (c *comparer) goAway(path string, want, got *GoAway) {
	switch {
	case want == nil && got == nil:
	case want == nil || got == nil:
		c.add(path, renderGoAway(want), renderGoAway(got))
	default:
		if want.Code != got.Code {
			c.add(path+".code", want.Code, got.Code)
		}
		if w, g := fmt.Sprint(want.Processed), fmt.Sprint(got.Processed); w != g {
			c.add(path+".processed", w, g)
		}
	}
}

func renderGoAway(g *GoAway) string {
	if g == nil {
		return "none"
	}
	return fmt.Sprintf("%s processed %v", g.Code, g.Processed)
}

func preface(p *bool) string {
	if p == nil || *p {
		return "sent"
	}
	return "missing"
}

// render lists fields one per line, as name: value.
func render(fields []Field) string {
	lines := make([]string, len(fields))
	for i, f := range fields {
		lines[i] = f.Name + ": " + f.Value
	}
	return strings.Join(lines, "\n")
}

// quote renders DATA bytes for a difference, cut at 200 bytes.
func quote(data []byte) string {
	const limit = 200
	if len(data) > limit {
		return strconv.Quote(string(data[:limit])) + fmt.Sprintf("... (%d bytes)", len(data))
	}
	return strconv.Quote(string(data))
}
