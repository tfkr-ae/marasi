package grpcclient

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// exchange is one h2c connection as the shim captures it: what the
// client sent and what it received.
type exchange struct {
	toServer, toClient bytes.Buffer
	client, server     *http2.Framer
	clientEnc          *hpack.Encoder
	serverEnc          *hpack.Encoder
	clientBlock        bytes.Buffer
	serverBlock        bytes.Buffer
}

func newExchange(t *testing.T) *exchange {
	t.Helper()
	e := &exchange{}
	e.toServer.WriteString(http2.ClientPreface)
	e.client = http2.NewFramer(&e.toServer, nil)
	e.server = http2.NewFramer(&e.toClient, nil)
	e.clientEnc = hpack.NewEncoder(&e.clientBlock)
	e.serverEnc = hpack.NewEncoder(&e.serverBlock)
	must(t, e.client.WriteSettings())
	must(t, e.server.WriteSettings())
	return e
}

func (e *exchange) request(t *testing.T, id uint32, path string) {
	t.Helper()
	e.clientBlock.Reset()
	for _, f := range h2.Fields(":method", "POST", ":scheme", "http", ":path", path, "content-type", "application/grpc") {
		must(t, e.clientEnc.WriteField(hpack.HeaderField{Name: f.Name, Value: f.Value}))
	}
	must(t, e.client.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: e.clientBlock.Bytes(), EndHeaders: true}))
}

func (e *exchange) headers(t *testing.T, id uint32, end bool, pairs ...string) {
	t.Helper()
	e.serverBlock.Reset()
	for _, f := range h2.Fields(pairs...) {
		must(t, e.serverEnc.WriteField(hpack.HeaderField{Name: f.Name, Value: f.Value}))
	}
	must(t, e.server.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: e.serverBlock.Bytes(), EndHeaders: true, EndStream: end}))
}

