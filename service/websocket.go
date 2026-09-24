package service

import (
	"net/http"
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

func addWebSocketRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
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
