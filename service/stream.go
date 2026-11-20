package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// Stream messages of a streamed pair (GLOSSARY: Streamed pair, Stream
// message): read a pair's messages a page at a time, in the order they
// arrived, or one by its ID. A message's payload is its decompressed
// payload without its envelope, or the compressed bytes when Marasi does
// not decode its encoding (metadata.encoding) or could not
// (metadata.inflate_error). One message read by its ID also carries
// its text form, when it has one (stream.TextForm).

// streamMessageResponse is one stream message. flags is null for a framing
// without a flags byte.
type streamMessageResponse struct {
	ID         uuid.UUID      `json:"id"`
	PairID     uuid.UUID      `json:"pair_id"`
	Direction  string         `json:"direction"`
	Framing    string         `json:"framing"`
	Flags      *uint8         `json:"flags"`
	Payload    []byte         `json:"payload"`
	Compressed bool           `json:"compressed"`
	CreatedAt  time.Time      `json:"created_at"`
	Metadata   map[string]any `json:"metadata"`
}

// streamMessageWithText is one stream message with its text form, null
// when it has none.
type streamMessageWithText struct {
	streamMessageResponse
	Text *string `json:"text"`
}

type streamMessageList struct {
	Items      []streamMessageResponse `json:"items"`
	NextCursor *uuid.UUID              `json:"next_cursor"`
}

func streamMessageFromDomain(message domain.StreamMessage) streamMessageResponse {
	payload := message.Payload
	if payload == nil {
		payload = []byte{}
	}
	metadata := message.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return streamMessageResponse{
		ID: message.ID, PairID: message.PairID, Direction: message.Direction, Framing: message.Framing,
		Flags: message.Flags, Payload: payload, Compressed: message.Compressed, CreatedAt: message.CreatedAt, Metadata: metadata,
	}
}

