package websocket

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// watchedPipe returns a watched stream and the peer end that writes to it.
func watchedPipe(t *testing.T) (*WatchedStream, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() {
		local.Close()
		peer.Close()
	})
	return NewWatchedStream(local), peer
}

func encodeFrame(t *testing.T, frame Frame) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := WriteFrame(&buffer, frame, false); err != nil {
		t.Fatalf("encoding frame: %v", err)
	}
	return buffer.Bytes()
}

// assertNotReported fails if reported is signalled within a short wait.
func assertNotReported(t *testing.T, reported <-chan struct{}) {
	t.Helper()
	select {
	case <-reported:
		t.Fatal("the watch reported a peer that closed after its close frame as gone")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWatchedStream(t *testing.T) {
	t.Run("should report a peer that closes without a close frame", func(t *testing.T) {
		stream, peer := watchedPipe(t)
		reported := make(chan struct{}, 1)
		stream.Watch(func() { reported <- struct{}{} })

		if _, err := peer.Write(encodeFrame(t, Frame{Fin: true, Opcode: OpText, Payload: []byte("next")})); err != nil {
			t.Fatalf("writing: %v", err)
		}
		peer.Close()

		select {
		case <-reported:
		case <-time.After(5 * time.Second):
			t.Fatal("the watch did not report the peer leaving")
		}
	})

	t.Run("should not report a peer that closes after a close frame", func(t *testing.T) {
		stream, peer := watchedPipe(t)
		reported := make(chan struct{}, 1)
		stream.Watch(func() { reported <- struct{}{} })

		closeFrame := encodeFrame(t, Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xea}})
		if _, err := peer.Write(closeFrame); err != nil {
			t.Fatalf("writing: %v", err)
		}
		peer.Close()
		assertNotReported(t, reported)

		got, err := io.ReadAll(stream)
		if err != nil && err != io.EOF {
			t.Fatalf("reading: %v", err)
		}
		if !bytes.Equal(got, closeFrame) {
			t.Fatalf("wanted: %x\ngot: %x", closeFrame, got)
		}
	})

	t.Run("should not report a peer that closes after a Read took its close frame", func(t *testing.T) {
		stream, peer := watchedPipe(t)
		reported := make(chan struct{}, 1)
		stream.Watch(func() { reported <- struct{}{} })

		closeFrame := encodeFrame(t, Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xea}})
		if _, err := peer.Write(closeFrame); err != nil {
			t.Fatalf("writing: %v", err)
		}
		// Let the watch store the close frame and start its next read.
		time.Sleep(20 * time.Millisecond)
		got := make([]byte, len(closeFrame))
		if _, err := io.ReadFull(stream, got); err != nil {
			t.Fatalf("reading the close frame: %v", err)
		}
		peer.Close()
		assertNotReported(t, reported)
	})
}
