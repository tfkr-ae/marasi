package service

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type webSocketConnectionResponse struct {
	ID          uuid.UUID  `json:"id"`
	RequestID   uuid.UUID  `json:"request_id"`
	State       string     `json:"state"`
	Transport   string     `json:"transport"`
	Host        string     `json:"host"`
	Path        string     `json:"path"`
	StartedAt   time.Time  `json:"started_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	CloseCode   int        `json:"close_code"`
	CloseReason string     `json:"close_reason"`
}

type webSocketConnectionList struct {
	Items      []webSocketConnectionResponse `json:"items"`
	NextCursor *uuid.UUID                    `json:"next_cursor"`
}

type webSocketMessageResponse struct {
	ID           uuid.UUID      `json:"id"`
	ConnectionID uuid.UUID      `json:"connection_id"`
	RequestID    uuid.UUID      `json:"request_id"`
	Direction    string         `json:"direction"`
	Opcode       int            `json:"opcode"`
	Fin          bool           `json:"fin"`
	Payload      []byte         `json:"payload"`
	IsBinary     bool           `json:"is_binary"`
	CreatedAt    time.Time      `json:"created_at"`
	Metadata     map[string]any `json:"metadata"`
}

type webSocketMessageList struct {
	Items      []webSocketMessageResponse `json:"items"`
	NextCursor *uuid.UUID                 `json:"next_cursor"`
}

func addWebSocketRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /websocket", func(w http.ResponseWriter, r *http.Request) {
		limit, cursor, err := parseWebSocketPage(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if proxy.WebSocketRepo == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		connections, nextCursor, err := proxy.WebSocketRepo.ListConnections(cursor, limit)
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		items := make([]webSocketConnectionResponse, len(connections))
		for i, connection := range connections {
			items[i] = webSocketConnectionFromDomain(*connection)
		}
		writeJSON(w, r, http.StatusOK, webSocketConnectionList{Items: items, NextCursor: nextCursor})
	})

	mux.HandleFunc("GET /websocket/{connection_id}/message", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("connection_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		limit, cursor, err := parseWebSocketPage(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if proxy.WebSocketRepo == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		connection, err := proxy.WebSocketRepo.GetConnection(id)
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		messages, nextCursor, err := proxy.WebSocketRepo.ListMessages(id, cursor, limit)
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		items := make([]webSocketMessageResponse, len(messages))
		for i, message := range messages {
			items[i] = webSocketMessageFromDomain(*message, connection.RequestID)
		}
		writeJSON(w, r, http.StatusOK, webSocketMessageList{Items: items, NextCursor: nextCursor})
	})

	mux.HandleFunc("GET /websocket/{connection_id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("connection_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if proxy.WebSocketRepo == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		connection, err := proxy.WebSocketRepo.GetConnection(id)
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, r, http.StatusOK, webSocketConnectionFromDomain(*connection))
	})

	mux.HandleFunc("GET /traffic/{request_id}/websocket", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("request_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if proxy.WebSocketRepo == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		connection, err := proxy.WebSocketRepo.GetConnectionByRequestID(id)
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, r, http.StatusOK, webSocketConnectionFromDomain(*connection))
	})
}

func parseWebSocketPage(r *http.Request) (int, *uuid.UUID, error) {
	var pageFields []string
	for _, field := range strings.Split(r.URL.RawQuery, "&") {
		name, _, _ := strings.Cut(field, "=")
		key, err := url.QueryUnescape(name)
		if err == nil && (key == "limit" || key == "cursor") {
			pageFields = append(pageFields, field)
		}
	}
	query, err := url.ParseQuery(strings.Join(pageFields, "&"))
	if err != nil {
		return 0, nil, err
	}
	limit := 200
	if values, present := query["limit"]; present {
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > 500 {
			return 0, nil, errors.New("invalid limit")
		}
		limit = parsed
	}
	var cursor *uuid.UUID
	if values, present := query["cursor"]; present {
		parsed, err := uuid.Parse(values[0])
		if err != nil {
			return 0, nil, err
		}
		cursor = &parsed
	}
	return limit, cursor, nil
}

func webSocketMessageFromDomain(message domain.WebSocketMessage, requestID uuid.UUID) webSocketMessageResponse {
	payload := message.Payload
	if payload == nil {
		payload = []byte{}
	}
	metadata := message.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return webSocketMessageResponse{
		ID: message.ID, ConnectionID: message.ConnectionID, RequestID: requestID,
		Direction: message.Direction, Opcode: message.Opcode, Fin: message.Fin,
		Payload: payload, IsBinary: message.IsBinary, CreatedAt: message.CreatedAt, Metadata: metadata,
	}
}

func webSocketConnectionFromDomain(connection domain.WebSocketConnection) webSocketConnectionResponse {
	return webSocketConnectionResponse{
		ID:          connection.ID,
		RequestID:   connection.RequestID,
		State:       connection.State,
		Transport:   connection.Transport,
		Host:        connection.Host,
		Path:        connection.Path,
		StartedAt:   connection.StartedAt,
		ClosedAt:    connection.ClosedAt,
		CloseCode:   connection.CloseCode,
		CloseReason: connection.CloseReason,
	}
}

// HandleWebSocketOpen publishes a newly opened WebSocket connection.
func (s *Server) HandleWebSocketOpen(connection domain.WebSocketConnection) error {
	s.events.publish("websocket.opened", webSocketConnectionFromDomain(connection))
	return nil
}

// HandleWebSocketClose publishes a closed WebSocket connection.
func (s *Server) HandleWebSocketClose(connection domain.WebSocketConnection) error {
	s.events.publish("websocket.closed", webSocketConnectionFromDomain(connection))
	return nil
}

// HandleWebSocketMessage publishes a frame after it has been resolved for storage.
func (s *Server) HandleWebSocketMessage(message domain.WebSocketMessage) error {
	s.events.publish("websocket.message", webSocketMessageFromDomain(message, message.RequestID))
	return nil
}
