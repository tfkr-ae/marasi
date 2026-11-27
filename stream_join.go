package marasi

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
	"github.com/tfkr-ae/marasi/stream"
)

// A streamed pair's request and response are stored as their heads with empty
// bodies; their bodies went out as stream messages. JoinedRequest puts a
// streamed request back together in its envelopes so a Launchpad entry or a
// copy of the pair replays the call. OpenJoinedSSEResponse streams an ended
// SSE response head followed by its forwarded decoded SSE events in order for
// inspection.

var (
	// ErrStreamedRequestOpen means the pair's request direction is still being
	// carried, so its body is not yet whole.
	ErrStreamedRequestOpen = errors.New("the streamed pair's request direction is still open")
	// ErrStreamedResponseOpen means the pair's SSE response direction is still
	// live or has not finished persisting its response capture.
	ErrStreamedResponseOpen = errors.New("the streamed pair's SSE response direction is still open")
	// ErrResponseNotSSE means the pair has no stored SSE response.
	ErrResponseNotSSE = errors.New("the pair has no SSE response")
)

// JoinedRequest is a pair's raw request with its body as it went to the
// origin.
type JoinedRequest struct {
	ID uuid.UUID
	// Raw is the stored request head followed by the body. For a pair
	// that is not a streamed pair, or whose request direction is not
	// streamed, it is the stored raw request unchanged.
	Raw []byte
	// Streamed reports that the body was joined from the request's stream
	// messages.
	Streamed bool
	// Messages is how many request messages the body joins, and Omitted
	// how many stored request messages it leaves out because they never
	// reached the origin whole: dropped, too large, cut short or not
	// decoded.
	Messages int
	Omitted  int
	// head is the joined request's head, its blank line included, and
	// forwarded the joined request messages as stored, in order.
	head      []byte
	forwarded []*domain.StreamMessage
}

// joinPageSize is how many stream messages JoinedRequest reads at a time.
const joinPageSize = 500

// JoinedRequest returns pair id's request with a streamed request
// direction's messages joined into its body, each in its envelope, as a
// client sends them:
//
//   - A message is joined as it was forwarded, after hooks and Checkpoint:
//     an edited message as edited, an injected one included. A dropped,
//     too large or cut short message, or one that could not be decoded,
//     never reached the origin whole and is left out.
//   - A message the proxy stored decompressed is compressed again with its
//     recorded encoding (gzip or deflate), its compressed flag kept; one
//     stored as the compressed bytes, an encoding Marasi does not decode,
//     is joined as stored. The compressed bytes can differ from the
//     original's; the decoded payload is the same.
//   - An HTTP/1.1 head loses Transfer-Encoding and gets a Content-Length of
//     the joined body; an HTTP/2 head keeps its fields, and a content-length
//     it has is set to the joined body's length.
//
// A pair whose request direction is still being carried is refused with
// ErrStreamedRequestOpen; once the request direction ended, the pair joins
// even while its response is live. A pair that is not stored returns an
// error wrapping domain.ErrPairNotFound.
func (proxy *Proxy) JoinedRequest(id uuid.UUID) (JoinedRequest, error) {
	repo, err := proxy.GetTrafficRepo()
	if err != nil {
		return JoinedRequest{}, err
	}
	streamed := false
	if sp, live := proxy.liveStreamedPair(id); live {
		sp.mu.Lock()
		d := sp.directions[domain.StreamDirectionRequest]
		open := d != nil && d.ending == ""
		streamed = d != nil
		sp.mu.Unlock()
		if open {
			return JoinedRequest{}, ErrStreamedRequestOpen
		}
	}
	// What the carry queued, its messages and its streamed state, is
	// stored before it is read.
	if err := proxy.flushDBWrites(); err != nil {
		return JoinedRequest{}, err
	}
	row, err := repo.GetRequestResponseRow(id)
	if err != nil {
		return JoinedRequest{}, fmt.Errorf("%w: %w", domain.ErrPairNotFound, err)
	}
	joined := JoinedRequest{ID: id, Raw: row.Request.Raw}
	if row.Streamed != nil && row.Streamed.Request != nil {
		streamed = true
	}
	if !streamed || proxy.StreamRepo == nil {
		return joined, nil
	}
	var body bytes.Buffer
	var cursor *uuid.UUID
	for {
		messages, next, err := proxy.StreamRepo.ListStreamMessages(id, cursor, joinPageSize)
		if err != nil {
			return JoinedRequest{}, fmt.Errorf("reading the request messages of %s: %w", id, err)
		}
		for _, message := range messages {
			if message.Direction != domain.StreamDirectionRequest {
				continue
			}
			wire, ok, err := forwardedMessage(message)
			if err != nil {
				return JoinedRequest{}, err
			}
			if !ok {
				joined.Omitted++
				continue
			}
			body.Write(wire)
			joined.Messages++
			joined.forwarded = append(joined.forwarded, message)
		}
		if next == nil {
			break
		}
		cursor = next
	}
	joined.Raw = withBody(row.Request.Raw, body.Bytes())
	joined.head = joined.Raw[:len(joined.Raw)-body.Len()]
	joined.Streamed = true
	return joined, nil
}

