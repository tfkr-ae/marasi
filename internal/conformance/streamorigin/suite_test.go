package streamorigin_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
	"github.com/tfkr-ae/marasi/internal/conformance/streamorigin"
)

// suite waits briefly, so a case a proxy delays times out quickly.
var suite = streamorigin.Suite{Timeout: 300 * time.Millisecond, EndTimeout: 3 * time.Second, Settle: 100 * time.Millisecond}

// newHarness records the direct baseline of the catalogue and returns
// settings for a proxy run that launches a fresh stand-in in mode m.
func newHarness(t *testing.T, m mode) conformance.Settings {
	t.Helper()
	dir := t.TempDir()
	ca, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	settings := conformance.Settings{
		CA:          ca,
		Suites:      []conformance.Suite{suite},
		Cases:       []string{"*"},
		BaselineDir: filepath.Join(dir, "baselines"),
		OutDir:      filepath.Join(dir, "out"),
		Launch: func(context.Context, conformance.Suite, conformance.RunName, string) (conformance.Proxy, error) {
			return startStandIn(t, m, ca), nil
		},
		Log:  &strings.Builder{},
		Runs: []conformance.RunName{conformance.RunDirect},
	}
	outcome := runHarness(t, settings)
	if outcome.Failed() {
		t.Fatalf("direct run failed: %+v", outcome.Results)
	}
	settings.Runs = []conformance.RunName{conformance.RunProxy}
	return settings
}

func runHarness(t *testing.T, settings conformance.Settings) conformance.Outcome {
	t.Helper()
	outcome, err := conformance.Run(context.Background(), settings)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	return outcome
}

func onlyResult(t *testing.T, outcome conformance.Outcome) conformance.Result {
	t.Helper()
	if len(outcome.Results) != 1 || outcome.Results[0].Err != nil {
		t.Fatalf("\nwanted one result with no error\ngot:\n%+v", outcome.Results)
	}
	return outcome.Results[0]
}

func baselineCase(t *testing.T, settings conformance.Settings, id string) streamorigin.Recording {
	t.Helper()
	baseline, err := conformance.ReadBaseline(settings.BaselineFile(streamorigin.Name, streamorigin.Target))
	if err != nil {
		t.Fatal(err)
	}
	var recording streamorigin.Recording
	if err := json.Unmarshal(baseline[id], &recording); err != nil {
		t.Fatalf("decoding %s: %v", id, err)
	}
	return recording
}

func arrival(side, kind string, fields []h2.Field, data string) streamorigin.Arrival {
	a := streamorigin.Arrival{Side: side, Kind: kind, Fields: fields}
	if data != "" {
		a.Data = []byte(data)
	}
	return a
}

// grpc is s in a gRPC envelope: an uncompressed flag and its length.
func grpc(s string) string { return "\x00\x00\x00\x00" + string(rune(len(s))) + s }

