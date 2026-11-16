package h1_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1"
)

// leg is one exchange through a proxy: what the client sent and received,
// and what the origin received and sent.
type leg struct {
	clientSent, originReceived, originSent, clientReceived string
}

func (l leg) recording() h1.Recording {
	return h1.Recording{
		Client: []h1.Connection{h1.NewConnection(h1.Raw{ToServer: []byte(l.clientSent), ToClient: []byte(l.clientReceived)}, h1.EndOpen)},
		Origin: []h1.Connection{h1.NewConnection(h1.Raw{ToServer: []byte(l.originReceived), ToClient: []byte(l.originSent)}, h1.EndOpen)},
	}
}

const (
	request  = "GET / HTTP/1.1\r\nHost: o\r\n\r\n"
	viaReq   = "GET / HTTP/1.1\r\nHost: o\r\nVia: 1.1 p\r\n\r\n"
	response = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
	viaResp  = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nVia: 1.1 p\r\n\r\nok"
)

// broken returns the IDs of the rules broken on the case, and fails unless
// every leg rule was checked.
func broken(t *testing.T, l leg) []string {
	t.Helper()
	differences, checks := h1.CheckRecording("c", l.recording())
	var checked, ids []string
	for _, check := range checks {
		checked = append(checked, check.Rule.ID)
		if check.Broken {
			ids = append(ids, check.Rule.ID)
		}
	}
	var all []string
	for _, rule := range h1.LegRules {
		all = append(all, rule.ID)
	}
	if !reflect.DeepEqual(checked, all) {
		t.Fatalf("\nwanted every rule checked once:\n%v\ngot:\n%v", all, checked)
	}
	var reported []string
	for _, d := range differences {
		if d.Case != "c" || d.Level != conformance.LevelMust || d.Got == "" {
			t.Fatalf("wanted a MUST difference for case c with what was sent\ngot:\n%+v", d)
		}
		reported = append(reported, d.Field)
	}
	if !reflect.DeepEqual(reported, ids) {
		t.Fatalf("\nwanted one difference per broken rule:\n%v\ngot:\n%v", ids, reported)
	}
	return ids
}

