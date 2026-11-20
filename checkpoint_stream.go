package marasi

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// Checkpoint holds of stream messages and trailer blocks (spec: Carrying a
// streamed pair, Control API). A stream message is held on its direction's one reader
// (streamedPair.handle), so nothing more is read from that sender while it
// is held, and the other direction keeps flowing. A hold ends with a
// forward, edited or not, a drop, or an abandon when the direction is reset
// or cut off, which drops every held message of the pair. A direction's
// trailers, and a gRPC-Web trailers frame, are held the same way as a
// trailer item, edited as a "name: value" line per field.

// ErrStreamLengthDeclared is returned for a Checkpoint edit or drop of a
// stream message whose direction's head declared a length that the change
// would alter. The message stays held.
var ErrStreamLengthDeclared = errors.New("the direction's head declares a length")

// ErrStreamReadOnly is returned for a Checkpoint edit of a stream message
// compressed with an encoding Marasi does not decode. The message stays
// held.
var ErrStreamReadOnly = errors.New("the stream message cannot be edited")

// ErrTrailerEdit is returned for a Checkpoint edit of a trailer item with
// a line that is not a field. The trailers stay held.
var ErrTrailerEdit = errors.New("the trailer block does not parse")

// pendingStream is a held stream message or trailer block.
type pendingStream struct {
	item domain.CheckpointItem
	sp   *streamedPair
	done chan struct{}
	// refuse says why a forward with payload, or a drop, cannot be
	// carried, or nil when it can (streamedPair.handle).
	refuse func(payload []byte, drop bool) error
	// decision is what the hold ended with, set before done closes.
	decision streamHoldDecision
}

// streamHoldDecision is how a stream message's hold ended: dropped, or
// forwarded with payload when it was edited; abandoned when the direction
// was reset or cut off under it.
type streamHoldDecision struct {
	dropped   bool
	abandoned bool
	payload   []byte
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
	if !message.ReadOnly {
		item.Text, item.HasText = message.Text()
	}
	return item
}

// holdStreamMessage holds message until it is forwarded, dropped or
// abandoned, and returns how its hold ended. refuse vets a forward's
// payload or a drop first. cutOff closing abandons the hold. A gRPC-Web
// trailers frame is held as a trailer item, its payload the trailer block.
func (proxy *Proxy) holdStreamMessage(sp *streamedPair, message *stream.LiveMessage, refuse func(payload []byte, drop bool) error, cutOff <-chan struct{}) streamHoldDecision {
	item := checkpointItemFromStream(message)
	if stream.IsTrailersFrame(message.Framing, message.Flags) {
		item.Type = domain.CheckpointTypeTrailer
		item.Raw, item.Payload = item.Payload, nil
		item.Text, item.HasText = "", false
	}
	decision, held := proxy.holdStreamItem(sp, item, refuse, cutOff)
	if !held {
		// Nothing would resolve the hold.
		return streamHoldDecision{dropped: true}
	}
	return decision
}

// holdTrailers holds a streamed direction's trailers as a trailer item
// until it is forwarded, edited or not, dropped or abandoned, and returns
// the trailers to send: nil when it was dropped. cutOff closing abandons
// the hold, and abandoned reports it.
func (sp *streamedPair) holdTrailers(name string, d *streamedDirection, trailers h2Fields, cutOff <-chan struct{}) (out h2Fields, abandoned bool) {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	item := domain.CheckpointItem{
		ID: id, Type: domain.CheckpointTypeTrailer, Raw: trailerBlock(trailers),
		Direction: name, PairID: sp.pair.ID, Framing: string(d.framing), Metadata: map[string]any{},
	}
	refuse := func(raw []byte, drop bool) error {
		if drop || raw == nil {
			return nil
		}
		_, err := parseTrailerBlock(raw)
		return err
	}
	decision, held := sp.proxy.holdStreamItem(sp, item, refuse, cutOff)
	switch {
	case !held:
		return trailers, false
	case decision.abandoned:
		return nil, true
	case decision.dropped:
		return nil, false
	case decision.payload != nil:
		// refuse parsed it before the hold ended.
		edited, _ := parseTrailerBlock(decision.payload)
		return edited, false
	}
	return trailers, false
}

// holdStreamItem holds a stream or trailer item until it is forwarded,
// dropped or abandoned, and returns how its hold ended. It reports false,
// holding nothing, once the proxy is closing.
func (proxy *Proxy) holdStreamItem(sp *streamedPair, item domain.CheckpointItem, refuse func(payload []byte, drop bool) error, cutOff <-chan struct{}) (streamHoldDecision, bool) {
	state := proxy.checkpoint
	item.InterceptedAt = state.now()
	pending := &pendingStream{item: item, sp: sp, refuse: refuse, done: make(chan struct{})}

	state.mu.Lock()
	if proxy.engine != nil && proxy.engine.closing.Load() {
		// The proxy is closing and has dropped what Checkpoint held, or is
		// about to: nothing would resolve a new hold. Closing sets the
		// engine's flag before it drops, so a hold either starts before the
		// drop or not at all.
		state.mu.Unlock()
		return streamHoldDecision{}, false
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
	return pending.decision, true
}

// trailerBlock is trailers as a trailer item shows them: one "name: value"
// line per field, each ending in CRLF, as a gRPC-Web trailers frame
// carries them.
func trailerBlock(trailers h2Fields) []byte {
	var block bytes.Buffer
	for _, f := range trailers {
		block.WriteString(f.Name)
		block.WriteString(": ")
		block.WriteString(f.Value)
		block.WriteString("\r\n")
	}
	return block.Bytes()
}

// parseTrailerBlock reads an edited trailer block: a "name: value" line per
// field, ending in CRLF or LF, blank lines skipped. Names and values go out
// as typed.
func parseTrailerBlock(raw []byte) (h2Fields, error) {
	var fields h2Fields
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || name == "" {
			return nil, fmt.Errorf("%w: %q is not a \"name: value\" line", ErrTrailerEdit, line)
		}
		fields = append(fields, h2Field{Name: name, Value: strings.TrimLeft(value, " \t")})
	}
	return fields, nil
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

// forwardStream forwards a held stream message, with fwd.Payload when set,
// or a held trailer block, with fwd.Raw when set. An edit the item cannot
// carry is refused with the reason, and the item stays held.
func (proxy *Proxy) forwardStream(id uuid.UUID, fwd CheckpointForward) error {
	state := proxy.checkpoint
	state.mu.Lock()
	pending, ok := state.stream[id]
	if !ok {
		state.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	edit := fwd.Payload
	if pending.item.Type == domain.CheckpointTypeTrailer {
		edit = fwd.Raw
	}
	if edit != nil {
		if err := pending.refuse(*edit, false); err != nil {
			state.mu.Unlock()
			return fmt.Errorf("%s: %w", id, err)
		}
	}
	state.takeStream(id)
	state.mu.Unlock()
	if edit != nil {
		pending.decision.payload = bytes.Clone(*edit)
	}
	close(pending.done)
	return nil
}

// dropStream drops a held stream message. A drop the direction cannot
// carry, because its head declared a length, is refused, and the message
// stays held.
func (proxy *Proxy) dropStream(id uuid.UUID) error {
	state := proxy.checkpoint
	state.mu.Lock()
	pending, ok := state.stream[id]
	if !ok {
		state.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	if err := pending.refuse(nil, true); err != nil {
		state.mu.Unlock()
		return fmt.Errorf("%s: %w", id, err)
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
		pending.decision.abandoned = true
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
