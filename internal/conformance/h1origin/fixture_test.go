package h1origin

import (
	"net"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance/h1"
)

func TestOriginAwaitEnds(t *testing.T) {
	t.Run("should record a connection its peer ends within the wait as closed", func(t *testing.T) {
		o, err := startOrigin("127.0.0.1", []Step{Await()}, 5*time.Second, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer o.stop()
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", o.port()))
		if err != nil {
			t.Fatal(err)
		}
		awaitAccepted(o)
		time.AfterFunc(50*time.Millisecond, func() { conn.Close() })

		o.awaitEnds(2 * time.Second)

		got := o.snapshot(func(raw h1.Raw) h1.Raw { return raw })
		if len(got) != 1 || got[0].End != h1.EndClosed {
			t.Fatalf("\nwanted:\none connection, closed\ngot:\n%+v", got)
		}
	})

	t.Run("should record a connection still open after the wait as open", func(t *testing.T) {
		o, err := startOrigin("127.0.0.1", []Step{Await()}, 5*time.Second, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer o.stop()
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", o.port()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		awaitAccepted(o)

		o.awaitEnds(50 * time.Millisecond)

		got := o.snapshot(func(raw h1.Raw) h1.Raw { return raw })
		if len(got) != 1 || got[0].End != h1.EndOpen {
			t.Fatalf("\nwanted:\none connection, open\ngot:\n%+v", got)
		}
	})
}

// awaitAccepted waits until o has accepted a connection.
func awaitAccepted(o *origin) {
	for {
		o.mu.Lock()
		accepted := len(o.connections)
		o.mu.Unlock()
		if accepted > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