func TestCheckRecording(t *testing.T) {
	tests := []struct {
		name string
		leg  leg
		want []string
	}{
		{
			name: "should pass a forward that adds Via on both legs",
			leg:  leg{request, viaReq, response, viaResp},
		},
		{
			name: "should break Via on each leg that has none",
			leg:  leg{request, request, response, viaResp},
			want: []string{"leg:via"},
		},
		{
			name: "should break the Connection rule when a field Connection names is forwarded",
			leg: leg{
				"GET / HTTP/1.1\r\nHost: o\r\nConnection: X-Hop\r\nX-Hop: 1\r\n\r\n",
				"GET / HTTP/1.1\r\nHost: o\r\nConnection: X-Hop\r\nX-Hop: 1\r\nVia: 1.1 p\r\n\r\n",
				response, viaResp,
			},
			want: []string{"leg:connection"},
		},
		{
			name: "should break the Connection rule when a response field Connection names is forwarded",
			leg: leg{
				request, viaReq,
				"HTTP/1.1 200 OK\r\nConnection: X-Back\r\nX-Back: 1\r\nContent-Length: 2\r\n\r\nok",
				"HTTP/1.1 200 OK\r\nX-Back: 1\r\nContent-Length: 2\r\nVia: 1.1 p\r\n\r\nok",
			},
			want: []string{"leg:connection"},
		},
		{
			name: "should pass a forward that removes Connection and the fields it names",
			leg: leg{
				"GET / HTTP/1.1\r\nHost: o\r\nConnection: X-Hop\r\nX-Hop: 1\r\n\r\n",
				viaReq, response, viaResp,
			},
		},
		{
			name: "should pass the proxy's own Connection options",
			leg: leg{
				request,
				"GET / HTTP/1.1\r\nHost: o\r\nConnection: keep-alive, TE\r\nKeep-Alive: timeout=5\r\nTE: trailers\r\nVia: 1.1 p\r\n\r\n",
				response, viaResp,
			},
		},
		{
			name: "should pass a chunked body re-framed with a matching Content-Length",
			leg: leg{
				request, viaReq,
				"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
				"HTTP/1.1 200 OK\r\nContent-Length: 5\r\nVia: 1.1 p\r\n\r\nhello",
			},
		},
		{
			name: "should break framing when Content-Length is shorter than the body",
			leg: leg{
				request, viaReq,
				"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
				"HTTP/1.1 200 OK\r\nContent-Length: 3\r\nVia: 1.1 p\r\n\r\nhello",
			},
			want: []string{"leg:framing"},
		},
		{
			name: "should break framing on an invalid Content-Length",
			leg: leg{
				"POST / HTTP/1.1\r\nHost: o\r\nContent-Length: 2\r\n\r\nhi",
				"POST / HTTP/1.1\r\nHost: o\r\nContent-Length: 2, 3\r\nVia: 1.1 p\r\n\r\nhi",
				response, viaResp,
			},
			want: []string{"leg:framing"},
		},
		{
			name: "should break framing on a request whose last coding is not chunked",
			leg: leg{
				"POST / HTTP/1.1\r\nHost: o\r\nContent-Length: 2\r\n\r\nhi",
				"POST / HTTP/1.1\r\nHost: o\r\nTransfer-Encoding: gzip\r\nVia: 1.1 p\r\n\r\nhi",
				response, viaResp,
			},
			want: []string{"leg:framing"},
		},
		{
			name: "should break only the CL and TE rule when both are sent with a valid chunked body",
			leg: leg{
				request, viaReq,
				"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
				"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Length: 5\r\nVia: 1.1 p\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
			},
			want: []string{"leg:cl-and-te"},
		},
		{
			name: "should break field order when same-name lines are reordered",
			leg: leg{
				request, viaReq,
				"HTTP/1.1 200 OK\r\nSet-Cookie: a=1\r\nX-Other: z\r\nSet-Cookie: b=2\r\nContent-Length: 2\r\n\r\nok",
				"HTTP/1.1 200 OK\r\nSet-Cookie: b=2\r\nSet-Cookie: a=1\r\nX-Other: z\r\nContent-Length: 2\r\nVia: 1.1 p\r\n\r\nok",
			},
			want: []string{"leg:field-order"},
		},
		{
			name: "should pass same-name lines combined into one in order",
			leg: leg{
				"GET / HTTP/1.1\r\nHost: o\r\nX-Multi: a\r\nX-Other: z\r\nX-Multi: b\r\n\r\n",
				"GET / HTTP/1.1\r\nHost: o\r\nX-Other: z\r\nX-Multi: a, b\r\nVia: 1.1 p\r\n\r\n",
				response, viaResp,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := broken(t, tt.leg); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("\nwanted broken:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}

func TestCheckLegWithoutWhatWasForwarded(t *testing.T) {
	t.Run("should check what the message alone shows and skip field order", func(t *testing.T) {
		// A client-leg capture with no origin capture, as the h1-client shim records.
		sent := h1.NewConnection(h1.Raw{
			ToServer: []byte(request),
			ToClient: []byte("HTTP/1.1 200 OK\r\nConnection: X-Back\r\nX-Back: 1\r\nContent-Length: 2\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\n\r\n"),
		}, h1.EndOpen)
		differences, checks := h1.CheckLeg("c", []h1.Forwarded{{Path: "client[0].responses[0]", Sent: sent.Responses[0]}})

		var checked []string
		for _, check := range checks {
			checked = append(checked, check.Rule.ID)
		}
		if want := []string{"leg:connection", "leg:framing", "leg:cl-and-te", "leg:via"}; !reflect.DeepEqual(checked, want) {
			t.Fatalf("\nwanted checked:\n%v\ngot:\n%v", want, checked)
		}
		var fields []string
		for _, d := range differences {
			fields = append(fields, d.Field)
			if !strings.HasPrefix(d.Got, "client[0].responses[0]") {
				t.Fatalf("wanted the message's path in what was sent\ngot: %q", d.Got)
			}
		}
		if want := []string{"leg:connection", "leg:cl-and-te", "leg:via"}; !reflect.DeepEqual(fields, want) {
			t.Fatalf("\nwanted broken:\n%v\ngot:\n%v", want, fields)
		}
	})
}
