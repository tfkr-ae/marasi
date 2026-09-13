package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

const eventHeartbeatInterval = 15 * time.Second

// Server serves a service instance's control API.
type Server struct {
	mux               *http.ServeMux    // control API routes
	events            *eventBroadcaster // live traffic event fan-out
	heartbeatInterval time.Duration     // idle SSE comment interval
	proxy             *marasi.Proxy
	listener          ListenerLifecycle
	version           string
	instance          string
	project           string
}

// NewServer creates a control API server for proxy.
// stop runs after a POST /service/stop, once event streams have been closed.
func NewServer(proxy *marasi.Proxy, listener ListenerLifecycle, stop func(), version, instance, project string) *Server {
	mux := http.NewServeMux()
	events := newEventBroadcaster()
	if lifecycle, ok := listener.(*listenerLifecycle); ok {
		events = lifecycle.events
	}
	server := &Server{
		mux:               mux,
		events:            events,
		heartbeatInterval: eventHeartbeatInterval,
		proxy:             proxy,
		listener:          listener,
		version:           version,
		instance:          instance,
		project:           project,
	}
	addRoutes(mux, proxy, server.serveStatus, func() {
		server.Close()
		stop()
	})
	listenerMux := http.NewServeMux()
	listenerMux.HandleFunc("/status", server.serveListenerStatus)
	listenerMux.HandleFunc("/start", server.serveListenerStart)
	listenerMux.HandleFunc("/stop", server.serveListenerStop)
	listenerMux.HandleFunc("/update", server.serveListenerUpdate)
	mux.Handle("/listener/", http.StripPrefix("/listener", listenerMux))
	mux.HandleFunc("/events", server.serveEvents)
	return server
}

func (s *Server) serveListenerStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, r, http.StatusOK, s.listener.Status())
}

func (s *Server) serveListenerStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	settings, err := decodeListenerSettings(r, false)
	if err != nil {
		writeListenerError(w, r, err)
		return
	}
	status, err := s.listener.Start(r.Context(), settings)
	if err != nil {
		writeListenerError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, status)
}

func (s *Server) serveListenerStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			writeListenerError(w, r, errInvalidListenerRequest)
			return
		}
	}
	status, err := s.listener.Stop(r.Context())
	if err != nil {
		writeListenerError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, status)
}

func (s *Server) serveListenerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	settings, err := decodeListenerSettings(r, true)
	if err != nil {
		writeListenerError(w, r, err)
		return
	}
	status, err := s.listener.Update(r.Context(), settings)
	if err != nil {
		writeListenerError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, status)
}

var errInvalidListenerRequest = errors.New("invalid listener request")

func decodeListenerSettings(r *http.Request, requireField bool) (ListenerSettings, error) {
	if r.Body == nil {
		if requireField {
			return ListenerSettings{}, errInvalidListenerRequest
		}
		return ListenerSettings{}, nil
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		if errors.Is(err, io.EOF) && !requireField {
			return ListenerSettings{}, nil
		}
		return ListenerSettings{}, errInvalidListenerRequest
	}
	if fields == nil {
		return ListenerSettings{}, errInvalidListenerRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ListenerSettings{}, errInvalidListenerRequest
	}
	if requireField && len(fields) == 0 {
		return ListenerSettings{}, errInvalidListenerRequest
	}
	var settings ListenerSettings
	for name, raw := range fields {
		switch name {
		case "address":
			var address string
			if err := json.Unmarshal(raw, &address); err != nil || address == "" {
				return ListenerSettings{}, errInvalidListenerRequest
			}
			settings.Address = &address
		case "port":
			var port int
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return ListenerSettings{}, errInvalidListenerRequest
			}
			if err := json.Unmarshal(raw, &port); err != nil || port < 0 || port > 65535 {
				return ListenerSettings{}, errInvalidListenerRequest
			}
			value := uint16(port)
			settings.Port = &value
		default:
			return ListenerSettings{}, errInvalidListenerRequest
		}
	}
	return settings, nil
}

func writeListenerError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_server_error"
	switch {
	case errors.Is(err, errInvalidListenerRequest):
		status, code = http.StatusBadRequest, "invalid_listener_request"
	case errors.Is(err, ErrListenerAlreadyActive):
		status, code = http.StatusConflict, "listener_already_active"
	case errors.Is(err, ErrListenerInactive):
		status, code = http.StatusConflict, "listener_inactive"
	case errors.Is(err, ErrListenerUnavailable):
		status, code = http.StatusConflict, "listener_unavailable"
	case errors.Is(err, ErrListenerCleanup):
		status, code = http.StatusInternalServerError, "listener_cleanup_failed"
	}
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.project == "" {
		writeJSON(w, r, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{Error: "internal_server_error"})
		return
	}

	proxyListener := s.listener.Status().ProxyListener
	writeJSON(w, r, http.StatusOK, struct {
		Status        string  `json:"status"`
		Version       string  `json:"version"`
		Instance      string  `json:"instance"`
		Project       string  `json:"project"`
		ProxyListener *string `json:"proxy_listener"`
	}{
		Status:        "running",
		Version:       s.version,
		Instance:      s.instance,
		Project:       s.project,
		ProxyListener: proxyListener,
	})
}

// ServeHTTP serves the instance control API.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// HandleRequest publishes a traffic.request event. It always returns nil so
// streaming failures cannot fail proxied traffic.
func (s *Server) HandleRequest(request domain.ProxyRequest) error {
	s.events.publishRequest(request)
	return nil
}

// HandleResponse publishes a traffic.response event. It always returns nil so
// streaming failures cannot fail proxied traffic.
func (s *Server) HandleResponse(response domain.ProxyResponse) error {
	s.events.publishResponse(response)
	return nil
}

// Close closes active event streams.
func (s *Server) Close() {
	s.events.close()
}

// serveEvents handles GET /events as a Server-Sent Events stream of traffic
// notifications. It writes a connected comment, then request and response
// events, with heartbeat comments while idle.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	subscriber := s.events.subscribe()
	defer s.events.unsubscribe(subscriber)

	w.Header().Set("Content-Type", "text/event-stream")
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTimer(s.heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-subscriber.events:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
				return
			}
			flusher.Flush()
			if !heartbeat.Stop() {
				select {
				case <-heartbeat.C:
				default:
				}
			}
			heartbeat.Reset(s.heartbeatInterval)
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
			heartbeat.Reset(s.heartbeatInterval)
		}
	}
}
