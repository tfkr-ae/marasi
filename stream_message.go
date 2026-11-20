package marasi

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// handle is what happens to a whole stream message between being read and
// being forwarded: a compressed one is decoded, the processStreamMessage
// hooks see and may change it, the declared-length rule undoes a change the
// direction cannot carry, Checkpoint may hold it, and it is stored as it
// goes out. It returns the bytes to forward: the message as it came when
// nothing changed it, nil when it was dropped. A compressed message the
// proxy cannot decode is stored with its inflate error and returned as a
// *messageDecodeError, so the carry resets the stream.
//
// It runs on the direction's one reader, so nothing more is read from the
// sender until it returns: a held message stops only its own direction.
// cutOff closes when the direction is reset or cut off, which abandons a
// hold.
func (sp *streamedPair) handle(name string, d *streamedDirection, message *stream.Message, cutOff <-chan struct{}) ([]byte, error) {
	raw, _, err := sp.process(name, d, message, cutOff, nil)
	return raw, err
}

// process is handle, for a message that comes with metadata, an injected
// one's, and it also returns the message as it was stored.
func (sp *streamedPair) process(name string, d *streamedDirection, message *stream.Message, cutOff <-chan struct{}, metadata map[string]any) ([]byte, domain.StreamMessage, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	live := &stream.LiveMessage{
		ID: id, PairID: sp.pair.ID, Direction: name, Framing: d.framing, ContentType: sp.contentType(d),
		Flags: message.Flags, Payload: message.Payload, Encoding: d.encoding, Metadata: metadata,
	}
	compressed := live.Compressed()
	if compressed {
		live.Metadata["encoding"] = d.encoding
		if !stream.Decodable(d.encoding) {
			live.ReadOnly = true
		} else if live.Payload, err = stream.Decompress(d.encoding, message.Payload, sp.limit); err != nil {
			return nil, domain.StreamMessage{}, sp.undecodable(name, d, id, message, err)
		}
	}
	original := live.Payload
	processStreamMessageExtensions(sp.proxy, sp.pair, live)
	intercept := !live.Dropped && shouldInterceptStreamMessage(sp.proxy, live)

	out := outgoing(d, message, original)
	raw, flagsOut, edited := out(live.Payload)
	if edited && live.ReadOnly {
		sp.readOnlyRefused(name, d.encoding)
		live.Payload = original
		raw, flagsOut, edited = out(original)
	}
	declared := sp.declaresLength(d)
	if declared && (live.Dropped || len(raw) != len(message.Raw)) {
		sp.lengthRefused(name, live.Dropped)
		live.Dropped, live.Payload = false, original
		raw, flagsOut, edited = out(original)
	}
	if intercept && !live.Dropped {
		live.Metadata["intercepted"] = true
		// refuse is what Checkpoint checks before a forward or drop ends
		// the hold, so a refused change leaves the message held.
		readOnly := live.ReadOnly
		refuse := func(payload []byte, drop bool) error {
			switch {
			case drop && declared:
				return fmt.Errorf("%w: the %s head declares a length, so a drop cannot be carried", ErrStreamLengthDeclared, name)
			case drop || payload == nil:
				return nil
			case readOnly && !bytes.Equal(payload, original):
				return fmt.Errorf("%w: the %s message is compressed with %q, which Marasi does not decode", ErrStreamReadOnly, name, d.encoding)
			}
			if wire, _, _ := out(payload); declared && len(wire) != len(message.Raw) {
				return fmt.Errorf("%w: the %s head declares a length, so an edit that makes the message %d bytes on the wire instead of %d cannot be carried",
					ErrStreamLengthDeclared, name, len(wire), len(message.Raw))
			}
			return nil
		}
		decision := sp.proxy.holdStreamMessage(sp, live, refuse, cutOff)
		switch {
		case decision.dropped:
			live.Dropped = true
		case decision.payload != nil:
			live.Payload = decision.payload
			raw, flagsOut, edited = out(live.Payload)
		}
	}

	switch {
	case live.Dropped:
		live.Metadata["dropped"] = true
		raw = nil
	case edited:
		live.Metadata["edited"] = true
		if compressed {
			compressed = false
			delete(live.Metadata, "encoding")
		}
	}

	sp.mu.Lock()
	d.messages++
	sp.mu.Unlock()
	var flags *uint8
	if d.framing.Enveloped() {
		flags = &flagsOut
	}
	row := sp.message(id, name, d, flags, live.Payload, live.Metadata)
	row.Compressed = compressed
	sp.queue(row)
	return raw, row, nil
}