// JoinedSSEResponse streams a pair's stored SSE response head followed by its
// forwarded decoded SSE event payloads in stream order, reading stream
// messages one page at a time.
type JoinedSSEResponse struct {
	ID         uuid.UUID
	head       []byte
	streamRepo domain.StreamRepository
	firstPage  []*domain.StreamMessage
	firstNext  *uuid.UUID
	hasFirst   bool
}

var _ io.WriterTo = (*JoinedSSEResponse)(nil)

// OpenJoinedSSEResponse validates that pair id has an ended SSE response in
// the open project and returns a JoinedSSEResponse that streams the stored
// status line, headers, blank line, and forwarded decoded SSE events in order.
//
// While the SSE response direction is live (or before its deferred response
// capture is queued), OpenJoinedSSEResponse returns ErrStreamedResponseOpen.
// A pair without a stored SSE response returns ErrResponseNotSSE. A missing
// pair wraps domain.ErrPairNotFound, and an unavailable project returns
// ErrExtensionRepoNotFound.
func (proxy *Proxy) OpenJoinedSSEResponse(id uuid.UUID) (*JoinedSSEResponse, error) {
	trafficRepo, err := proxy.GetTrafficRepo()
	if err != nil {
		return nil, err
	}
	streamRepo := proxy.StreamRepo
	if streamRepo == nil {
		return nil, ErrExtensionRepoNotFound
	}
	if sp, live := proxy.liveStreamedPair(id); live {
		sp.mu.Lock()
		d := sp.directions[domain.StreamDirectionResponse]
		open := sp.announced() && d != nil && d.framing == stream.FramingSSE && !sp.finished
		sp.mu.Unlock()
		if open {
			return nil, ErrStreamedResponseOpen
		}
	}
	// What the carry and finish queued, its events, streamed state, and
	// deferred response head, is stored before it is read.
	if err := proxy.flushDBWrites(); err != nil {
		return nil, err
	}
	row, err := trafficRepo.GetRequestResponseRow(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrPairNotFound) {
			return nil, fmt.Errorf("%w: %w", domain.ErrPairNotFound, err)
		}
		return nil, fmt.Errorf("reading traffic pair %s: %w", id, err)
	}
	if row.Streamed == nil || row.Streamed.Response == nil || row.Streamed.Response.Framing != string(stream.FramingSSE) {
		return nil, ErrResponseNotSSE
	}
	if row.Streamed.Response.Ending == "" {
		return nil, ErrStreamedResponseOpen
	}
	if len(row.Response.Raw) == 0 {
		return nil, ErrResponseNotSSE
	}
	messages, next, err := streamRepo.ListStreamMessages(id, nil, joinPageSize)
	if err != nil {
		return nil, fmt.Errorf("reading the response messages of %s: %w", id, err)
	}
	return &JoinedSSEResponse{
		ID:         id,
		head:       storedResponseHead(row.Response.Raw),
		streamRepo: streamRepo,
		firstPage:  messages,
		firstNext:  next,
		hasFirst:   true,
	}, nil
}

// WriteTo writes the stored SSE response head, blank line, and each forwarded
// SSE event payload to w incrementally, one database page at a time.
func (r *JoinedSSEResponse) WriteTo(w io.Writer) (int64, error) {
	return r.WriteToContext(context.Background(), w)
}

