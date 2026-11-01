package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
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
	// Deflate is null when the connection does not use permessage-deflate.
	Deflate           *webSocketDeflateResponse `json:"deflate"`
	RemovedExtensions []string                  `json:"removed_extensions"`
}

// webSocketDeflateResponse is the negotiated permessage-deflate configuration (RFC 7692).
type webSocketDeflateResponse struct {
	ServerNoContextTakeover bool `json:"server_no_context_takeover"`
	ClientNoContextTakeover bool `json:"client_no_context_takeover"`
	ServerMaxWindowBits     int  `json:"server_max_window_bits"`
	ClientMaxWindowBits     int  `json:"client_max_window_bits"`
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
	FrameCount   int            `json:"frame_count"`
	Compressed   bool           `json:"compressed"`
	Payload      []byte         `json:"payload"`
	IsBinary     bool           `json:"is_binary"`
	CreatedAt    time.Time      `json:"created_at"`
	Metadata     map[string]any `json:"metadata"`
}

// webSocketProgressResponse announces a message whose frames are still
// arriving. message_id is the ID the message has once complete.
type webSocketProgressResponse struct {
	MessageID    uuid.UUID `json:"message_id"`
	ConnectionID uuid.UUID `json:"connection_id"`
	RequestID    uuid.UUID `json:"request_id"`
	Direction    string    `json:"direction"`
	Opcode       int       `json:"opcode"`
	Bytes        int64     `json:"bytes"`
	Frames       int       `json:"frames"`
}

type webSocketMessageList struct {
	Items      []webSocketMessageResponse `json:"items"`
	NextCursor *uuid.UUID                 `json:"next_cursor"`
}

func addWebSocketRoutes(mux routeMux, raw *http.ServeMux, projects *ProjectLifecycle, proxy *marasi.Proxy) {
	mux.HandleFunc("POST /websocket/{connection_id}/close", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("connection_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		code, reason, err := decodeWebSocketClose(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_websocket_request"})
			return
		}
		var live *marasiws.Connection
		var ok bool
		if proxy.WebSocketRegistry != nil {
			live, ok = proxy.WebSocketRegistry.Get(id)
		}
		if !ok {
			if proxy.WebSocketRepo == nil {
				writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
				return
			}
			if _, err := proxy.WebSocketRepo.GetConnection(id); err != nil {
				writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
				return
			}
			writeJSON(w, r, http.StatusConflict, map[string]string{"error": "websocket_not_open"})
			return
		}
		if err := live.StartClose(code, reason); errors.Is(err, marasiws.ErrConnectionClosed) {
			writeJSON(w, r, http.StatusConflict, map[string]string{"error": "websocket_not_open"})
			return
		} else if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		// Frame writes can fail even though the relay still produces a closed record.
		select {
		case record := <-live.ClosedRecord():
			writeJSON(w, r, http.StatusOK, webSocketConnectionFromDomain(record))
		case <-time.After(marasiws.CloseHandshakeTimeout + 100*time.Millisecond):
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
		}
	})

	// Inject can run extension callbacks that send through the proxy. Release first.
	raw.HandleFunc("POST /websocket/{connection_id}/inject", func(w http.ResponseWriter, r *http.Request) {
		release, admitted := admitProjectWork(projects, w, r)
		if !admitted {
			return
		}
		defer release()
		id, err := uuid.Parse(r.PathValue("connection_id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		inject, err := decodeWebSocketInject(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_websocket_request"})
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
		if proxy.WebSocketRegistry == nil {
			writeJSON(w, r, http.StatusConflict, map[string]string{"error": "websocket_not_open"})
			return
		}
		if _, live := proxy.WebSocketRegistry.Get(id); !live {
			writeJSON(w, r, http.StatusConflict, map[string]string{"error": "websocket_not_open"})
			return
		}
		release()
		message, err := proxy.InjectWebSocketMessage(connection.RequestID, inject.direction, inject.opcode, inject.payload, inject.options)
		if errors.Is(err, marasi.ErrWebSocketConnectionNotFound) || errors.Is(err, marasiws.ErrConnectionClosed) {
			writeJSON(w, r, http.StatusConflict, map[string]string{"error": "websocket_not_open"})
			return
		}
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		writeJSON(w, r, http.StatusOK, webSocketMessageFromDomain(message, connection.RequestID))
	})

	mux.HandleFunc("GET /websocket", func(w http.ResponseWriter, r *http.Request) {
		limit, cursor, err := parseNewestFirstPage(r)
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
		limit, cursor, err := parseNewestFirstPage(r)
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

func decodeWebSocketClose(r *http.Request) (int, string, error) {
	invalid := errors.New("invalid websocket request")
	code := marasiws.CloseNormalClosure
	var reason string
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		if errors.Is(err, io.EOF) {
			return code, reason, nil
		}
		return 0, "", invalid
	}
	if fields == nil {
		return 0, "", invalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return 0, "", invalid
	}
	for name, raw := range fields {
		if !utf8.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return 0, "", invalid
		}
		switch name {
		case "code":
			if err := json.Unmarshal(raw, &code); err != nil {
				return 0, "", invalid
			}
		case "reason":
			if err := json.Unmarshal(raw, &reason); err != nil {
				return 0, "", invalid
			}
		default:
			return 0, "", invalid
		}
	}
	if code == 0 {
		code = marasiws.CloseNormalClosure
	}
	if _, err := marasiws.EncodeClosePayload(code, reason); err != nil {
		return 0, "", invalid
	}
	return code, reason, nil
}

