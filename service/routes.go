package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
)

// routeMux registers control handlers. Project-bound registrars receive an
// admitting mux so each of those routes takes the project gate.
type routeMux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

type admittingMux struct {
	*http.ServeMux
	projects *ProjectLifecycle
}

func admitProjectWork(projects *ProjectLifecycle, w http.ResponseWriter, r *http.Request) (func(), bool) {
	if projects == nil {
		return func() {}, true
	}
	release, err := projects.Admit(r.Context())
	if err != nil {
		writeJSON(w, r, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{Error: "internal_server_error"})
		return nil, false
	}
	return release, true
}

func (m admittingMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	if m.projects == nil {
		m.ServeMux.HandleFunc(pattern, handler)
		return
	}
	m.ServeMux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		release, err := m.projects.Admit(r.Context())
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, struct {
				Error string `json:"error"`
			}{Error: "internal_server_error"})
			return
		}
		defer release()
		handler(w, r)
	})
}

// addRoutes registers control and traffic routes on mux.
func addRoutes(mux *http.ServeMux, projects *ProjectLifecycle, proxy *marasi.Proxy, chrome *Chrome, events *eventBroadcaster, status http.HandlerFunc, stop func()) {
	serviceMux := http.NewServeMux()
	addServiceRoutes(serviceMux, status, stop)
	mux.Handle("/service/", http.StripPrefix("/service", serviceMux))
	projectRoutes := admittingMux{ServeMux: mux, projects: projects}
	addScopeRoutes(projectRoutes, proxy)
	addCertificateRoutes(mux, proxy)
	addTrafficRoutes(projectRoutes, proxy, events)
	addLogRoutes(projectRoutes, proxy)
	addWebSocketRoutes(projectRoutes, mux, projects, proxy)
	addNoteRoutes(projectRoutes, proxy, events)
	addCheckpointRoutes(projectRoutes, proxy, events)
	addLaunchpadRoutes(projectRoutes, mux, proxy, events)
	addWaypointRoutes(projectRoutes, proxy, events)
	addTestCaseRoutes(projectRoutes, proxy, events)
	addFindingRoutes(projectRoutes, proxy, events)
	addArtifactRoutes(projectRoutes, proxy, events)
	addArmoryRoutes(projectRoutes, proxy, events)
	addWordlistRoutes(mux, proxy, events)
	addReportRoutes(projectRoutes, proxy, events)
	addChromeRoutes(mux, chrome)
	addExtensionRoutes(projectRoutes, mux, projects, proxy, events)
}

// addServiceRoutes registers the service status and stop routes.
func addServiceRoutes(mux *http.ServeMux, status http.HandlerFunc, stop func()) {
	var stopOnce sync.Once
	mux.HandleFunc("/status", status)
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		stopOnce.Do(stop)
		if err := encode(w, r, http.StatusAccepted, map[string]string{"status": "shutdown_in_progress"}); err != nil {
			fmt.Fprintf(os.Stderr, "encoding response: %v\n", err)
		}
	})
}

// encode writes value as JSON with the given status.
func encode[T any](w http.ResponseWriter, r *http.Request, status int, value T) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(value)
}

// writeJSON writes value as JSON and logs encoding failures to stderr.
func writeJSON[T any](w http.ResponseWriter, r *http.Request, status int, value T) {
	if err := encode(w, r, status, value); err != nil {
		fmt.Fprintf(os.Stderr, "encoding response: %v\n", err)
	}
}

func parseNewestFirstPage(r *http.Request) (int, *uuid.UUID, error) {
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
