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