func addStreamRoutes(mux routeMux, raw *http.ServeMux, projects *ProjectLifecycle, proxy *marasi.Proxy) {
	// Inject runs the hooks and may be held in Checkpoint, which a project
	// switch waits for, so the project's admission is released first, as
	// for a WebSocket injection.
	raw.HandleFunc("POST /stream/{pair_id}/inject", func(w http.ResponseWriter, r *http.Request) {
		release, admitted := admitProjectWork(projects, w, r)
		if !admitted {
			return
		}
		defer release()
		pairID, err := uuid.Parse(r.PathValue("pair_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		inject, err := decodeStreamInject(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_stream_request"})
			return
		}
		release()
		message, err := proxy.InjectStreamMessage(r.Context(), pairID, inject.direction, inject.payload, inject.options)
		if err != nil {
			writeStreamControlError(w, r, proxy, pairID, err)
			return
		}
		writeJSON(w, r, http.StatusOK, streamMessageFromDomain(message))
	})

	mux.HandleFunc("POST /stream/{pair_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		pairID, err := uuid.Parse(r.PathValue("pair_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if err := proxy.CancelStreamedPair(pairID); err != nil {
			writeStreamControlError(w, r, proxy, pairID, err)
			return
		}
		writeJSON(w, r, http.StatusOK, streamCancelResponse{PairID: pairID, Cancelled: true})
	})

	// The pair's messages, oldest first: cursor is the last ID of the
	// page before.
	mux.HandleFunc("GET /stream/{pair_id}/message", func(w http.ResponseWriter, r *http.Request) {
		pairID, err := uuid.Parse(r.PathValue("pair_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		limit, cursor, err := parseNewestFirstPage(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if proxy.StreamRepo == nil || proxy.TrafficRepo == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if _, err := proxy.TrafficRepo.GetMetadata(pairID); err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		messages, next, err := proxy.StreamRepo.ListStreamMessages(pairID, cursor, limit)
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		items := make([]streamMessageResponse, len(messages))
		for i, message := range messages {
			items[i] = streamMessageFromDomain(*message)
		}
		writeJSON(w, r, http.StatusOK, streamMessageList{Items: items, NextCursor: next})
	})

	mux.HandleFunc("GET /stream/{pair_id}/message/{id}", func(w http.ResponseWriter, r *http.Request) {
		pairID, err := uuid.Parse(r.PathValue("pair_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if proxy.StreamRepo == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		message, err := proxy.StreamRepo.GetStreamMessage(id)
		switch {
		case errors.Is(err, domain.ErrStreamMessageNotFound), err == nil && message.PairID != pairID:
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		item := streamMessageWithText{streamMessageResponse: streamMessageFromDomain(*message)}
		if proxy.TrafficRepo != nil && payloadDecoded(message) {
			if row, err := proxy.TrafficRepo.GetRequestResponseRow(pairID); err == nil {
				if text, ok := stream.TextForm(streamContentType(row, message.Direction), message.Payload); ok {
					item.Text = &text
				}
			}
		}
		writeJSON(w, r, http.StatusOK, item)
	})
}

// streamCancelResponse answers POST /stream/{pair_id}/cancel.
type streamCancelResponse struct {
	PairID    uuid.UUID `json:"pair_id"`
	Cancelled bool      `json:"cancelled"`
}

// streamInjectRequest is a decoded inject request.
type streamInjectRequest struct {
	direction string
	payload   []byte
	options   marasi.StreamInjectOptions
}

// decodeStreamInject decodes an inject request: direction (request or
// response), a base64 payload, and an optional compress of "auto" (the
// default: compress as the direction does) or "never".
func decodeStreamInject(r *http.Request) (streamInjectRequest, error) {
	invalid := errors.New("invalid stream request")
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		Direction *string `json:"direction"`
		Payload   *[]byte `json:"payload"`
		Compress  *string `json:"compress"`
	}
	if err := decoder.Decode(&body); err != nil {
		return streamInjectRequest{}, invalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return streamInjectRequest{}, invalid
	}
	if body.Direction == nil || *body.Direction != domain.StreamDirectionRequest && *body.Direction != domain.StreamDirectionResponse || body.Payload == nil {
		return streamInjectRequest{}, invalid
	}
	request := streamInjectRequest{direction: *body.Direction, payload: *body.Payload}
	if body.Compress != nil {
		switch *body.Compress {
		case "auto":
		case "never":
			request.options.NeverCompress = true
		default:
			return streamInjectRequest{}, invalid
		}
	}
	return request, nil
}

// writeStreamControlError answers an inject or cancel that failed: not
// found for a pair that was never stored, conflict with the reason for one
// that is not being carried, or a direction that cannot take an injection.
func writeStreamControlError(w http.ResponseWriter, r *http.Request, proxy *marasi.Proxy, pairID uuid.UUID, err error) {
	switch {
	case errors.Is(err, marasi.ErrStreamedPairNotFound):
		if proxy.TrafficRepo != nil {
			if _, stored := proxy.TrafficRepo.GetMetadata(pairID); stored == nil {
				writeRefusal(w, r, http.StatusConflict, "stream_not_open", "the pair is not a streamed pair being carried")
				return
			}
		}
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, marasi.ErrStreamDirectionNotStreamed),
		errors.Is(err, marasi.ErrStreamDirectionEnded):
		writeRefusal(w, r, http.StatusConflict, "stream_not_open", err.Error())
	case errors.Is(err, marasi.ErrStreamLengthDeclared):
		writeRefusal(w, r, http.StatusConflict, "stream_inject_refused", err.Error())
	case errors.Is(err, r.Context().Err()):
		// The caller left while the injection waited for its turn.
	default:
		writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
	}
}

// payloadDecoded reports whether message's stored payload is decoded: it
// was not compressed, or was compressed with an encoding Marasi decodes and
// decoded without error. A payload stored as the compressed bytes has no
// text form.
func payloadDecoded(message *domain.StreamMessage) bool {
	if !message.Compressed {
		return true
	}
	encoding, _ := message.Metadata["encoding"].(string)
	_, failed := message.Metadata["inflate_error"]
	return stream.Decodable(encoding) && !failed
}

// streamContentType is the content type of the pair's head in direction.
// A response message is read by the request's content type while the
// streamed response is not yet stored, which happens when it ends.
func streamContentType(row *domain.RequestResponseRow, direction string) string {
	if direction == domain.StreamDirectionResponse && row.Response.ContentType != "" {
		return row.Response.ContentType
	}
	// A stored HTTP/2 head has its pseudo-header fields as lines, which a
	// MIME header reader refuses, so the lines are read one by one.
	head, _, _ := bytes.Cut(row.Request.Raw, []byte("\r\n\r\n"))
	lines := bytes.Split(head, []byte("\r\n"))
	for _, line := range lines[1:] {
		name, value, ok := bytes.Cut(line, []byte(":"))
		if ok && len(name) > 0 && strings.EqualFold(string(name), "content-type") {
			return strings.TrimSpace(string(value))
		}
	}
	return ""
}

// Streamed pair events (spec: Control API, CLI and events). The stream.
// prefix names the event stream itself, so these use streamed_pair. and
// stream_message.

// streamMessageEventPayloadLimit is the largest payload a stream_message
// event carries. A larger one is left out, with payload_truncated, and is
// read from GET /stream/{pair_id}/message/{id}.
const streamMessageEventPayloadLimit = 64 << 10

type streamedPairStartedEvent struct {
	PairID   uuid.UUID         `json:"pair_id"`
	Framings map[string]string `json:"framings"`
}

type streamMessageEvent struct {
	streamMessageResponse
	PayloadTruncated bool `json:"payload_truncated"`
}

type streamMessageProgressEvent struct {
	PairID    uuid.UUID `json:"pair_id"`
	Direction string    `json:"direction"`
	Framing   string    `json:"framing"`
	Bytes     int64     `json:"bytes"`
}

type streamTrailerResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// streamedDirectionResponse has the shape of a direction in the response's
// metadata.streamed.
type streamedDirectionResponse struct {
	Framing   string                  `json:"framing"`
	Messages  int                     `json:"messages"`
	Ending    string                  `json:"ending"`
	ResetCode string                  `json:"reset_code,omitempty"`
	Trailers  []streamTrailerResponse `json:"trailers,omitempty"`
}

type streamedPairEndedEvent struct {
	PairID     uuid.UUID                            `json:"pair_id"`
	Directions map[string]streamedDirectionResponse `json:"directions"`
}

// HandleStreamedPairStarted publishes streamed_pair.started.
func (s *Server) HandleStreamedPairStarted(started domain.StreamedPairStarted) {
	framings := started.Framings
	if framings == nil {
		framings = map[string]string{}
	}
	s.events.publish("streamed_pair.started", streamedPairStartedEvent{PairID: started.PairID, Framings: framings})
}

// HandleStreamMessage publishes stream_message, without a payload over
// streamMessageEventPayloadLimit.
func (s *Server) HandleStreamMessage(message domain.StreamMessage) {
	event := streamMessageEvent{streamMessageResponse: streamMessageFromDomain(message)}
	if len(message.Payload) > streamMessageEventPayloadLimit {
		event.Payload = nil
		event.PayloadTruncated = true
	}
	s.events.publish("stream_message", event)
}

// HandleStreamMessageProgress publishes stream_message.progress.
func (s *Server) HandleStreamMessageProgress(progress domain.StreamMessageProgress) {
	s.events.publish("stream_message.progress", streamMessageProgressEvent{
		PairID: progress.PairID, Direction: progress.Direction, Framing: progress.Framing, Bytes: progress.Bytes,
	})
}

// HandleStreamedPairEnded publishes streamed_pair.ended.
func (s *Server) HandleStreamedPairEnded(ended domain.StreamedPairEnded) {
	directions := make(map[string]streamedDirectionResponse, len(ended.Directions))
	for name, d := range ended.Directions {
		direction := streamedDirectionResponse{Framing: d.Framing, Messages: d.Messages, Ending: d.Ending, ResetCode: d.ResetCode}
		for _, trailer := range d.Trailers {
			direction.Trailers = append(direction.Trailers, streamTrailerResponse(trailer))
		}
		directions[name] = direction
	}
	s.events.publish("streamed_pair.ended", streamedPairEndedEvent{PairID: ended.PairID, Directions: directions})
}
