package h1client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

func capture(toServer, toClient string, end shim.End) shim.Capture {
	return shim.Capture{
		Chunks: []shim.Chunk{{ToServer: true, Data: []byte(toServer)}, {Data: []byte(toClient)}},
		End:    end,
	}
}

// testdataCaptures stand for the shim's captures of the four tests in
// testdata/results.json, which ran against port 18087.
func testdataCaptures() []shim.Capture {
	ok := "HTTP/1.1 200 OK\r\nDate: Mon, 16 Nov 2026 19:51:54 GMT\r\nContent-Length: 22\r\n\r\nHost: 127.0.0.1:18087\n"
	bad := "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
	return []shim.Capture{
		capture("GET / HTTP/1.1\r\nHost: 127.0.0.1:18087\r\n\r\n", ok, shim.EndOpen),
		capture("POST / HTTP/1.1\r\nHost: 127.0.0.1:18087\r\nContent-Length: 23\r\nTransfer-Encoding: chunked\r\n\r\nD\r\nhello-bananas\r\n0\r\n\r\n", bad, shim.EndClosed),
		capture("POST / HTTP/1.1\r\nHost: 127.0.0.1:18087\r\nTransfer-Encoding: chunked\r\nContent-Length: 30\r\n\r\n0\r\n\r\n", bad, shim.EndOpen),
		capture("", "", shim.EndOpen),
	}
}

func decode(t *testing.T, data json.RawMessage) h1.Recording {
	t.Helper()
	var r h1.Recording
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRecordings(t *testing.T) {
	report, err := LoadReport("testdata/results.json")
	if err != nil {
		t.Fatal(err)
	}
	all := func(string) bool { return true }

	t.Run("should pair each test that ran with the connection the shim saw, in order", func(t *testing.T) {
		recordings, err := recordings(report, testdataCaptures(), conformance.ApproachConnectPlain, "127.0.0.1", "18087", all)
		if err != nil {
			t.Fatal(err)
		}
		if len(recordings) != 4 {
			t.Fatalf("\nwanted:\n4 recordings\ngot:\n%d", len(recordings))
		}
		r := decode(t, recordings["SMUG-CL-TE-BOTH@connect-plain"])
		if r.Verdict != "Pass" || len(r.Client) != 1 || r.Client[0].End != h1.EndClosed {
			t.Fatalf("\nwanted:\nverdict Pass on one closed connection\ngot:\n%+v", r)
		}
		if got := r.Client[0].Responses[0].Status(); got != 400 {
			t.Fatalf("\nwanted:\nthe 400 the target sent\ngot:\n%d", got)
		}
	})

	t.Run("should write the shim's port and the Date the same way in every run", func(t *testing.T) {
		recordings, err := recordings(report, testdataCaptures(), conformance.ApproachConnectTLS, "127.0.0.1", "18087", all)
		if err != nil {
			t.Fatal(err)
		}
		r := decode(t, recordings["COMP-BASELINE@connect-tls"])
		response := r.Client[0].Responses[0]
		if got := string(response.Body); got != "Host: 127.0.0.1:00000\n" {
			t.Fatalf("\nwanted:\nthe port as zeros, the body's length kept\ngot:\n%q", got)
		}
		if got := response.Values("Date"); len(got) != 1 || got[0] != " Thu, 01 Jan 1970 00:00:00 GMT" {
			t.Fatalf("\nwanted:\na fixed Date\ngot:\n%q", got)
		}
		if got := r.Client[0].Requests[0].StartLine; got != "GET / HTTP/1.1" {
			t.Fatalf("\nwanted:\nthe request parsed\ngot:\n%q", got)
		}
	})

	t.Run("should keep only the selected cases", func(t *testing.T) {
		selected := func(id string) bool { return strings.HasPrefix(id, "SMUG-") }
		recordings, err := recordings(report, testdataCaptures(), conformance.ApproachConnectPlain, "127.0.0.1", "18087", selected)
		if err != nil {
			t.Fatal(err)
		}
		if len(recordings) != 2 {
			t.Fatalf("\nwanted:\nthe two SMUG- cases\ngot:\n%d recordings", len(recordings))
		}
	})

	t.Run("should fail when the shim saw a different number of connections", func(t *testing.T) {
		if _, err := recordings(report, testdataCaptures()[:3], conformance.ApproachConnectPlain, "127.0.0.1", "18087", all); err == nil {
			t.Fatal("\nwanted:\nan error for 4 tests on 3 connections\ngot:\nnone")
		}
	})

	t.Run("should fail when a connection does not carry its test's request", func(t *testing.T) {
		captures := testdataCaptures()
		captures[0], captures[1] = captures[1], captures[0]
		if _, err := recordings(report, captures, conformance.ApproachConnectPlain, "127.0.0.1", "18087", all); err == nil {
			t.Fatal("\nwanted:\nan error for connections out of order\ngot:\nnone")
		}
	})

	t.Run("should fail when the shim could not reach the target", func(t *testing.T) {
		captures := testdataCaptures()
		captures[2].Err = "shaking hands over TLS: bad certificate"
		if _, err := recordings(report, captures, conformance.ApproachConnectPlain, "127.0.0.1", "18087", all); err == nil {
			t.Fatal("\nwanted:\nan error naming the failed handshake\ngot:\nnone")
		}
	})
}
