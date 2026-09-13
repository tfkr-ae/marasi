package service

import (
	"fmt"
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
	version           string
	instance          string
	project           string
}

// NewServer creates a control API server for proxy.
// stop runs after a POST /service/stop, once event streams have been closed.
func NewServer(proxy *marasi.Proxy, stop func(), version, instance, project string) *Server {
	mux := http.NewServeMux()
	server := &Server{
		mux:               mux,
		events:            newEventBroadcaster(),
		heartbeatInterval: eventHeartbeatInterval,
		proxy:             proxy,
		version:           version,
		instance:          instance,
		project:           project,
	}
	addRoutes(mux, proxy, server.serveStatus, func() {
		server.Close()
		stop()
	})
	mux.HandleFunc("/events", server.serveEvents)
	return server
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

	var proxyListener *string
	if address, active := s.proxy.ActiveListenerAddress(); active {
		proxyListener = &address
	}
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
