package marasi

import (
	"fmt"
	"time"
)

// [DEBUG-ab7c] Temporary timings for the Autobahn CI investigation: any DB
// queue send, DB write, or WebSocket hold step slower than debugSlow is
// logged to the instance log. Remove with the rest of the DEBUG-ab7c tags.
const debugSlow = 100 * time.Millisecond

// debugQueueDB sends item to the DB writer and logs a send that blocked.
func (proxy *Proxy) debugQueueDB(site string, item any) {
	started := time.Now()
	proxy.DBWriteChannel <- item
	if elapsed := time.Since(started); elapsed > debugSlow && proxy.Logger != nil {
		proxy.Logger.Warn("[DEBUG-ab7c] slow db queue send", "site", site, "ms", elapsed.Milliseconds(), "queued", len(proxy.DBWriteChannel))
	}
}

// debugSlowStep logs step when it took longer than debugSlow.
func (proxy *Proxy) debugSlowStep(step string, started time.Time, attrs ...any) {
	if elapsed := time.Since(started); elapsed > debugSlow && proxy.Logger != nil {
		proxy.Logger.Warn("[DEBUG-ab7c] slow "+step, append([]any{"ms", elapsed.Milliseconds()}, attrs...)...)
	}
}

func debugType(item any) string { return fmt.Sprintf("%T", item) }

// debugSince is the milliseconds from started to at, or -1 when at is unset.
func debugSince(started, at time.Time) int64 {
	if at.IsZero() {
		return -1
	}
	return at.Sub(started).Milliseconds()
}
