package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type checkpointHTTPItem struct {
	ID   uuid.UUID `json:"id"`
	Type string    `json:"type"`
	Raw  []byte    `json:"raw"`
}

type checkpointWebSocketItem struct {
	ID           uuid.UUID `json:"id"`
	Type         string    `json:"type"`
	Payload      []byte    `json:"payload"`
	Opcode       int       `json:"opcode"`
	Direction    string    `json:"direction"`
	ConnectionID uuid.UUID `json:"connection_id"`
	RequestID    uuid.UUID `json:"request_id"`
}

type checkpointList struct {
	Items              []any `json:"items"`
	Intercept          bool  `json:"intercept"`
	WebsocketIntercept bool  `json:"websocket_intercept"`
}

type checkpointResolvedEvent struct {
	ID   uuid.UUID `json:"id"`
	Type string    `json:"type"`
}

type checkpointFlags struct {
	Intercept          bool `json:"intercept"`
	WebsocketIntercept bool `json:"websocket_intercept"`
}

var errInvalidCheckpointRequest = errors.New("invalid checkpoint request")

func addCheckpointRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /checkpoint", func(w http.ResponseWriter, r *http.Request) {
		kind, err := parseCheckpointKind(r)
		if err != nil {
			writeCheckpointError(w, r, http.StatusBadRequest, "invalid_checkpoint_request")
			return
		}
		writeJSON(w, r, http.StatusOK, getCheckpointList(proxy, kind))
	})

	mux.HandleFunc("GET /checkpoint/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseCheckpointID(w, r)
		if !ok {
			return
		}
		item, found := proxy.GetCheckpoint(id)
		if !found {
			writeCheckpointError(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, r, http.StatusOK, checkpointItemFromDomain(item))
	})

	mux.HandleFunc("POST /checkpoint/{id}/forward", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseCheckpointID(w, r)
		if !ok {
			return
		}
		item, found := proxy.GetCheckpoint(id)
		if !found {
			writeCheckpointError(w, r, http.StatusNotFound, "not_found")
			return
		}
		fwd, err := decodeCheckpointForward(r, item.Type)
		if err != nil {
			writeCheckpointError(w, r, http.StatusBadRequest, "invalid_checkpoint_request")
			return
		}
		if err := proxy.ForwardCheckpoint(id, fwd); err != nil {
			writeCheckpointForwardError(w, r, err)
			return
		}
		events.publish("checkpoint.forwarded", checkpointResolvedEvent{ID: id, Type: item.Type})
		writeJSON(w, r, http.StatusOK, getCheckpointList(proxy, ""))
	})

	mux.HandleFunc("POST /checkpoint/{id}/drop", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseCheckpointID(w, r)
		if !ok {
			return
		}
		item, found := proxy.GetCheckpoint(id)
		if !found {
			writeCheckpointError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := decodeCheckpointDrop(r); err != nil {
			writeCheckpointError(w, r, http.StatusBadRequest, "invalid_checkpoint_request")
			return
		}
		if err := proxy.DropCheckpoint(id); err != nil {
			if errors.Is(err, marasi.ErrCheckpointNotFound) {
				writeCheckpointError(w, r, http.StatusNotFound, "not_found")
				return
			}
			writeCheckpointError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		events.publish("checkpoint.dropped", checkpointResolvedEvent{ID: id, Type: item.Type})
		writeJSON(w, r, http.StatusOK, getCheckpointList(proxy, ""))
	})

	mux.HandleFunc("POST /checkpoint/intercept", func(w http.ResponseWriter, r *http.Request) {
		serveCheckpointFlag(w, r, proxy, events, "intercept", proxy.GetIntercept, proxy.SetIntercept)
	})

	mux.HandleFunc("POST /checkpoint/websocket-intercept", func(w http.ResponseWriter, r *http.Request) {
		serveCheckpointFlag(w, r, proxy, events, "websocket_intercept", proxy.GetWebSocketIntercept, proxy.SetWebSocketIntercept)
	})
}

func serveCheckpointFlag(w http.ResponseWriter, r *http.Request, proxy *marasi.Proxy, events *eventBroadcaster, field string, get func() bool, set func(bool)) {
	value, err := decodeCheckpointFlag(r, field)
	if err != nil {
		writeCheckpointError(w, r, http.StatusBadRequest, "invalid_checkpoint_request")
		return
	}
	changed := get() != value
	set(value)
	response := getCheckpointList(proxy, "")
	if changed {
		events.publish("checkpoint.updated", checkpointFlags{
			Intercept:          response.Intercept,
			WebsocketIntercept: response.WebsocketIntercept,
		})
	}
	writeJSON(w, r, http.StatusOK, response)
}

func parseCheckpointKind(r *http.Request) (string, error) {
	kind := r.URL.Query().Get("kind")
	switch kind {
	case "", "http", "websocket":
		return kind, nil
	default:
		return "", errInvalidCheckpointRequest
	}
}

