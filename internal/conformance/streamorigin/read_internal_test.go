package streamorigin

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

func TestH1ConnRead(t *testing.T) {
	t.Run("should record a request with no body as ended before the origin's script is told its head arrived", func(t *testing.T) {
		c := &h1Conn{side: SideOrigin, t: newTranscript()}
		br := bufio.NewReader(strings.NewReader("GET /download HTTP/1.1\r\nHost: origin.test\r\n\r\n"))
		var ended int
		c.read(br, "", func() { ended, _ = c.t.count(SideOrigin, KindEnd) })
		if ended != 1 {
			t.Fatalf("\nwanted:\nthe origin's end recorded when onHead runs\ngot:\n%d ends", ended)
		}
		if got, _ := c.t.count(SideOrigin, KindEnd); got != 1 {
			t.Fatalf("\nwanted:\none end\ngot:\n%d", got)
		}
	})

	t.Run("should record the end of a length-framed body together with its last message", func(t *testing.T) {
		tr := newTranscript()
		c := &h1Conn{side: SideClient, t: tr}
		raw := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 11\r\n\r\ngot part 1\n"
		c.read(bufio.NewReader(strings.NewReader(raw)), "POST", nil)
		// One update for the head and one atomic update for both the last
		// message and end, so a waiter waking on KindMessage already sees KindEnd.
		if got := tr.updateCount(); got != 2 {
			t.Fatalf("\nwanted:\n2 atomic updates (head, then message+end together)\ngot:\n%d updates", got)
		}
		if got, _ := tr.count(SideClient, KindMessage); got != 1 {
			t.Fatalf("\nwanted:\n1 message\ngot:\n%d", got)
		}
		if got, _ := tr.count(SideClient, KindEnd); got != 1 {
			t.Fatalf("\nwanted:\n1 end\ngot:\n%d", got)
		}
	})
}

func TestH2ConnRead(t *testing.T) {
	t.Run("should record a head that ends its stream as ended before the origin's script is told it arrived", func(t *testing.T) {
		near, far := net.Pipe()
		c := newH2Conn(SideOrigin, newTranscript(), near)
		go func() {
			var block bytes.Buffer
			enc := hpack.NewEncoder(&block)
			for _, f := range [][2]string{{":method", "GET"}, {":scheme", "https"}, {":authority", "origin.test"}, {":path", "/events"}} {
				_ = enc.WriteField(hpack.HeaderField{Name: f[0], Value: f[1]})
			}
			_ = http2.NewFramer(far, far).WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: true})
			_ = far.Close()
		}()
		var ended int
		c.read(func() { ended, _ = c.t.count(SideOrigin, KindEnd) })
		if ended != 1 {
			t.Fatalf("\nwanted:\nthe origin's end recorded when onHead runs\ngot:\n%d ends", ended)
		}
	})

	t.Run("should record END_STREAM together with the DATA frame's last message", func(t *testing.T) {
		near, far := net.Pipe()
		tr := newTranscript()
		c := newH2Conn(SideClient, tr, near)
		c.stream = 1
		go io.Copy(io.Discard, far)
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.read(nil)
		}()
		var block bytes.Buffer
		enc := hpack.NewEncoder(&block)
		for _, f := range [][2]string{{":status", "200"}, {"content-type", "text/plain"}} {
			_ = enc.WriteField(hpack.HeaderField{Name: f[0], Value: f[1]})
		}
		fr := http2.NewFramer(far, far)
		if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: false}); err != nil {
			t.Fatal(err)
		}
		if err := fr.WriteData(1, true, []byte("got part 1\n")); err != nil {
			t.Fatal(err)
		}
		if !tr.await(SideClient, KindEnd, 1, time.Second) {
			t.Fatal("timed out waiting for client end")
		}
		_ = far.Close()
		_ = near.Close()
		<-done
		// One update for the head and one atomic update for both the last
		// message and END_STREAM, so a waiter waking on KindMessage already sees KindEnd.
		if got := tr.updateCount(); got != 2 {
			t.Fatalf("\nwanted:\n2 atomic updates (head, then message+end together)\ngot:\n%d updates", got)
		}
		if got, _ := tr.count(SideClient, KindMessage); got != 1 {
			t.Fatalf("\nwanted:\n1 message\ngot:\n%d", got)
		}
		if got, _ := tr.count(SideClient, KindEnd); got != 1 {
			t.Fatalf("\nwanted:\n1 end\ngot:\n%d", got)
		}
	})
}

func TestRecordWaitsForOriginScript(t *testing.T) {
	ca, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	s := Suite{
		Timeout:    time.Second,
		EndTimeout: 3 * time.Second,
		Settle:     time.Nanosecond,
	}
	job := conformance.Job{Run: conformance.RunDirect, Target: Target, CA: ca}
	wantOrder := strings.Join([]string{
		"origin head",
		"origin message",
		"client head",
		"client message",
		"client end",
		"origin message",
		"origin end",
	}, "\n")
	for _, c := range Catalogue {
		if c.ID != "ordinary-request-stall-http1" && c.ID != "ordinary-request-stall-h2" {
			continue
		}
		for _, approach := range c.approaches() {
			t.Run(conformance.CaseID(c.ID, approach), func(t *testing.T) {
				for i := range 20 {
					rec, err := s.record(context.Background(), c, approach, job)
					if err != nil {
						t.Fatalf("iter %d: %v", i, err)
					}
					if got := order(rec.Arrivals); got != wantOrder {
						t.Fatalf("iter %d:\nwanted order:\n%s\ngot:\n%s", i, wantOrder, got)
					}
				}
			})
		}
	}
}
