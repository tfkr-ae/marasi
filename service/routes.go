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
func addRoutes(mux *http.ServeMux, proxy *marasi.Proxy, stop func()) {
	serviceMux := http.NewServeMux()
	addServiceRoutes(serviceMux, stop)
	mux.Handle("/service/", http.StripPrefix("/service", serviceMux))
	addTrafficRoutes(mux, proxy)
}

// addServiceRoutes registers POST /stop, which runs stop once and returns 202.
func addServiceRoutes(mux *http.ServeMux, stop func()) {
	var stopOnce sync.Once
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