// WriteToContext writes the stored SSE response head, blank line, and each
// forwarded SSE event payload to w incrementally, stopping if ctx ends.
func (r *JoinedSSEResponse) WriteToContext(ctx context.Context, w io.Writer) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	written, err := w.Write(r.head)
	total := int64(written)
	if err != nil {
		return total, err
	}
	var cursor *uuid.UUID
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var messages []*domain.StreamMessage
		var next *uuid.UUID
		if r.hasFirst {
			messages, next = r.firstPage, r.firstNext
			r.firstPage, r.firstNext, r.hasFirst = nil, nil, false
		} else {
			messages, next, err = r.streamRepo.ListStreamMessages(r.ID, cursor, joinPageSize)
			if err != nil {
				return total, fmt.Errorf("reading the response messages of %s: %w", r.ID, err)
			}
		}
		for _, message := range messages {
			if message.Direction != domain.StreamDirectionResponse || !messageForwarded(message) || len(message.Payload) == 0 {
				continue
			}
			n, err := w.Write(message.Payload)
			total += int64(n)
			if err != nil {
				return total, err
			}
		}
		if next == nil {
			return total, nil
		}
		cursor = next
	}
}

// storedResponseHead returns the stored status line, headers, and blank line
// of raw without altering header fields.
func storedResponseHead(raw []byte) []byte {
	if end := bytes.Index(raw, []byte("\r\n\r\n")); end >= 0 {
		return raw[:end+4]
	}
	head := bytes.TrimSuffix(raw, []byte("\r\n"))
	out := make([]byte, len(head)+4)
	copy(out, head)
	copy(out[len(head):], "\r\n\r\n")
	return out
}

// messageForwarded reports whether message was forwarded whole: dropped,
// oversized, cut-short, and undecodable messages never reached the peer whole.
func messageForwarded(message *domain.StreamMessage) bool {
	for _, key := range []string{"dropped", "too_large", "cut_short", "inflate_error"} {
		if _, ok := message.Metadata[key]; ok {
			return false
		}
	}
	return true
}

// forwardedMessage is message as it went to the origin, in its envelope, and
// false for a message that never reached it whole.
func forwardedMessage(message *domain.StreamMessage) ([]byte, bool, error) {
	if !messageForwarded(message) {
		return nil, false, nil
	}
	payload := message.Payload
	if encoding, _ := message.Metadata["encoding"].(string); message.Compressed && stream.Decodable(encoding) {
		compressed, err := stream.Compress(encoding, payload)
		if err != nil {
			return nil, false, fmt.Errorf("compressing stream message %s with %q: %w", message.ID, encoding, err)
		}
		payload = compressed
	}
	if !stream.Framing(message.Framing).Enveloped() {
		return payload, true, nil
	}
	var flags byte
	if message.Flags != nil {
		flags = *message.Flags
	}
	return stream.Envelope(flags, payload), true, nil
}

// withBody is the head of raw followed by body. An HTTP/1.1 head loses its
// Transfer-Encoding fields and has its Content-Length set to the body's
// length, added when it has none; an HTTP/2 head has a content-length field
// it has set, and none added.
func withBody(raw, body []byte) []byte {
	head := raw
	if end := bytes.Index(raw, []byte("\r\n\r\n")); end >= 0 {
		head = raw[:end]
	}
	lines := bytes.Split(head, []byte("\r\n"))
	h2 := rawhttp.IsHTTP2Text(string(lines[0]))
	length := []byte(strconv.Itoa(len(body)))
	out := [][]byte{lines[0]}
	found := false
	for _, line := range lines[1:] {
		name, _, _ := bytes.Cut(line, []byte(":"))
		switch {
		case h2 && len(name) == 0:
			// A pseudo-header field line, ":authority: …".
		case !h2 && bytes.EqualFold(bytes.TrimSpace(name), []byte("Transfer-Encoding")):
			continue
		case bytes.EqualFold(bytes.TrimSpace(name), []byte("Content-Length")):
			found = true
			line = append(append(append([]byte{}, name...), ": "...), length...)
		}
		out = append(out, line)
	}
	if !found && !h2 {
		out = append(out, append([]byte("Content-Length: "), length...))
	}
	joined := bytes.Join(out, []byte("\r\n"))
	joined = append(joined, "\r\n\r\n"...)
	return append(joined, body...)
}
