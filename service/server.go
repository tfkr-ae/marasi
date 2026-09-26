package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	projects          *ProjectLifecycle
	chrome            *Chrome
	version           string
	instance          string
	project           string
}

// NewServer creates a control API server for proxy.
// stop runs after a POST /service/stop, once event streams have been closed.
func NewServer(proxy *marasi.Proxy, listener ListenerLifecycle, projects *ProjectLifecycle, stop func(), version, instance, project string) *Server {
	mux := http.NewServeMux()
	events := newEventBroadcaster()
	chromeLog := io.Writer(io.Discard)
	if lifecycle, ok := listener.(*listenerLifecycle); ok {
		events = lifecycle.events
		chromeLog = lifecycle.logWriter
	}
	chrome := NewChrome(proxy, listener, chromeLog)
	chrome.events = events
	server := &Server{
		mux:               mux,
		events:            events,
		heartbeatInterval: eventHeartbeatInterval,
		proxy:             proxy,
		listener:          listener,
		projects:          projects,
		chrome:            chrome,
		version:           version,
		instance:          instance,
		project:           project,
	}
	publishArmoryRunUpdated := func(run *domain.ArmoryRun) {
		events.publish("armory.run.updated", armoryRunFromDomain(run))
	}
	if projects != nil {
		projects.opened = func(path string) {
			events.publish("project.opened", struct {
				Project string `json:"project"`
			}{Project: path})
		}
		projects.logAdded = func(entry *domain.Log) {
			events.publish("log.added", proxyLogFromDomain(entry))
		}
		projects.armoryRunUpdated = publishArmoryRunUpdated
	}
	if proxy != nil {
		if armoryService, err := proxy.GetArmory(); err == nil {
			if manager, ok := armoryService.(interface{ SetRunUpdated(func(*domain.ArmoryRun)) }); ok {
				manager.SetRunUpdated(publishArmoryRunUpdated)
			}
		}
	}
	addRoutes(mux, proxy, chrome, events, server.serveStatus, func() {
		server.dropPendingCheckpoint()
		server.Close()
		stop()
	})
	listenerMux := http.NewServeMux()
	listenerMux.HandleFunc("/status", server.serveListenerStatus)
	listenerMux.HandleFunc("/start", server.serveListenerStart)
	listenerMux.HandleFunc("/stop", server.serveListenerStop)
	listenerMux.HandleFunc("/update", server.serveListenerUpdate)
	mux.Handle("/listener/", http.StripPrefix("/listener", listenerMux))
	projectMux := http.NewServeMux()
	projectMux.HandleFunc("/open", server.serveProjectOpen)
	mux.Handle("/project/", http.StripPrefix("/project", projectMux))
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
	settings, err := decodeListenerSettings(r)
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
	settings, err := decodeListenerSettings(r)
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

func decodeListenerSettings(r *http.Request) (ListenerSettings, error) {
	if r.Body == nil {
		return ListenerSettings{}, errInvalidListenerRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return ListenerSettings{}, errInvalidListenerRequest
	}
	if fields == nil {
		return ListenerSettings{}, errInvalidListenerRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ListenerSettings{}, errInvalidListenerRequest
	}
	if len(fields) != 2 {
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
	if settings.Address == nil || settings.Port == nil {
		return ListenerSettings{}, errInvalidListenerRequest
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
	}
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func (s *Server) serveProjectOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, err := decodeProjectOpen(r)
	if err != nil {
		writeProjectError(w, r, err)
		return
	}
	if s.projects == nil {
		writeProjectError(w, r, errors.New("project open not configured"))
		return
	}
	if err := s.projects.Open(r.Context(), path); err != nil {
		writeProjectError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, struct {
		Project string `json:"project"`
	}{Project: path})
}

var errInvalidProjectRequest = errors.New("invalid project request")

func decodeProjectOpen(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", errInvalidProjectRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return "", errInvalidProjectRequest
	}
	if fields == nil {
		return "", errInvalidProjectRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errInvalidProjectRequest
	}
	raw, ok := fields["path"]
	if !ok || len(fields) != 1 {
		return "", errInvalidProjectRequest
	}
	var path string
	if err := json.Unmarshal(raw, &path); err != nil || path == "" {
		return "", errInvalidProjectRequest
	}
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".marasi") {
		return "", errInvalidProjectRequest
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() {
		return "", errInvalidProjectRequest
	}
	canonical, err := ResolveProjectPath(path)
	if err != nil {
		return "", errInvalidProjectRequest
	}
	return canonical, nil
}

func writeProjectError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_server_error"
	switch {
	case errors.Is(err, errInvalidProjectRequest):
		status, code = http.StatusBadRequest, "invalid_project_request"
	case errors.Is(err, ErrProjectAlreadyOpen):
		status, code = http.StatusConflict, "project_already_open"
	case errors.Is(err, ErrProjectBusy):
		status, code = http.StatusConflict, "project_busy"
	case errors.Is(err, ErrProjectCleanup):
		status, code = http.StatusInternalServerError, "project_cleanup_failed"
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
	project := s.project
	if s.projects != nil {
		project = s.projects.Path()
	}
	if project == "" {
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
		Project:       project,
		ProxyListener: proxyListener,
	})
}

// ServeHTTP serves the instance control API.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if (r.Method == http.MethodGet || r.Method == http.MethodDelete) && invalidWordlistPath(r.URL.Path) {
		writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
		return
	}
	if r.Method == http.MethodDelete && invalidReportTemplatePath(r.URL.Path) {
		writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
		return
	}
	if s.projects != nil && persistsToOpenProject(r.Method, r.URL.Path) {
		release, err := s.projects.Admit(r.Context())
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, struct {
				Error string `json:"error"`
			}{Error: "internal_server_error"})
			return
		}
		defer release()
	}
	s.mux.ServeHTTP(w, r)
}

func persistsToOpenProject(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	switch {
	case strings.HasPrefix(path, "/launchpad"):
		return !strings.HasSuffix(path, "/launch")
	case strings.HasPrefix(path, "/waypoint"),
		strings.HasPrefix(path, "/test-case"),
		strings.HasPrefix(path, "/finding"),
		strings.HasPrefix(path, "/artifact"),
		strings.HasPrefix(path, "/extension"):
		return true
	case strings.HasPrefix(path, "/armory"):
		return !strings.HasSuffix(path, "/validate")
	default:
		return false
	}
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

// HandleIntercept publishes checkpoint.held after an item is pending. It always
// returns nil so notify cannot drop the hold or wait for subscribers.
func (s *Server) HandleIntercept(item domain.CheckpointItem) error {
	s.events.publish("checkpoint.held", checkpointItemFromDomain(item))
	return nil
}

// HandleWebSocketIntercept publishes checkpoint.held after a WebSocket item is
// pending. It always returns nil so notify cannot drop the hold or wait for subscribers.
func (s *Server) HandleWebSocketIntercept(message domain.WebSocketMessage) error {
	s.events.publish("checkpoint.held", checkpointItemFromDomain(checkpointItemFromWebSocket(message)))
	return nil
}

func (s *Server) dropPendingCheckpoint() {
	if s.proxy == nil {
		return
	}
	items := s.proxy.CheckpointItems()
	s.proxy.DropAllCheckpoint()
	for _, item := range items {
		s.events.publish("checkpoint.dropped", checkpointResolvedEvent{ID: item.ID, Type: item.Type})
	}
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
