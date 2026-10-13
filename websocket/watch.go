package websocket

import (
	"io"
	"sync"
)

// maxWatchedBytes bounds what a watch holds for the next Read. A peer that sends more
// while nobody reads it keeps its bytes queued in the socket; the watch stops and does
// not report it.
const maxWatchedBytes = 4096

// WatchedStream reports an upstream that closes while nobody is reading it. While
// Checkpoint holds a server message, the relay stops reading the upstream, so an
// origin that closes is not otherwise noticed until the next write (ADR-0030).
//
// The stream has no read deadline to end a watch early, so a Read that comes while a
// watch is running waits for the watch's read in flight and then returns its bytes.
type WatchedStream struct {
	io.ReadWriteCloser

	mu       sync.Mutex
	finished *sync.Cond
	// watching is true while a watch reads the stream.
	watching bool
	// stop asks the running watch to return after its read in flight.
	stop bool
	// pending holds bytes the watch read. Read returns them before reading the stream.
	pending []byte
	// err is the read error that ended a watch. Read returns it after pending.
	err    error
	closed bool
}

// NewWatchedStream wraps stream.
func NewWatchedStream(stream io.ReadWriteCloser) *WatchedStream {
	watched := &WatchedStream{ReadWriteCloser: stream}
	watched.finished = sync.NewCond(&watched.mu)
	return watched
}

// Watch reads the stream in the background until the next Read and calls onClosed once
// if the peer closes the stream or it fails. It calls onClosed at once when an earlier
// watch already saw the close.
func (s *WatchedStream) Watch(onClosed func()) {
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		onClosed()
		return
	}
	if s.watching || s.closed {
		s.mu.Unlock()
		return
	}
	s.watching = true
	s.stop = false
	s.mu.Unlock()
	go s.watch(onClosed)
}

func (s *WatchedStream) watch(onClosed func()) {
	buf := make([]byte, 512)
	for {
		n, err := s.ReadWriteCloser.Read(buf)
		s.mu.Lock()
		s.pending = append(s.pending, buf[:n]...)
		reportClose := err != nil && !s.closed
		if err != nil {
			s.err = err
		}
		finished := err != nil || s.stop || len(s.pending) >= maxWatchedBytes
		if finished {
			s.watching = false
			s.finished.Broadcast()
		}
		s.mu.Unlock()
		if reportClose {
			onClosed()
		}
		if finished {
			return
		}
	}
}

// Read ends a running watch, then returns watched bytes, the watch's error, or a stream read.
func (s *WatchedStream) Read(b []byte) (int, error) {
	s.mu.Lock()
	if s.watching {
		s.stop = true
		for s.watching {
			s.finished.Wait()
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
	s.mu.Unlock()
	return s.ReadWriteCloser.Read(b)
}

// Close closes the stream. A watch it ends does not report a closed peer.
func (s *WatchedStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.ReadWriteCloser.Close()
}
