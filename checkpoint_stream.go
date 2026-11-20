package marasi

import (
	"bytes"
	"errors"
	"fmt"
	"maps"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// Checkpoint holds of stream messages (spec: Carrying a streamed pair,
// Control API). A stream message is held on its direction's one reader
// (streamedPair.handle), so nothing more is read from that sender while it
// is held, and the other direction keeps flowing. A hold ends with a
// forward, edited or not, a drop, or an abandon when the direction is reset
// or cut off, which drops every held message of the pair.

// ErrStreamLengthDeclared is returned for a Checkpoint edit or drop of a
// stream message whose direction's head declared a length that the change
// would alter. The message stays held.
var ErrStreamLengthDeclared = errors.New("the direction's head declares a length")

// pendingStream is a held stream message.
type pendingStream struct {
	item domain.CheckpointItem
	sp   *streamedPair
	d    *streamedDirection
	done chan struct{}
	// decision is what the hold ended with, set before done closes.
	decision streamHoldDecision
}

// streamHoldDecision is how a stream message's hold ended: dropped, or
// forwarded with payload when it was edited.
type streamHoldDecision struct {
	dropped bool
	payload []byte
}

// SetStreamIntercept enables or disables holding every stream message.
func (proxy *Proxy) SetStreamIntercept(enabled bool) {
	proxy.streamIntercept.Store(enabled)
}

// GetStreamIntercept reports whether every stream message is held.
func (proxy *Proxy) GetStreamIntercept() bool {
	return proxy.streamIntercept.Load()
}

// checkpointItemFromStream is the Checkpoint item of a held stream message.
func checkpointItemFromStream(message *stream.LiveMessage) domain.CheckpointItem {
	flags := message.Flags
	item := domain.CheckpointItem{
		ID:         message.ID,
		Type:       domain.CheckpointTypeStream,
		Payload:    bytes.Clone(message.Payload),
		Direction:  message.Direction,
		PairID:     message.PairID,
		Framing:    string(message.Framing),
		Compressed: message.Compressed(),
		Metadata:   maps.Clone(message.Metadata),
	}
	switch message.Framing {
	case stream.FramingGRPC, stream.FramingGRPCWeb, stream.FramingConnect:
		item.Flags = &flags
	}
	item.Text, item.HasText = message.Text()
	return item
}

// holdStreamMessage holds message until it is forwarded, dropped or
// abandoned, and returns how its hold ended. cutOff closing abandons it.
func (proxy *Proxy) holdStreamMessage(sp *streamedPair, d *streamedDirection, message *stream.LiveMessage, cutOff <-chan struct{}) streamHoldDecision {
	state := proxy.checkpoint
	item := checkpointItemFromStream(message)
	item.InterceptedAt = state.now()
	pending := &pendingStream{item: item, sp: sp, d: d, done: make(chan struct{})}

	state.mu.Lock()
	if proxy.engine != nil && proxy.engine.closing.Load() {
		// The proxy is closing and has dropped what Checkpoint held, or is
		// about to: nothing would resolve a new hold. Closing sets the
		// engine's flag before it drops, so a hold either starts before the
		// drop or not at all.
		state.mu.Unlock()
		return streamHoldDecision{dropped: true}
	}
	state.stream[item.ID] = pending
	state.order = append(state.order, item.ID)
	state.signalHoldStarted()
	notify := proxy.OnIntercept
	state.mu.Unlock()

	if notify != nil {
		_ = notify(cloneCheckpointItem(item))
	}

	select {
	case <-pending.done:
	case <-cutOff:
		proxy.abandonStreamPair(sp)
		<-pending.done
	}
	return pending.decision
}

// takeStream removes the held stream message id and reports it, or nil
// when it is not held. The caller holds the state's lock.
func (state *checkpointState) takeStream(id uuid.UUID) *pendingStream {
	pending, ok := state.stream[id]
	if !ok {
		return nil
	}
	delete(state.stream, id)
	state.order = removeCheckpointID(state.order, id)
	return pending
}

// forwardStream forwards a held stream message, with fwd.Payload when set.
// An edit that changes the length of a direction whose head declared one is
// refused, and the message stays held.
func (proxy *Proxy) forwardStream(id uuid.UUID, fwd CheckpointForward) error {
	state := proxy.checkpoint
	state.mu.Lock()
	pending, ok := state.stream[id]
	if !ok {
		state.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	if fwd.Payload != nil && len(*fwd.Payload) != len(pending.item.Payload) && pending.sp.declaresLength(pending.d) {
		state.mu.Unlock()
		return fmt.Errorf("%w (%s): the %s head declares a length, so an edit that changes the payload's length from %d to %d bytes cannot be carried",
			ErrStreamLengthDeclared, id, pending.item.Direction, len(pending.item.Payload), len(*fwd.Payload))
	}
	state.takeStream(id)
	state.mu.Unlock()
	if fwd.Payload != nil {
		pending.decision.payload = bytes.Clone(*fwd.Payload)
	}
	close(pending.done)
	return nil
}

// dropStream drops a held stream message. A drop on a direction whose head
// declared a length is refused, and the message stays held.
func (proxy *Proxy) dropStream(id uuid.UUID) error {
	state := proxy.checkpoint
	state.mu.Lock()
	pending, ok := state.stream[id]
	if !ok {
		state.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	if pending.sp.declaresLength(pending.d) {
		state.mu.Unlock()
		return fmt.Errorf("%w (%s): the %s head declares a length, so a drop cannot be carried", ErrStreamLengthDeclared, id, pending.item.Direction)
	}
	state.takeStream(id)
	state.mu.Unlock()
	pending.decision.dropped = true
	close(pending.done)
	return nil
}

// dropAllStreams drops every held stream message and returns their items.
// The caller holds the state's lock.
func (state *checkpointState) dropAllStreams() []*pendingStream {
	dropped := make([]*pendingStream, 0, len(state.stream))
	for id, pending := range state.stream {
		delete(state.stream, id)
		state.order = removeCheckpointID(state.order, id)
		pending.decision.dropped = true
		dropped = append(dropped, pending)
	}
	return dropped
}

// abandonStreamPair drops every held stream message of sp's pair and
// reports each through OnCheckpointAbandoned
// (docs/adr/2026-10-13-checkpoint-hold-ends-when-its-client-leaves.md). A
// forward or drop that took a message first wins.
func (proxy *Proxy) abandonStreamPair(sp *streamedPair) {
	state := proxy.checkpoint
	state.mu.Lock()
	var abandoned []*pendingStream
	for id, pending := range state.stream {
		if pending.sp != sp {
			continue
		}
		state.takeStream(id)
		pending.decision.dropped = true
		abandoned = append(abandoned, pending)
	}
	notify := proxy.OnCheckpointAbandoned
	state.mu.Unlock()
	for _, pending := range abandoned {
		close(pending.done)
		if notify != nil {
			_ = notify(cloneCheckpointItem(pending.item))
		}
	}
}
