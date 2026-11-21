package grpcclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// Verdict is the interop client's verdict on a case.
type Verdict string

// Verdicts.
const (
	Passed Verdict = "passed" // the client exited 0
	Failed Verdict = "failed" // the client exited non-zero
	// TimedOut: the client gave no verdict within the case's time limit
	// and was stopped, as a call that deadlocks behind a buffering proxy is.
	TimedOut Verdict = "timed-out"
)

// Outcome is what one run of the interop client said about its case.
type Outcome struct {
	Verdict Verdict
	Error   string // why it failed: its fatal line or first exception
}

// Recording is a grpc-client case's recording: the interop client's
// verdict and, from the shim's capture of the client's plaintext h2, every
// stream the client opened as the client received it: response headers,
// the gRPC messages' bytes, the trailers with grpc-status and
// grpc-message, and any RST_STREAM. A proxy that replaced a status, a
// message or a header the client does not check still differs from the
// baseline. GOAWAY is left out: whether one reaches the client depends on
// when it leaves.
type Recording struct {
	Verdict Verdict     `json:"verdict"`
	Error   string      `json:"error,omitempty"`
	Streams []h2.Stream `json:"streams"`
}

// record builds a case's recording from the client's outcome and the
// shim's captures of the connections it opened, in order.
func record(name string, outcome Outcome, captures []shim.Capture) (Recording, error) {
	if len(captures) == 0 {
		return Recording{}, fmt.Errorf("%s: the client opened no connection", name)
	}
	recording := Recording{Verdict: outcome.Verdict, Error: outcome.Error, Streams: []h2.Stream{}}
	for _, c := range captures {
		if c.Err != "" {
			return Recording{}, fmt.Errorf("%s: the shim could not reach the target: %s", name, c.Err)
		}
		toServer, toClient := c.Bytes()
		recorder := h2.NewRecorder("")
		eachFrame(bytes.TrimPrefix(toServer, []byte(http2.ClientPreface)), func(frame http2.Frame) error {
			if headers, ok := frame.(*http2.HeadersFrame); ok {
				recorder.Open(headers.StreamID)
			}
			return nil
		})
		if err := eachFrame(toClient, recorder.Frame); err != nil {
			return Recording{}, fmt.Errorf("%s: %w", name, err)
		}
		recording.Streams = append(recording.Streams, recorder.Connection(func(v string) string { return v }).Streams...)
	}
	return recording, nil
}

// eachFrame passes each h2 frame in data to fn, up to the first it cannot
// read, such as one cut short when the connection ended.
func eachFrame(data []byte, fn func(http2.Frame) error) error {
	framer := http2.NewFramer(nil, bytes.NewReader(data))
	framer.SetMaxReadFrameSize(1 << 24)
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			return nil
		}
		if err := fn(frame); err != nil {
			return err
		}
	}
}

var (
	goFatal       = regexp.MustCompile(`FATAL: (.*)$`)
	javaException = regexp.MustCompile(`^(?:Exception in thread "[^"]*" )?([\w.$]+(?:Exception|Error)\b.*)$`)
)

// outcomeOf reads the interop client's verdict from its exit status and
// output: grpc-go's client ends with a FATAL log line, grpc-java's with an
// exception.
func outcomeOf(exit int, output string) Outcome {
	if exit == 0 {
		return Outcome{Verdict: Passed}
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := goFatal.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			return Outcome{Verdict: Failed, Error: m[1]}
		}
	}
	for _, line := range lines {
		if m := javaException.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return Outcome{Verdict: Failed, Error: m[1]}
		}
	}
	return Outcome{Verdict: Failed, Error: strings.TrimSpace(lines[len(lines)-1])}
}

// Compare compares each case's verdict, error and streams with the
// baseline, in the suite's order. Stream field paths look like
// streams[0].trailers.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return conformance.CompareCases(baseline, got, conformance.CaseComparison{Less: lessCase, Diff: compareCase})
}

func compareCase(caseID string, wantData, gotData json.RawMessage) ([]conformance.Difference, error) {
	var want, got Recording
	if err := json.Unmarshal(wantData, &want); err != nil {
		return nil, fmt.Errorf("decoding the baseline: %w", err)
	}
	if err := json.Unmarshal(gotData, &got); err != nil {
		return nil, err
	}
	var differences []conformance.Difference
	for _, field := range []struct{ name, want, got string }{
		{"verdict", string(want.Verdict), string(got.Verdict)},
		{"error", want.Error, got.Error},
	} {
		if field.want != field.got {
			differences = append(differences, conformance.Difference{Case: caseID, Field: field.name, Want: field.want, Got: field.got})
		}
	}
	// h2 compares streams as one connection's; the suite's paths leave
	// the connection out.
	streams := h2.CompareRecordings(caseID,
		h2.Recording{Client: []h2.Connection{{Streams: want.Streams}}},
		h2.Recording{Client: []h2.Connection{{Streams: got.Streams}}})
	for _, d := range streams {
		d.Field = strings.TrimPrefix(d.Field, "client[0].")
		differences = append(differences, d)
	}
	return differences, nil
}

// lessCase orders case IDs as Cases lists them.
func lessCase(a, b string) bool {
	return caseRank(a) < caseRank(b) || caseRank(a) == caseRank(b) && a < b
}

func caseRank(caseID string) int {
	name, _, _ := strings.Cut(caseID, "@")
	for i, c := range Cases {
		if c.Name == name {
			return i
		}
	}
	return len(Cases)
}
