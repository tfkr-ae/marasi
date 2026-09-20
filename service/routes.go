package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/tfkr-ae/marasi"
)

// addRoutes registers control and traffic routes on mux.
func addRoutes(mux *http.ServeMux, proxy *marasi.Proxy, chrome *Chrome, events *eventBroadcaster, status http.HandlerFunc, stop func()) {
	serviceMux := http.NewServeMux()
	addServiceRoutes(serviceMux, status, stop)
	mux.Handle("/service/", http.StripPrefix("/service", serviceMux))
	addTrafficRoutes(mux, proxy)
	addNoteRoutes(mux, proxy, events)
	addCheckpointRoutes(mux, proxy, events)
	addLaunchpadRoutes(mux, proxy, events)
	addWaypointRoutes(mux, proxy, events)
	addTestCaseRoutes(mux, proxy, events)
	addFindingRoutes(mux, proxy, events)
	addArtifactRoutes(mux, proxy, events)
	addArmoryRoutes(mux, proxy, events)
	addWordlistRoutes(mux, proxy, events)
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
