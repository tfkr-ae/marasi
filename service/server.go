package service

import (
	"fmt"
	"net/http"
	"time"

	"github.com/tfkr-ae/marasi"
)

const eventHeartbeatInterval = 15 * time.Second

// Server serves a service instance's control API.
type Server struct {
	mux               *http.ServeMux
	events            *eventBroadcaster
	heartbeatInterval time.Duration
}

// NewServer creates a control API server for proxy.
func NewServer(proxy *marasi.Proxy, stop func()) *Server {
	mux := http.NewServeMux()
	server := &Server{
		mux:               mux,
		events:            newEventBroadcaster(),
		heartbeatInterval: eventHeartbeatInterval,
	}
	addRoutes(mux, proxy, func() {
		server.Close()
		stop()
	})
	mux.HandleFunc("/events", server.serveEvents)
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Close closes active event streams.
func (s *Server) Close() {
	s.events.close()
}

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
