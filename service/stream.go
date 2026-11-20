package service

import (
	"bufio"
	"bytes"
	"errors"
	"net/http"
	"net/textproto"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// Stream messages of a streamed pair (GLOSSARY: Streamed pair, Stream
// message): read a pair's messages a page at a time, in the order they
// arrived, or one by its ID. A message's payload is its decompressed
// payload without its envelope. One message read by its ID also carries
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

func addStreamRoutes(mux routeMux, proxy *marasi.Proxy) {
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
		if proxy.TrafficRepo != nil {
			if row, err := proxy.TrafficRepo.GetRequestResponseRow(pairID); err == nil {
				if text, ok := stream.TextForm(streamContentType(row, message.Direction), message.Payload); ok {
					item.Text = &text
				}
			}
		}
		writeJSON(w, r, http.StatusOK, item)
	})
}

// streamContentType is the content type of the pair's head in direction.
// A response message is read by the request's content type while the
// streamed response is not yet stored, which happens when it ends.
func streamContentType(row *domain.RequestResponseRow, direction string) string {
	if direction == domain.StreamDirectionResponse && row.Response.ContentType != "" {
		return row.Response.ContentType
	}
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(row.Request.Raw)))
	if _, err := reader.ReadLine(); err != nil {
		return ""
	}
	header, _ := reader.ReadMIMEHeader()
	return header.Get("Content-Type")
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
