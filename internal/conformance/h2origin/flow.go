package h2origin

import (
	"sync"

	"golang.org/x/net/http2"
)

// maxFrame is the largest DATA payload sent: the default SETTINGS_MAX_FRAME_SIZE.
const maxFrame = 16384

// windows tracks the flow-control windows a sender may use (RFC 9113
// §6.9): the connection's and each stream's, from the peer's
// SETTINGS_INITIAL_WINDOW_SIZE and WINDOW_UPDATE frames. It is safe for
// concurrent use; wait blocks until a window opens or the connection ends.
type windows struct {
	mu      sync.Mutex
	opened  *sync.Cond
	conn    int64
	initial int64
	streams map[uint32]int64
	ended   bool
}

func newWindows() *windows {
	w := &windows{conn: 65535, initial: 65535, streams: map[uint32]int64{}}
	w.opened = sync.NewCond(&w.mu)
	return w
}

// received accounts for a frame from the peer.
func (w *windows) received(frame http2.Frame) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if v, ok := f.Value(http2.SettingInitialWindowSize); ok && !f.IsAck() {
			delta := int64(v) - w.initial
			w.initial = int64(v)
			for id := range w.streams {
				w.streams[id] += delta
			}
		}
	case *http2.WindowUpdateFrame:
		if f.StreamID == 0 {
			w.conn += int64(f.Increment)
		} else {
			w.streams[f.StreamID] = w.stream(f.StreamID) + int64(f.Increment)
		}
	default:
		return
	}
	w.opened.Broadcast()
}

// end wakes every waiter for good: the connection is gone.
func (w *windows) end() {
	w.mu.Lock()
	w.ended = true
	w.mu.Unlock()
	w.opened.Broadcast()
}

func (w *windows) stream(id uint32) int64 {
	if n, ok := w.streams[id]; ok {
		return n
	}
	return w.initial
}

// allowed is how many of want bytes may go on stream id in one DATA frame now.
func (w *windows) allowed(id uint32, want int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return int(max(0, min(int64(want), int64(maxFrame), w.conn, w.stream(id))))
}

// spend takes n bytes from the connection's and stream id's windows.
func (w *windows) spend(id uint32, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn -= int64(n)
	w.streams[id] = w.stream(id) - int64(n)
}

// take waits until stream id may send at least need bytes, or at most up
// to want when need is zero and any is open, and spends them. It returns 0
// when the connection ended first.
func (w *windows) take(id uint32, want, need int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	for !w.ended {
		n := int(max(0, min(int64(want), int64(maxFrame), w.conn, w.stream(id))))
		if (need == 0 && n > 0) || (need > 0 && n >= need) {
			if need > 0 {
				n = need
			}
			w.conn -= int64(n)
			w.streams[id] = w.stream(id) - int64(n)
			return n
		}
		w.opened.Wait()
	}
	return 0
}
