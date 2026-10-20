package websocket

import (
	"bytes"
	"io"
	"sync"
)

// maxWatchedBytes bounds what a watch holds for the next Read. A peer that sends more
// while nobody reads it keeps its bytes queued in the socket; the watch stops and does
// not report it.
const maxWatchedBytes = 4096

// WatchedStream reports a peer that leaves while nobody is reading it. While Checkpoint
// holds a message, the relay stops reading its sender, so a sender that leaves is not
// otherwise noticed until the next write (docs/adr/2026-10-13-checkpoint-hold-ends-when-its-client-leaves.md).
//
// A peer that sent a close frame before its stream ended has started the closing
// handshake rather than left: closing the connection right after its close frame is
// what RFC 6455 asks (7.1.1, 7.1.7). The watch does not report it, so what that peer
// sent before its close is still relayed. The watch starts where the relay stopped
// reading, at a frame boundary, so the bytes it read can be walked as frames.
//
// The stream has no read deadline to end a watch early, so the watch keeps the read
// in flight when a Read comes. Read returns bytes the watch already read without
// waiting for that read, and otherwise waits for it as it would for a stream read.
type WatchedStream struct {
	io.ReadWriteCloser

	mu      sync.Mutex
	changed *sync.Cond
	// watching is true while a watch reads the stream.
	watching bool
	// reading is true while a Read reads the stream itself. A watch does not start
	// then: the reader meets the peer's close, and the stream allows one read at once.
	reading bool
	// stop asks the running watch to return after its read in flight.
	stop bool
	// onClosed is called when the running watch sees the peer close the stream.
	onClosed func()
	// pending holds bytes the watch read. Read returns them before reading the stream.
	pending []byte
	// err is the read error that ended a watch. Read returns it after pending.
	err error
	// peerClosing is true when the bytes before err held a close frame.
	peerClosing bool
	closed      bool
}

// NewWatchedStream wraps stream.
func NewWatchedStream(stream io.ReadWriteCloser) *WatchedStream {
	watched := &WatchedStream{ReadWriteCloser: stream}
	watched.changed = sync.NewCond(&watched.mu)
	return watched
}

// Watch reads the stream in the background until the next Read and calls onClosed once
// if the peer leaves: the stream ends or fails without a close frame first. It calls
// onClosed at once when an earlier watch already saw the peer leave. A watch still
// finishing its read in flight carries on as this watch. Watch does nothing while a
// Read reads the stream, such as for a message injected from elsewhere.
func (s *WatchedStream) Watch(onClosed func()) {
	s.mu.Lock()
	if s.err != nil {
		left := !s.peerClosing
		s.mu.Unlock()
		if left {
			onClosed()
		}
		return
	}
	if s.closed || s.reading {
		s.mu.Unlock()
		return
	}
	s.stop = false
	s.onClosed = onClosed
	if s.watching {
		s.mu.Unlock()
		return
	}
	s.watching = true
	s.mu.Unlock()
	go s.watch()
}

func (s *WatchedStream) watch() {
	buf := make([]byte, 512)
	for {
		n, err := s.ReadWriteCloser.Read(buf)
		s.mu.Lock()
		s.pending = append(s.pending, buf[:n]...)
		var onClosed func()
		if err != nil {
			s.err = err
			s.peerClosing = holdsCloseFrame(s.pending)
			// After a Read asked the watch to stop, the relay reads the peer again
			// and meets the end itself, possibly after taking the peer's close frame
			// from pending, so the watch no longer reports it.
			if !s.closed && !s.stop && !s.peerClosing {
				onClosed = s.onClosed
			}
		}
		finished := err != nil || s.stop || len(s.pending) >= maxWatchedBytes
		if finished {
			s.watching = false
			s.onClosed = nil
		}
		s.changed.Broadcast()
		s.mu.Unlock()
		if onClosed != nil {
			onClosed()
		}
		if finished {
			return
		}
	}
}

// Read ends a running watch, then returns watched bytes, the watch's error, or a stream
// read. It waits for the watch's read in flight only when the watch has nothing to return.
func (s *WatchedStream) Read(b []byte) (int, error) {
	s.mu.Lock()
	if s.watching {
		s.stop = true
		for s.watching && len(s.pending) == 0 {
			s.changed.Wait()
		}
	}
	if len(s.pending) > 0 {
		n := copy(b, s.pending)
		s.pending = s.pending[n:]
		s.mu.Unlock()
		return n, nil
	}
	if err := s.err; err != nil {
		s.mu.Unlock()
		return 0, err
	}
	s.reading = true
	s.mu.Unlock()
	n, err := s.ReadWriteCloser.Read(b)
	s.mu.Lock()
	s.reading = false
	s.mu.Unlock()
	return n, err
}

// Close closes the stream and waits for a running watch to return, so nothing reads
// the stream after Close: a hijacked client stream goes back to its HTTP server. A
// watch it ends does not report a closed peer.
func (s *WatchedStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.stop = true
	s.mu.Unlock()
	err := s.ReadWriteCloser.Close()
	s.mu.Lock()
	for s.watching {
		s.changed.Wait()
	}
	s.mu.Unlock()
	return err
}

// holdsCloseFrame reports whether data, starting at a frame boundary, holds the start
// of a close frame.
func holdsCloseFrame(data []byte) bool {
	reader := bytes.NewReader(data)
	for {
		frame, length, err := readFrameHeader(reader)
		if err != nil {
			return false
		}
		if frame.Opcode == OpClose {
			return true
		}
		if length > int64(reader.Len()) {
			return false
		}
		if _, err := reader.Seek(length, io.SeekCurrent); err != nil {
			return false
		}
	}
}
