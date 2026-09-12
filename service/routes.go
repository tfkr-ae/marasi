package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
)

func addRoutes(mux *http.ServeMux, stop func()) {
	serviceMux := http.NewServeMux()
	addServiceRoutes(serviceMux, stop)
	mux.Handle("/service/", http.StripPrefix("/service", serviceMux))
}

func addServiceRoutes(mux *http.ServeMux, stop func()) {
	var stopOnce sync.Once
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		stopOnce.Do(stop)
		if err := encode(w, r, http.StatusAccepted, map[string]string{"status": "shutdown_in_progress"}); err != nil {
			fmt.Fprintf(os.Stderr, "encoding response: %v\n", err)
		}
	})
}

func encode[T any](w http.ResponseWriter, r *http.Request, status int, value T) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(value)
}