func parseCheckpointID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeCheckpointError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func getCheckpointList(proxy *marasi.Proxy, kind string) checkpointList {
	items := make([]any, 0)
	for _, item := range proxy.CheckpointItems() {
		if kind == "http" && item.Type == domain.CheckpointTypeWebSocket {
			continue
		}
		if kind == "websocket" && item.Type != domain.CheckpointTypeWebSocket {
			continue
		}
		items = append(items, checkpointItemFromDomain(item))
	}
	return checkpointList{
		Items:              items,
		Intercept:          proxy.GetIntercept(),
		WebsocketIntercept: proxy.GetWebSocketIntercept(),
	}
}

func checkpointItemFromDomain(item domain.CheckpointItem) any {
	if item.Type == domain.CheckpointTypeWebSocket {
		return checkpointWebSocketItem{
			ID:           item.ID,
			Type:         item.Type,
			Payload:      item.Payload,
			Opcode:       item.Opcode,
			Direction:    item.Direction,
			ConnectionID: item.ConnectionID,
			RequestID:    item.RequestID,
		}
	}
	return checkpointHTTPItem{
		ID:   item.ID,
		Type: item.Type,
		Raw:  item.Raw,
	}
}

func checkpointItemFromWebSocket(message domain.WebSocketMessage) domain.CheckpointItem {
	return domain.CheckpointItem{
		ID:           message.ID,
		Type:         domain.CheckpointTypeWebSocket,
		Payload:      message.Payload,
		Opcode:       message.Opcode,
		Direction:    message.Direction,
		ConnectionID: message.ConnectionID,
		RequestID:    message.RequestID,
	}
}

func decodeCheckpointForward(r *http.Request, itemType string) (marasi.CheckpointForward, error) {
	if r.Body == nil {
		return marasi.CheckpointForward{}, errInvalidCheckpointRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return marasi.CheckpointForward{}, errInvalidCheckpointRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return marasi.CheckpointForward{}, errInvalidCheckpointRequest
	}
	var fwd marasi.CheckpointForward
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return marasi.CheckpointForward{}, errInvalidCheckpointRequest
		}
		switch itemType {
		case domain.CheckpointTypeRequest, domain.CheckpointTypeResponse:
			switch name {
			case "raw":
				value, err := decodeCheckpointBytes(raw)
				if err != nil {
					return marasi.CheckpointForward{}, err
				}
				fwd.Raw = &value
			case "intercept_response":
				if itemType != domain.CheckpointTypeRequest {
					return marasi.CheckpointForward{}, errInvalidCheckpointRequest
				}
				var value bool
				if err := json.Unmarshal(raw, &value); err != nil {
					return marasi.CheckpointForward{}, errInvalidCheckpointRequest
				}
				fwd.InterceptResponse = value
			default:
				return marasi.CheckpointForward{}, errInvalidCheckpointRequest
			}
		case domain.CheckpointTypeWebSocket:
			switch name {
			case "payload":
				value, err := decodeCheckpointBytes(raw)
				if err != nil {
					return marasi.CheckpointForward{}, err
				}
				fwd.Payload = &value
			case "opcode":
				var value int
				if err := json.Unmarshal(raw, &value); err != nil || value < 0 || value > 15 {
					return marasi.CheckpointForward{}, errInvalidCheckpointRequest
				}
				fwd.Opcode = &value
			default:
				return marasi.CheckpointForward{}, errInvalidCheckpointRequest
			}
		default:
			return marasi.CheckpointForward{}, errInvalidCheckpointRequest
		}
	}
	return fwd, nil
}

func decodeCheckpointBytes(raw json.RawMessage) ([]byte, error) {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, errInvalidCheckpointRequest
	}
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errInvalidCheckpointRequest
	}
	return value, nil
}

func decodeCheckpointDrop(r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	err := decoder.Decode(&fields)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil || fields == nil {
		return errInvalidCheckpointRequest
	}
	if len(fields) != 0 {
		return errInvalidCheckpointRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errInvalidCheckpointRequest
	}
	return nil
}

func decodeCheckpointFlag(r *http.Request, field string) (bool, error) {
	if r.Body == nil {
		return false, errInvalidCheckpointRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return false, errInvalidCheckpointRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false, errInvalidCheckpointRequest
	}
	if len(fields) != 1 {
		return false, errInvalidCheckpointRequest
	}
	raw, ok := fields[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errInvalidCheckpointRequest
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errInvalidCheckpointRequest
	}
	return value, nil
}

func writeCheckpointForwardError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, marasi.ErrCheckpointNotFound):
		writeCheckpointError(w, r, http.StatusNotFound, "not_found")
	case errors.Is(err, marasi.ErrInterceptResponseNotAllowed),
		errors.Is(err, marasi.ErrRebuildRequest),
		errors.Is(err, marasi.ErrRebuildResponse):
		writeCheckpointError(w, r, http.StatusBadRequest, "invalid_checkpoint_request")
	default:
		writeCheckpointError(w, r, http.StatusInternalServerError, "internal_server_error")
	}
}

func writeCheckpointError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