// outgoing returns what message goes out as with a payload: the bytes it
// came in and its flags when the payload is original, the payload it was
// read as; otherwise the payload uncompressed, in a new envelope with its
// compressed flag cleared where the framing has one, reported edited.
func outgoing(d *streamedDirection, message *stream.Message, original []byte) func(payload []byte) (raw []byte, flags byte, edited bool) {
	return func(payload []byte) ([]byte, byte, bool) {
		if bytes.Equal(payload, original) {
			return message.Raw, message.Flags, false
		}
		if !d.framing.Enveloped() {
			return payload, message.Flags, true
		}
		flags := stream.Uncompressed(message.Flags)
		return stream.Envelope(flags, payload), flags, true
	}
}

// messageDecodeError is a compressed stream message the proxy could not
// decode, so it cannot forward it.
type messageDecodeError struct{ err error }

func (e *messageDecodeError) Error() string { return "decoding a stream message: " + e.err.Error() }

// tooLarge reports whether the message decoded past the per-message limit.
func (e *messageDecodeError) tooLarge() bool { return strings.HasPrefix(e.err.Error(), "too large") }

// undecodable stores a compressed message the proxy could not decode as the
// compressed bytes it came as, with the error in inflate_error, writes a
// proxy log and records the direction reset. No hook sees it, since it
// cannot be edited or forwarded.
func (sp *streamedPair) undecodable(name string, d *streamedDirection, id uuid.UUID, message *stream.Message, err error) error {
	decodeErr := &messageDecodeError{err: err}
	code := http2.ErrCodeInternal
	if decodeErr.tooLarge() {
		code = http2.ErrCodeEnhanceYourCalm
	}
	sp.mu.Lock()
	d.messages++
	sp.mu.Unlock()
	flags := message.Flags
	row := sp.message(id, name, d, &flags, message.Payload, map[string]any{"encoding": d.encoding, "inflate_error": err.Error()})
	row.Compressed = true
	sp.queue(row)
	sp.end(d, streamReset, code)
	entry := fmt.Sprintf("Stream message not decoded (%s): a %s message compressed with %q could not be decoded (%v); the stream was reset", sp.requestName(), name, d.encoding, err)
	_ = sp.proxy.WriteLog("WARN", entry, pairLogOptions(sp.pair)...)
	return decodeErr
}

// readOnlyRefused writes the proxy log for a hook's edit to a message
// compressed with an encoding the proxy does not decode.
func (sp *streamedPair) readOnlyRefused(name, encoding string) {
	entry := fmt.Sprintf("Stream message change ignored (%s): the %s message is compressed with %q, which Marasi does not decode, so it cannot be edited and went out as it came", sp.requestName(), name, encoding)
	_ = sp.proxy.WriteLog("WARN", entry, pairLogOptions(sp.pair)...)
}

// headWentOut records the content type and the per-message encoding of d's
// head as it went out, with fields. The engine calls it before the direction
// is carried.
func (sp *streamedPair) headWentOut(d *streamedDirection, fields h2Fields) {
	contentType, _ := fields.get("content-type")
	var encoding string
	if header := stream.EncodingHeader(d.framing); header != "" {
		for _, f := range fields {
			if strings.EqualFold(f.Name, header) {
				encoding = f.Value
				break
			}
		}
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	d.contentType, d.encoding = contentType, encoding
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