func TestDirectRecording(t *testing.T) {
	settings := newHarness(t, splice)

	t.Run("should record each server-sent event over HTTP/1.1 as it arrives, before the client's answer reaches the origin", func(t *testing.T) {
		got := baselineCase(t, settings, "sse-wait-for-answer-http1@connect-plain")
		want := streamorigin.Recording{
			ClientALPN: "",
			OriginALPN: []string{""},
			Arrivals: []streamorigin.Arrival{
				arrival("origin", "head", h2.Fields(":method", "GET", ":path", "/events",
					"Host", "origin.test:PORT", "Accept", "text/event-stream"), ""),
				arrival("origin", "end", nil, ""),
				arrival("client", "head", h2.Fields(":status", "200", "Content-Type", "text/event-stream",
					"Cache-Control", "no-cache", "Transfer-Encoding", "chunked"), ""),
				arrival("client", "message", nil, "id: 1\ndata: first\n\n"),
				arrival("origin", "answer", nil, ""),
				arrival("client", "message", nil, "id: 2\ndata: second\n\n"),
				arrival("client", "end", nil, ""),
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should record each gRPC message of a bidirectional call between the peer's answers", func(t *testing.T) {
		got := baselineCase(t, settings, "grpc-wait-for-answer@connect-tls")
		kinds := make([]string, len(got.Arrivals))
		for i, a := range got.Arrivals {
			kinds[i] = a.Side + " " + a.Kind + " " + string(a.Data)
		}
		want := []string{
			"origin head ", "client head ",
			"client message " + grpc("ping 1"), "origin message " + grpc("pong 1"),
			"client message " + grpc("ping 2"), "origin message " + grpc("pong 2"), "origin end ",
			"client trailers ", "client end ",
		}
		if got.ClientALPN != "h2" || !reflect.DeepEqual(kinds, want) {
			t.Fatalf("\nwanted h2 and:\n%q\ngot %q and:\n%q", want, got.ClientALPN, kinds)
		}
	})

	// messages are a recording's stream messages in order, each as its
	// side and bytes.
	messages := func(got streamorigin.Recording) []string {
		var out []string
		for _, a := range got.Arrivals {
			if a.Kind == "message" || a.Kind == "partial" || a.Kind == "answer" {
				out = append(out, a.Side+" "+a.Kind+" "+string(a.Data))
			}
		}
		return out
	}

	t.Run("should record a gRPC-Web trailers frame over HTTP/2 as its own message, after the client's answer", func(t *testing.T) {
		got := baselineCase(t, settings, "grpc-web-trailers-frame-h2@connect-tls")
		want := []string{
			"origin message " + grpc("watch"), "client message " + grpc("tick 1"), "origin answer ",
			"client message " + grpc("tick 2"), "client message \x80\x00\x00\x00\x10grpc-status: 0\r\n",
		}
		if got.ClientALPN != "h2" || !reflect.DeepEqual(messages(got), want) {
			t.Fatalf("\nwanted h2 and:\n%q\ngot %q and:\n%q", want, got.ClientALPN, messages(got))
		}
	})

	t.Run("should record each Connect message of a bidirectional call between the peer's answers, its end-of-stream envelope its own message", func(t *testing.T) {
		got := baselineCase(t, settings, "connect-wait-for-answer-h2@connect-tls")
		want := []string{
			"client message " + grpc("ping 1"), "origin message " + grpc("pong 1"),
			"client message " + grpc("ping 2"), "origin message " + grpc("pong 2"),
			"client message \x02\x00\x00\x00\x02{}",
		}
		if !reflect.DeepEqual(messages(got), want) {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, messages(got))
		}
	})

	t.Run("should record each NDJSON line over HTTP/2 as it arrives, before the client's answer reaches the origin", func(t *testing.T) {
		got := baselineCase(t, settings, "ndjson-wait-for-answer-h2@connect-tls")
		want := []string{"client message {\"n\":1}\n", "origin answer ", "client message {\"n\":2}\n"}
		if !reflect.DeepEqual(messages(got), want) {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, messages(got))
		}
	})

	t.Run("should record a trailers-only gRPC response as one head that ends the stream", func(t *testing.T) {
		got := baselineCase(t, settings, "grpc-trailers-only@connect-tls").Arrivals
		last := got[len(got)-2:]
		want := []streamorigin.Arrival{
			arrival("client", "head", h2.Fields(":status", "200", "content-type", "application/grpc",
				"grpc-status", "12", "grpc-message", "unknown method"), ""),
			arrival("client", "end", nil, ""),
		}
		if !reflect.DeepEqual(last, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})
}

func TestSplicingRelay(t *testing.T) {
	t.Run("should match every case through a proxy that forwards bytes as they come", func(t *testing.T) {
		settings := newHarness(t, splice)
		result := onlyResult(t, runHarness(t, settings))
		if len(result.Cases) == 0 || result.Matched() != len(result.Cases) || len(result.New) != 0 {
			t.Fatalf("\nwanted every case matched\ngot %d of %d:\n%+v", result.Matched(), len(result.Cases), result.New)
		}
	})
}

func TestLateRelay(t *testing.T) {
	t.Run("should fail the order of a case whose proxy delivers each message late, though every byte arrives", func(t *testing.T) {
		settings := newHarness(t, late)
		settings.Cases = []string{"sse-wait-for-answer-http1@connect-plain"}
		result := onlyResult(t, runHarness(t, settings))

		id := "sse-wait-for-answer-http1@connect-plain"
		want := []conformance.Difference{{Case: id, Field: "order",
			Want: "origin head\norigin end\nclient head\nclient message\norigin answer\nclient message\nclient end",
			Got:  "client timeout (message)\norigin answer\norigin head\norigin end\nclient head\nclient message\nclient message\nclient end",
		}}
		if !reflect.DeepEqual(result.New, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, result.New)
		}
	})
}
