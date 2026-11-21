package streamorigin

import (
	"bufio"
	"strings"
	"testing"
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
