package marasi

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/stream"
)

// handle is what happens to a whole stream message between being read and
// being forwarded: a compressed one is decoded, the processStreamMessage
// hooks see and may change it, the declared-length rule undoes a change the
// direction cannot carry, and it is stored as it goes out. It returns the
// bytes to forward: the message as it came when nothing changed it, nil
// when it was dropped. A compressed message the proxy cannot decode is
// stored with its inflate error and returned as a *messageDecodeError, so
// the carry resets the stream.
//
// It runs on the direction's one reader, so nothing more is read from the
// sender until it returns. Checkpoint holds and injection extend it.
func (sp *streamedPair) handle(name string, d *streamedDirection, message *stream.Message) ([]byte, error) {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	live := &stream.LiveMessage{
		ID: id, PairID: sp.pair.ID, Direction: name, Framing: d.framing,
		Flags: message.Flags, Payload: message.Payload, Encoding: d.encoding, Metadata: map[string]any{},
	}
	compressed := live.Compressed()
	if compressed {
		live.Metadata["encoding"] = d.encoding
		if !stream.Decodable(d.encoding) {
			live.ReadOnly = true
		} else if live.Payload, err = stream.Decompress(d.encoding, message.Payload, sp.limit); err != nil {
			return nil, sp.undecodable(name, d, id, message, err)
		}
	}
	original := live.Payload
	processStreamMessageExtensions(sp.proxy, sp.pair, live)

	edited := !bytes.Equal(live.Payload, original)
	if edited && live.ReadOnly {
		sp.readOnlyRefused(name, d.encoding)
		live.Payload, edited = original, false
	}
	// An edited message goes out uncompressed, its compressed flag cleared.
	flagsOut, raw := live.Flags, message.Raw
	if edited {
		raw = live.Payload
		if d.framing.Enveloped() {
			flagsOut = stream.Uncompressed(live.Flags)
			raw = stream.Envelope(flagsOut, live.Payload)
		}
	}
	if sp.declaresLength(d) && (live.Dropped || len(raw) != len(message.Raw)) {
		sp.lengthRefused(name, live.Dropped)
		live.Dropped, live.Payload, edited = false, original, false
		flagsOut, raw = live.Flags, message.Raw
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
	return raw, nil
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
