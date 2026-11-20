package marasi

import (
	"bytes"
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/stream"
)

// handle is what happens to a whole stream message between being read and
// being forwarded: the processStreamMessage hooks see and may change it, the
// declared-length rule undoes a change the direction cannot carry,
// Checkpoint may hold it, and it is stored as it goes out. It returns the
// bytes to forward: the message as it came when nothing changed it, nil
// when it was dropped.
//
// It runs on the direction's one reader, so nothing more is read from the
// sender until it returns: a held message stops only its own direction.
// cutOff closes when the direction is reset or cut off, which abandons a
// hold. Injection extends it.
func (sp *streamedPair) handle(name string, d *streamedDirection, message *stream.Message, cutOff <-chan struct{}) []byte {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	live := &stream.LiveMessage{
		ID: id, PairID: sp.pair.ID, Direction: name, Framing: d.framing, ContentType: sp.contentType(d),
		Flags: message.Flags, Payload: message.Payload, Metadata: map[string]any{},
	}
	processStreamMessageExtensions(sp.proxy, sp.pair, live)
	intercept := !live.Dropped && shouldInterceptStreamMessage(sp.proxy, live)

	if sp.declaresLength(d) && (live.Dropped || len(live.Payload) != len(message.Payload)) {
		sp.lengthRefused(name, live.Dropped)
		live.Dropped, live.Payload = false, message.Payload
	}
	if intercept && !live.Dropped {
		live.Metadata["intercepted"] = true
		decision := sp.proxy.holdStreamMessage(sp, d, live, cutOff)
		switch {
		case decision.dropped:
			live.Dropped = true
		case decision.payload != nil:
			live.Payload = decision.payload
		}
	}

	raw := message.Raw
	switch {
	case live.Dropped:
		live.Metadata["dropped"] = true
		raw = nil
	case !bytes.Equal(live.Payload, message.Payload):
		live.Metadata["edited"] = true
		raw = stream.Envelope(live.Flags, live.Payload)
	}

	sp.mu.Lock()
	d.messages++
	sp.mu.Unlock()
	flags := live.Flags
	sp.queue(sp.message(id, name, d, &flags, live.Payload, live.Metadata))
	return raw
}

// headWentOut records the content type of d's head as it went out.
func (sp *streamedPair) headWentOut(d *streamedDirection, contentType string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	d.contentType = contentType
}

// contentType is the content type of d's head as it went out.
func (sp *streamedPair) contentType(d *streamedDirection) string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return d.contentType
}

// declaresLength reports whether d's head declared a length.
func (sp *streamedPair) declaresLength(d *streamedDirection) bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return d.declared
}

// declareLength records that d's head declared its length, so a change to
// it cannot be carried. The engine calls it before the direction is carried.
func (sp *streamedPair) declareLength(d *streamedDirection) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	d.declared = true
}

// lengthRefused writes the proxy log for a hook's change that the
// declared-length rule ignored.
func (sp *streamedPair) lengthRefused(name string, dropped bool) {
	change := "an edit that changes its length was"
	if dropped {
		change = "a drop was"
	}
	entry := fmt.Sprintf("Stream message change ignored (%s): the %s head declares a length, so %s ignored and the message went out as it came", sp.requestName(), name, change)
	_ = sp.proxy.WriteLog("WARN", entry, pairLogOptions(sp.pair)...)
}

// requestName names the pair's request in a proxy log.
func (sp *streamedPair) requestName() string {
	if req := sp.pair.Request; req != nil && req.URL != nil {
		return fmt.Sprintf("request %s %s", req.Method, req.URL.Redacted())
	}
	return "request"
}

// processStreamMessageExtensions runs processStreamMessage in each enabled
// extension but checkpoint, and not in the extension that sent the pair.
// A drop or skip stops later extensions. An interrupted hook drops the
// message; another Lua error leaves it as it was.
func processStreamMessageExtensions(proxy *Proxy, pair *Pair, message *stream.LiveMessage) {
	for _, extension := range proxy.Extensions {
		if extension == nil || extension.Data == nil || !extension.MetadataSnapshot().Enabled || extension.Data.Name == "checkpoint" {
			continue
		}
		if pair != nil && pair.ExtensionID != uuid.Nil && pair.ExtensionID == extension.Data.ID {
			continue
		}
		if err := extension.CallStreamMessageHandler(message); err != nil {
			_ = proxy.WriteLog("ERROR", fmt.Sprintf("Running processStreamMessage : %s", err.Error()), core.LogWithExtensionID(extension.Data.ID))
			if hookInterrupted(context.Background(), extension) {
				message.Dropped = true
			}
		}
		if message.Dropped || message.Skipped {
			return
		}
	}
}

// shouldInterceptStreamMessage reports whether Checkpoint holds a stream
// message: the stream_intercept switch is on, or checkpoint's
// interceptStreamMessage returns true. A disabled checkpoint holds none,
// whatever the switch. An interrupted hook drops the message, as for
// WebSocket.
func shouldInterceptStreamMessage(proxy *Proxy, message *stream.LiveMessage) bool {
	checkpoint, ok := proxy.GetExtension("checkpoint")
	if ok && (checkpoint.Data == nil || !checkpoint.MetadataSnapshot().Enabled) {
		return false
	}
	if proxy.GetStreamIntercept() {
		return true
	}
	if !ok {
		return false
	}
	intercept, err := checkpoint.ShouldInterceptStreamMessage(message)
	if err != nil {
		_ = proxy.WriteLog("ERROR", fmt.Sprintf("Running interceptStreamMessage : %s", err.Error()), core.LogWithExtensionID(checkpoint.Data.ID))
		if hookInterrupted(context.Background(), checkpoint) {
			message.Dropped = true
		}
		return false
	}
	return intercept
}
