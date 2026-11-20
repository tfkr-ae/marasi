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
// declared-length rule undoes a change the direction cannot carry, and it
// is stored as it goes out. It returns the bytes to forward: the message as
// it came when nothing changed it, nil when it was dropped.
//
// It runs on the direction's one reader, so nothing more is read from the
// sender until it returns. Checkpoint holds and injection extend it.
func (sp *streamedPair) handle(name string, d *streamedDirection, message *stream.Message) []byte {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	live := &stream.LiveMessage{
		ID: id, PairID: sp.pair.ID, Direction: name, Framing: d.framing,
		Flags: message.Flags, Payload: message.Payload, Metadata: map[string]any{},
	}
	processStreamMessageExtensions(sp.proxy, sp.pair, live)

	edited := !bytes.Equal(live.Payload, message.Payload)
	if sp.declaresLength(d) && (live.Dropped || len(live.Payload) != len(message.Payload)) {
		sp.lengthRefused(name, live.Dropped)
		live.Dropped, live.Payload, edited = false, message.Payload, false
	}
	raw := message.Raw
	switch {
	case live.Dropped:
		live.Metadata["dropped"] = true
		raw = nil
	case edited:
		live.Metadata["edited"] = true
		raw = live.Payload
		if d.framing.Enveloped() {
			raw = stream.Envelope(live.Flags, live.Payload)
		}
	}

	sp.mu.Lock()
	d.messages++
	sp.mu.Unlock()
	var flags *uint8
	if d.framing.Enveloped() {
		f := live.Flags
		flags = &f
	}
	sp.queue(sp.message(id, name, d, flags, live.Payload, live.Metadata))
	return raw
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