// injectRequest is a decoded inject request.
type injectRequest struct {
	direction string
	opcode    int
	payload   []byte
	options   marasiws.InjectOptions
}

// decodeWebSocketInject decodes an inject request: direction, opcode, an
// optional base64 payload, and an optional compress of "auto" (the default:
// compress by the leg's rules) or "never".
func decodeWebSocketInject(r *http.Request) (injectRequest, error) {
	invalid := errors.New("invalid websocket request")
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return injectRequest{}, invalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return injectRequest{}, invalid
	}
	var request injectRequest
	if len(fields) < 2 || len(fields) > 4 {
		return injectRequest{}, invalid
	}
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return injectRequest{}, invalid
		}
		switch name {
		case "direction":
			if err := json.Unmarshal(raw, &request.direction); err != nil || request.direction != "client" && request.direction != "server" {
				return injectRequest{}, invalid
			}
		case "opcode":
			if err := json.Unmarshal(raw, &request.opcode); err != nil || request.opcode < 0 || request.opcode > 15 {
				return injectRequest{}, invalid
			}
		case "payload":
			var encoded string
			if err := json.Unmarshal(raw, &encoded); err != nil {
				return injectRequest{}, invalid
			}
			var err error
			request.payload, err = base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return injectRequest{}, invalid
			}
		case "compress":
			var compress string
			if err := json.Unmarshal(raw, &compress); err != nil || compress != "auto" && compress != "never" {
				return injectRequest{}, invalid
			}
			request.options.NeverCompress = compress == "never"
		default:
			return injectRequest{}, invalid
		}
	}
	if _, ok := fields["direction"]; !ok {
		return injectRequest{}, invalid
	}
	if _, ok := fields["opcode"]; !ok {
		return injectRequest{}, invalid
	}
	return request, nil
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
		Direction: message.Direction, Opcode: message.Opcode, FrameCount: message.FrameCount,
		Compressed: message.Compressed,
		Payload:    payload, IsBinary: message.IsBinary, CreatedAt: message.CreatedAt, Metadata: metadata,
	}
}

func webSocketConnectionFromDomain(connection domain.WebSocketConnection) webSocketConnectionResponse {
	var deflate *webSocketDeflateResponse
	if connection.Deflate != nil {
		params := webSocketDeflateResponse(*connection.Deflate)
		deflate = &params
	}
	removed := connection.RemovedExtensions
	if removed == nil {
		removed = []string{}
	}
	return webSocketConnectionResponse{
		Deflate:           deflate,
		RemovedExtensions: removed,
		ID:                connection.ID,
		RequestID:         connection.RequestID,
		State:             connection.State,
		Transport:         connection.Transport,
		Host:              connection.Host,
		Path:              connection.Path,
		StartedAt:         connection.StartedAt,
		ClosedAt:          connection.ClosedAt,
		CloseCode:         connection.CloseCode,
		CloseReason:       connection.CloseReason,
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

// webSocketHandshakeEndedEvent announces an upgrade that will not open.
type webSocketHandshakeEndedEvent struct {
	RequestID uuid.UUID `json:"request_id"`
	State     string    `json:"state"` // failed or dropped
}

// HandleWebSocketHandshakeEnded publishes websocket.handshake_ended after a
// failed or dropped upgrade's websocket.state is stored.
func (s *Server) HandleWebSocketHandshakeEnded(requestID uuid.UUID, state string) {
	s.events.publish("websocket.handshake_ended", webSocketHandshakeEndedEvent{RequestID: requestID, State: state})
}

// HandleWebSocketProgress publishes a message whose frames are still arriving.
func (s *Server) HandleWebSocketProgress(progress domain.WebSocketMessageProgress) error {
	s.events.publish("websocket.progress", webSocketProgressResponse{
		MessageID:    progress.MessageID,
		ConnectionID: progress.ConnectionID,
		RequestID:    progress.RequestID,
		Direction:    progress.Direction,
		Opcode:       progress.Opcode,
		Bytes:        progress.Bytes,
		Frames:       progress.Frames,
	})
	return nil
}

// HandleWebSocketMessage publishes a whole message after it has been resolved for storage.
func (s *Server) HandleWebSocketMessage(message domain.WebSocketMessage) error {
	s.events.publish("websocket.message", webSocketMessageFromDomain(message, message.RequestID))
	return nil
}
