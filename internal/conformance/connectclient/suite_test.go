package connectclient

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

func TestCaseID(t *testing.T) {
	t.Run("should name a case so a pattern's * stops at each of the runner's levels", func(t *testing.T) {
		name := "Basic/HTTPVersion:2/Protocol:PROTOCOL_GRPC/Codec:CODEC_PROTO/Compression:COMPRESSION_IDENTITY/TLS:false/(grpc server impl)/bidi-stream/full-duplex/success"
		want := "Basic.HTTPVersion:2.Protocol:PROTOCOL_GRPC.Codec:CODEC_PROTO.Compression:COMPRESSION_IDENTITY.TLS:false.(grpc server impl).bidi-stream.full-duplex.success@connect-plain"
		if got := caseID(name); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if !conformance.Selected(want, []string{"*.HTTPVersion:2.*full-duplex*"}, nil, nil) {
			t.Fatal("\nwanted a pattern to select the case")
		}
	})
}

func TestCompare(t *testing.T) {
	t.Run("should report a case whose verdict differs from the baseline", func(t *testing.T) {
		rec := func(v Verdict) json.RawMessage { data, _ := json.Marshal(Recording{Verdict: v}); return data }
		baseline := conformance.Recordings{"b@connect-plain": rec(Passed), "a@connect-plain": rec(Passed)}
		got := conformance.Recordings{"b@connect-plain": rec(TimedOut), "a@connect-plain": rec(Passed)}
		comparison, err := Suite{}.Compare(Target, baseline, got)
		if err != nil {
			t.Fatal(err)
		}
		want := []conformance.Difference{{Case: "b@connect-plain", Field: "verdict", Want: "passed", Got: "timed-out"}}
		if !reflect.DeepEqual(comparison.Differences, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, comparison.Differences)
		}
		if !reflect.DeepEqual(comparison.Cases, []string{"a@connect-plain", "b@connect-plain"}) {
			t.Fatalf("\nwanted cases in ID order\ngot %v", comparison.Cases)
		}
	})
}
