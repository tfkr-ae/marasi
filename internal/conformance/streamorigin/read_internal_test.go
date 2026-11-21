package streamorigin

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
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
}
