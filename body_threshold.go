package marasi

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
)

// An ordinary pair's body is read whole before the chains run, until it
// passes the body threshold: from then on it is forwarded as it arrives,
// cannot be held or edited, and is stored up to the threshold with the
// too-large ending (docs/adr/2026-11-20-streamed-pairs-are-recognised-from-the-head-and-other-pairs-stay-buffered.md).
// Each engine decides when a body passes (h1engine_threshold.go,
// h2engine_threshold.go); this file is what the pipeline does with one.

// DefaultBodyThreshold is the body threshold when body_threshold is unset:
// 64 MiB, the WebSocket message limit.
const DefaultBodyThreshold = 64 << 20

// bodyChunkSize is how much of a body is read at a time where it goes on
// in pieces: a passing body's wire bytes, or a compressed streamed body.
const bodyChunkSize = 32 << 10

// bodyStallWait is how long a body that has started may go without new
// bytes before it is forwarded as it arrives.
var bodyStallWait = 2 * time.Second

// bodyThreshold is the size in bytes past which a body is forwarded as it
// arrives.
func (proxy *Proxy) bodyThreshold() int64 {
	if proxy.Config != nil && proxy.Config.BodyThreshold > 0 {
		return proxy.Config.BodyThreshold
	}
	return DefaultBodyThreshold
}

// lengthOver reports whether a declared body length is over threshold: such
// a body goes as it arrives from the start.
func lengthOver(length, threshold int64) bool { return length > threshold }

// cutAtThreshold cuts the bytes of a body that passed threshold: what is
// kept for the view and storage, and the rest, which goes on as it arrived.
func cutAtThreshold(arrived []byte, threshold int64) (kept, rest []byte) {
	if int64(len(arrived)) <= threshold {
		return arrived, nil
	}
	return arrived[:threshold:threshold], arrived[threshold:]
}

// bodyStall is the wait after which a body that has started but gets no
// new bytes for bodyStallWait passes. Its zero value is not started.
type bodyStall struct{ timer *time.Timer }

// arrived notes that bytes arrived: the wait starts, or starts over, and
// pass runs once it ends.
func (s *bodyStall) arrived(pass func()) {
	if s.timer == nil {
		s.timer = time.AfterFunc(bodyStallWait, pass)
		return
	}
	s.timer.Reset(bodyStallWait)
}

// stop stops the wait.
func (s *bodyStall) stop() {
	if s.timer != nil {
		s.timer.Stop()
	}
}

// passedBody is the view body of a message whose body passed the threshold:
// what arrived before it did, up to the threshold. readBody reads it with
// the too-large ending.
type passedBody struct{ *bytes.Reader }

func (passedBody) Close() error { return nil }

// newPassedBody is the view body of a passed message: kept, what arrived
// before it passed, already cut at the threshold (cutAtThreshold).
func newPassedBody(kept []byte) io.ReadCloser {
	return passedBody{bytes.NewReader(kept)}
}

// Metadata fields of a direction whose body passed the threshold:
// <direction>_passed_through is true and <direction>_body_ending is
// "too large".
const (
	passedThroughMetadataSuffix = "_passed_through"
	bodyEndingMetadataSuffix    = "_body_ending"
)

// markPassedThrough records in the pair's metadata that direction
// ("request" or "response") was forwarded as it arrived.
func markPassedThrough(pair *Pair, direction string) {
	if pair.Metadata == nil {
		pair.Metadata = make(map[string]any)
	}
	pair.Metadata[direction+passedThroughMetadataSuffix] = true
	pair.Metadata[direction+bodyEndingMetadataSuffix] = BodyTooLarge.String()
}

// logPassedThrough names in a proxy log what the pair's passed body
// skipped: a Checkpoint hold or a Lua body edit.
func (proxy *Proxy) logPassedThrough(pair *Pair, direction, skipped string) {
	target := direction
	if req := pair.Request; req != nil && req.URL != nil {
		target = fmt.Sprintf("%s to %s %s", direction, req.Method, req.URL.Redacted())
	}
	message := fmt.Sprintf("%s skipped for the %s: its body passed the body threshold and is forwarded as it arrives", skipped, target)
	switch {
	case pair.ID == uuid.Nil:
		proxy.WriteLog("WARN", message)
	case pair.requestCaptured:
		proxy.WriteLog("WARN", message, core.LogWithReqResID(pair.ID))
	default:
		// The log's request link is a foreign key, and the request chain
		// has not stored the request yet: hold the log until it ends.
		pair.heldLogs = append(pair.heldLogs, heldLog{level: "WARN", message: message})
	}
}

// heldLog is a proxy log about a pair whose request was not yet stored.
type heldLog struct {
	level   string
	message string
}

// writeHeldLogs writes the logs held while the request chain ran. Each is
// linked to the request when the chain stored it, and otherwise names the
// request only in its context.
func (proxy *Proxy) writeHeldLogs(pair *Pair) {
	for _, held := range pair.heldLogs {
		option := core.LogWithContext(map[string]any{"request_id": pair.ID.String()})
		if pair.requestCaptured {
			option = core.LogWithReqResID(pair.ID)
		}
		if err := proxy.WriteLog(held.level, held.message, option); err != nil {
			log.Printf("writing a held proxy log: %v", err)
		}
	}
	pair.heldLogs = nil
}

// keepPassedBody guards a passed body against a Lua hook's edit: call it
// before the hooks run and the function it returns after. An edit is
// undone and named in a proxy log.
func (proxy *Proxy) keepPassedBody(pair *Pair, body *io.ReadCloser, direction string) func() {
	before, _ := readViewBody(body)
	return func() {
		after, err := readViewBody(body)
		if err == nil && bytes.Equal(before, after) {
			return
		}
		*body = io.NopCloser(bytes.NewReader(before))
		proxy.logPassedThrough(pair, direction, "A Lua body edit was")
	}
}
