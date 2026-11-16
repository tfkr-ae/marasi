package h1client

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// date matches the value of a Date field in a response head.
var date = regexp.MustCompile(`(?i)(\r\ndate:[ \t]*)[A-Za-z]{3}, \d{2} [A-Za-z]{3} \d{4} \d{2}:\d{2}:\d{2} GMT`)

// fixedDate replaces every Date value: the target writes the time it
// answered, which no two runs share. It has the same length, so a body
// that echoes a head keeps its framing.
const fixedDate = "Thu, 01 Jan 1970 00:00:00 GMT"

// recordings pairs each test of an Http11Probe report that ran with the
// connection the shim captured for it, in order: Http11Probe runs one test
// at a time, each on one connection. A test's recording is its verdict and
// the parsed capture. host:port is what Http11Probe dialed, the shim,
// which its requests carry in Host; the port is written as zeros of the
// same length, so the bytes and their framing are the same in every run.
func recordings(report Report, captures []shim.Capture, approach conformance.Approach, host, port string, selected func(caseID string) bool) (conformance.Recordings, error) {
	ran := report.Ran()
	if len(ran) != len(captures) {
		return nil, fmt.Errorf("the report lists %d tests that ran, but the shim saw %d connections", len(ran), len(captures))
	}
	authority := host + ":" + port
	placeholder := host + ":" + strings.Repeat("0", len(port))
	recordings := conformance.Recordings{}
	for i, result := range ran {
		c := captures[i]
		if c.Err != "" {
			return nil, fmt.Errorf("%s: the shim could not reach the target: %s", result.ID, c.Err)
		}
		toServer, toClient := c.Bytes()
		if err := sameRequest(result, toServer); err != nil {
			return nil, err
		}
		id := conformance.CaseID(result.ID, approach)
		if !selected(id) {
			continue
		}
		raw := h1.Raw{ToServer: toServer, ToClient: toClient}.Replace(authority, placeholder)
		raw.ToClient = date.ReplaceAll(raw.ToClient, []byte("${1}"+fixedDate))
		connection := h1.NewConnection(raw, h1.End(c.End))
		connection.Tunnel = c.Tunnel
		data, err := json.Marshal(h1.Recording{Client: []h1.Connection{connection}, Verdict: result.Verdict})
		if err != nil {
			return nil, err
		}
		recordings[id] = data
	}
	return recordings, nil
}

// sameRequest checks that a connection carried the first line of the
// request its test reports, so a connection is never scored as another
// test. A sequence test's report lists labelled steps, and is not checked.
func sameRequest(result Result, toServer []byte) error {
	if result.RawRequest == "" || strings.HasPrefix(result.RawRequest, "── ") {
		return nil
	}
	want, _, _ := strings.Cut(result.RawRequest, "\n")
	// The report decodes the request as ASCII: any other byte reads "?".
	sent := make([]byte, 0, len(want))
	for _, b := range toServer {
		if b > 0x7f {
			b = '?'
		}
		sent = append(sent, b)
		if len(sent) == len(want) {
			break
		}
	}
	if string(sent) != want {
		return fmt.Errorf("%s: the shim's connection carried %q, not the test's request %q", result.ID, sent, want)
	}
	return nil
}