func (e *exchange) capture() shim.Capture {
	return shim.Capture{
		Chunks: []shim.Chunk{{ToServer: true, Data: e.toServer.Bytes()}, {Data: e.toClient.Bytes()}},
		End:    shim.EndClosed,
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// unary is a whole unary call on stream 1: response headers, one
// message and the trailers.
func unary(t *testing.T, status, message string) shim.Capture {
	e := newExchange(t)
	e.request(t, 1, "/grpc.testing.TestService/UnaryCall")
	e.headers(t, 1, false, ":status", "200", "content-type", "application/grpc")
	must(t, e.server.WriteData(1, false, []byte{0, 0, 0, 0, 2, 0x0a, 0x00}))
	trailers := []string{"grpc-status", status}
	if message != "" {
		trailers = append(trailers, "grpc-message", message)
	}
	e.headers(t, 1, true, trailers...)
	return e.capture()
}

func TestRecord(t *testing.T) {
	t.Run("should record the client's verdict and every stream as the client received it", func(t *testing.T) {
		got, err := record("large_unary", Outcome{Verdict: Passed}, []shim.Capture{unary(t, "0", "")})
		if err != nil {
			t.Fatal(err)
		}
		want := Recording{Verdict: Passed, Streams: []h2.Stream{{
			Headers:   [][]h2.Field{h2.Fields(":status", "200", "content-type", "application/grpc")},
			Data:      []byte{0, 0, 0, 0, 2, 0x0a, 0x00},
			Trailers:  h2.Fields("grpc-status", "0"),
			EndStream: h2.EndTrailers,
		}}}
		if w, g := string(marshal(t, want)), string(marshal(t, got)); w != g {
			t.Fatalf("\nwanted %s\ngot    %s", w, g)
		}
	})

	t.Run("should keep a stream the client opened and the server never answered", func(t *testing.T) {
		e := newExchange(t)
		e.request(t, 1, "/grpc.testing.TestService/StreamingInputCall")
		e.request(t, 3, "/grpc.testing.TestService/FullDuplexCall")
		e.headers(t, 3, true, ":status", "200", "content-type", "application/grpc", "grpc-status", "4")
		got, err := record("cancel_after_begin", Outcome{Verdict: Passed}, []shim.Capture{e.capture()})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Streams) != 2 || len(got.Streams[0].Headers) != 0 || got.Streams[1].EndStream != h2.EndHeaders {
			t.Fatalf("wanted the unanswered stream first and the trailers-only one second, got %+v", got.Streams)
		}
	})

	t.Run("should leave out a GOAWAY, which comes or not by when the client leaves", func(t *testing.T) {
		e := newExchange(t)
		e.request(t, 1, "/grpc.testing.TestService/EmptyCall")
		e.headers(t, 1, true, ":status", "200", "content-type", "application/grpc", "grpc-status", "0")
		must(t, e.server.WriteGoAway(1, http2.ErrCodeNo, nil))
		with, err := record("empty_unary", Outcome{Verdict: Passed}, []shim.Capture{e.capture()})
		if err != nil {
			t.Fatal(err)
		}
		e = newExchange(t)
		e.request(t, 1, "/grpc.testing.TestService/EmptyCall")
		e.headers(t, 1, true, ":status", "200", "content-type", "application/grpc", "grpc-status", "0")
		without, err := record("empty_unary", Outcome{Verdict: Passed}, []shim.Capture{e.capture()})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(marshal(t, with), marshal(t, without)) {
			t.Fatalf("wanted the same recording, got %+v and %+v", with, without)
		}
	})

	t.Run("should record the client's error when it failed", func(t *testing.T) {
		outcome := Outcome{Verdict: Failed, Error: "/TestService/UnaryCall RPC failed: rpc error: code = Unavailable"}
		got, err := record("large_unary", outcome, []shim.Capture{unary(t, "14", "")})
		if err != nil {
			t.Fatal(err)
		}
		if got.Verdict != Failed || got.Error != outcome.Error {
			t.Fatalf("wanted %+v, got %+v", outcome, got)
		}
	})

	t.Run("should refuse a case whose connection the shim could not take to the target", func(t *testing.T) {
		capture := shim.Capture{End: shim.EndClosed, Err: "shaking hands over TLS as grpc.test: x509: certificate signed by unknown authority"}
		if _, err := record("empty_unary", Outcome{Verdict: Failed}, []shim.Capture{capture}); err == nil {
			t.Fatal("wanted an error")
		}
	})

	t.Run("should refuse a case that opened no connection", func(t *testing.T) {
		if _, err := record("empty_unary", Outcome{Verdict: Failed}, nil); err == nil {
			t.Fatal("wanted an error")
		}
	})
}

func TestOutcomeOf(t *testing.T) {
	for _, test := range []struct {
		name   string
		exit   int
		output string
		want   Outcome
	}{
		{"should pass a client that exited 0", 0, "2026/11/20 20:11:21 INFO: [interop] EmptyUnaryCall done\n", Outcome{Verdict: Passed}},
		{
			"should fail grpc-go's client with its FATAL line, without the timestamp", 1,
			"2026/11/20 20:11:02 INFO: [interop] Client running with test case \"ping_pong\"\n" +
				"2026/11/20 20:11:02 FATAL: [interop] /TestService/FullDuplexCall RPC failed: rpc error: code = Unavailable desc = boom\nexit status 1\n",
			Outcome{Verdict: Failed, Error: "[interop] /TestService/FullDuplexCall RPC failed: rpc error: code = Unavailable desc = boom"},
		},
		{
			"should fail grpc-java's client with its first exception line", 1,
			"Nov 20, 2026 8:11:02 PM io.grpc.testing.integration.TestServiceClient run\n" +
				"SEVERE: Test failed\n" +
				"java.lang.AssertionError: expected:<0> but was:<14>\n\tat org.junit.Assert.fail(Assert.java:89)\n",
			Outcome{Verdict: Failed, Error: "java.lang.AssertionError: expected:<0> but was:<14>"},
		},
		{
			"should fail grpc-java's client with an exception thrown out of main", 1,
			"Running test client_compressed_unary\n" +
				"Exception in thread \"main\" java.lang.AssertionError: expected INVALID_ARGUMENT\n\tat org.junit.Assert.fail(Assert.java:89)\n",
			Outcome{Verdict: Failed, Error: "java.lang.AssertionError: expected INVALID_ARGUMENT"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := outcomeOf(test.exit, test.output); got != test.want {
				t.Fatalf("wanted %+v, got %+v", test.want, got)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	t.Run("should report a replaced grpc-status although the client still passed", func(t *testing.T) {
		want, err := record("custom_metadata", Outcome{Verdict: Passed}, []shim.Capture{unary(t, "0", "")})
		must(t, err)
		got, err := record("custom_metadata", Outcome{Verdict: Passed}, []shim.Capture{unary(t, "0", "replaced")})
		must(t, err)
		baseline := conformance.Recordings{"custom_metadata@connect-tls": marshal(t, want)}
		run := conformance.Recordings{"custom_metadata@connect-tls": marshal(t, got)}
		comparison, err := Suite{}.Compare(Target, baseline, run)
		must(t, err)
		wantDiffs := []conformance.Difference{{Case: "custom_metadata@connect-tls", Field: "streams[0].trailers",
			Want: "grpc-status: 0", Got: "grpc-status: 0\ngrpc-message: replaced"}}
		if !reflect.DeepEqual(comparison.Differences, wantDiffs) {
			t.Fatalf("\nwanted %+v\ngot    %+v", wantDiffs, comparison.Differences)
		}
	})

	t.Run("should report the verdict and error, and order cases as the suite lists them", func(t *testing.T) {
		pass := marshal(t, Recording{Verdict: Passed, Streams: []h2.Stream{}})
		fail := marshal(t, Recording{Verdict: TimedOut, Error: "no verdict in 30s", Streams: []h2.Stream{}})
		baseline := conformance.Recordings{"ping_pong@connect-tls": pass, "empty_unary@connect-tls": pass}
		run := conformance.Recordings{"ping_pong@connect-tls": fail, "empty_unary@connect-tls": pass}
		comparison, err := Suite{}.Compare(Target, baseline, run)
		must(t, err)
		wantDiffs := []conformance.Difference{
			{Case: "ping_pong@connect-tls", Field: "verdict", Want: "passed", Got: "timed-out"},
			{Case: "ping_pong@connect-tls", Field: "error", Want: "", Got: "no verdict in 30s"},
		}
		if !reflect.DeepEqual(comparison.Differences, wantDiffs) {
			t.Fatalf("\nwanted %+v\ngot    %+v", wantDiffs, comparison.Differences)
		}
		if want := []string{"empty_unary@connect-tls", "ping_pong@connect-tls"}; !reflect.DeepEqual(comparison.Cases, want) {
			t.Fatalf("wanted cases %v, got %v", want, comparison.Cases)
		}
	})
}

func marshal(t *testing.T, r Recording) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(r)
	must(t, err)
	return data
}
