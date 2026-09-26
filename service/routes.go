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

// addRoutes registers control and traffic routes on mux.
func addRoutes(mux *http.ServeMux, proxy *marasi.Proxy, chrome *Chrome, events *eventBroadcaster, status http.HandlerFunc, stop func()) {
	serviceMux := http.NewServeMux()
	addServiceRoutes(serviceMux, status, stop)
	mux.Handle("/service/", http.StripPrefix("/service", serviceMux))
	addCertificateRoutes(mux, proxy)
	addTrafficRoutes(mux, proxy, events)
	addLogRoutes(mux, proxy)
	addWebSocketRoutes(mux, proxy)
	addNoteRoutes(mux, proxy, events)
	addCheckpointRoutes(mux, proxy, events)
	addLaunchpadRoutes(mux, proxy, events)
	addWaypointRoutes(mux, proxy, events)
	addTestCaseRoutes(mux, proxy, events)
	addFindingRoutes(mux, proxy, events)
	addArtifactRoutes(mux, proxy, events)
	addArmoryRoutes(mux, proxy, events)
	addWordlistRoutes(mux, proxy, events)
	addReportRoutes(mux, proxy, events)
	addChromeRoutes(mux, chrome)
	addExtensionRoutes(mux, proxy, events)
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
