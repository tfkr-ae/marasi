package h2origin_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
	"github.com/tfkr-ae/marasi/internal/conformance/h2origin"
)

// newHarness records the direct baseline of the catalogue and returns
// settings for a proxy run that launches a fresh stand-in in mode m each time.
func newHarness(t *testing.T, m mode, wrap func(*standIn) conformance.Proxy) conformance.Settings {
	t.Helper()
	dir := t.TempDir()
	ca, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	settings := conformance.Settings{
		CA:          ca,
		Suites:      []conformance.Suite{h2origin.Suite{}},
		Cases:       []string{"*"},
		BaselineDir: filepath.Join(dir, "baselines"),
		OutDir:      filepath.Join(dir, "out"),
		Launch: func(context.Context, conformance.Suite, conformance.RunName, string) (conformance.Proxy, error) {
			proxy := startStandIn(t, m, ca)
			if wrap != nil {
				return wrap(proxy), nil
			}
			return proxy, nil
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

// baselineCase reads one case of the committed-style baseline the harness wrote.
func baselineCase(t *testing.T, settings conformance.Settings, id string) h2.Recording {
	t.Helper()
	baseline, err := conformance.ReadBaseline(settings.BaselineFile(h2origin.Name, h2origin.Target))
	if err != nil {
		t.Fatal(err)
	}
	var recording h2.Recording
	if err := json.Unmarshal(baseline[id], &recording); err != nil {
		t.Fatalf("decoding %s: %v", id, err)
	}
	return recording
}

func TestDirectRecording(t *testing.T) {
	settings := newHarness(t, reencode, nil)

	t.Run("should record each stream's header blocks, DATA, trailers and END_STREAM on both sides", func(t *testing.T) {
		got := baselineCase(t, settings, "trailers@connect-tls")
		want := h2.Recording{
			Client: []h2.Connection{{ALPN: "h2", Streams: []h2.Stream{{
				Headers:   [][]h2.Field{h2.Fields(":status", "200", "content-type", "text/plain", "trailer", "x-checksum")},
				Data:      []byte("pong"),
				Trailers:  h2.Fields("x-checksum", "abc", "x-trailer-two", "2"),
				EndStream: h2.EndTrailers,
			}}}},
			Origin: []h2.Connection{{ALPN: "h2", Streams: []h2.Stream{{
				Headers: [][]h2.Field{h2.Fields(":method", "POST", ":scheme", "https", ":authority", "origin.test:PORT",
					":path", "/trailers", "content-type", "text/plain", "te", "trailers")},
				Data:      []byte("ping"),
				Trailers:  h2.Fields("x-request-checksum", "1"),
				EndStream: h2.EndTrailers,
			}}}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should record the RST_STREAM code", func(t *testing.T) {
		got := baselineCase(t, settings, "rst-stream@connect-tls").Client[0].Streams[0]
		if got.Reset != "INTERNAL_ERROR" || string(got.Data) != "partial" || got.EndStream != "" {
			t.Fatalf("\nwanted INTERNAL_ERROR after \"partial\" with no END_STREAM\ngot:\n%+v", got)
		}
	})

	t.Run("should record the GOAWAY code and the streams it says were processed", func(t *testing.T) {
		got := baselineCase(t, settings, "goaway@connect-tls").Client[0]
		want := h2.Connection{ALPN: "h2", Streams: []h2.Stream{
			{Headers: [][]h2.Field{h2.Fields(":status", "200")}, Data: []byte("first"), EndStream: h2.EndData},
			{Headers: [][]h2.Field{}},
		}, GoAway: &h2.GoAway{Code: "NO_ERROR", Processed: []int{0}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should record a body larger than the flow-control window by its length and SHA-256", func(t *testing.T) {
		got := baselineCase(t, settings, "flow-control-window@connect-tls").Client[0].Streams[0]
		const want = "9c7ba7491ab99b39cfcb11c7815edfa453aae3afe155848fe4220f8f731fff9e"
		if got.DataLength != 81920 || got.DataSHA256 != want || got.Data != nil || got.EndStream != h2.EndData {
			t.Fatalf("\nwanted 81920 bytes, sha256 %s, ending on DATA\ngot:\n%+v", want, got)
		}
	})

	t.Run("should record padded, split DATA and other SETTINGS the same as one plain DATA frame", func(t *testing.T) {
		plain := baselineCase(t, settings, "headers-with-data@connect-tls")
		split := baselineCase(t, settings, "data-padded-split@connect-tls")
		if differences := h2.CompareRecordings("x", plain, split); len(differences) != 0 {
			t.Fatalf("\nwanted no differences\ngot:\n%+v", differences)
		}
	})
}

func TestReencodingRelay(t *testing.T) {
	t.Run("should match every case though SETTINGS, HPACK encoding, DATA frames, padding and stream IDs differ", func(t *testing.T) {
		settings := newHarness(t, reencode, nil)
		result := onlyResult(t, runHarness(t, settings))
		if len(result.Cases) != len(h2origin.Catalogue) || result.Matched() != len(result.Cases) || len(result.New) != 0 {
			t.Fatalf("\nwanted every case matched and nothing new\ngot %d of %d:\n%+v", result.Matched(), len(result.Cases), result.New)
		}
	})
}

func TestBrokenRelay(t *testing.T) {
	t.Run("should report a changed header value and a changed DATA byte", func(t *testing.T) {
		settings := newHarness(t, broken, nil)
		settings.Cases = []string{"headers-with-data@*"}
		result := onlyResult(t, runHarness(t, settings))

		id := "headers-with-data@connect-tls"
		want := []conformance.Difference{
			{Case: id, Field: "client[0].streams[0].headers[0]",
				Want: ":status: 200\ncontent-type: text/plain\nx-origin: fixture",
				Got:  ":status: 200\ncontent-type: text/plainx\nx-origin: fixture"},
			{Case: id, Field: "client[0].streams[0].data", Want: `"hello, world"`, Got: `"Hello, world"`},
		}
		if !reflect.DeepEqual(result.New, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, result.New)
		}
	})
}

func TestHTTP11Proxy(t *testing.T) {
	t.Run("should report the ALPN answer, no streams and no origin connection", func(t *testing.T) {
		settings := newHarness(t, http11, nil)
		settings.Cases = []string{"goaway@*"}
		result := onlyResult(t, runHarness(t, settings))

		id := "goaway@connect-tls"
		want := []conformance.Difference{
			{Case: id, Field: "client[0].alpn", Want: "h2", Got: "http/1.1"},
			{Case: id, Field: "client[0].streams.count", Want: "2", Got: "0"},
			{Case: id, Field: "client[0].goaway", Want: "NO_ERROR processed [0]", Got: "none"},
			{Case: id, Field: "origin.count", Want: "1", Got: "0"},
		}
		if !reflect.DeepEqual(result.New, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, result.New)
		}
	})
}

func TestProxyWithoutCACertificate(t *testing.T) {
	t.Run("should fail the target as not proxied", func(t *testing.T) {
		settings := newHarness(t, reencode, func(s *standIn) conformance.Proxy { return uncertified{s} })
		outcome := runHarness(t, settings)
		if len(outcome.Results) != 1 || !errors.Is(outcome.Results[0].Err, conformance.ErrNotProxied) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%+v", conformance.ErrNotProxied, outcome.Results)
		}
	})
}
