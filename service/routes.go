package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// addRoutes registers control and traffic routes on mux.
func addRoutes(mux *http.ServeMux, proxy *marasi.Proxy, status http.HandlerFunc, stop func()) {
	serviceMux := http.NewServeMux()
	addServiceRoutes(serviceMux, status, stop)
	mux.Handle("/service/", http.StripPrefix("/service", serviceMux))
	addTrafficRoutes(mux, proxy)
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

type launchpadSummary struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
}

type launchpadList struct {
	Items []launchpadSummary `json:"items"`
}

type launchpadDetail struct {
	launchpadSummary
	Items []trafficSummary `json:"items"`
}

type launchpadMutation struct {
	Name        *string
	Description *string
}

var errInvalidLaunchpadRequest = errors.New("invalid launchpad request")

func addLaunchpadRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /launchpad", func(w http.ResponseWriter, r *http.Request) {
		repo, err := proxy.GetLaunchpadRepo()
		if err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		launchpads, err := repo.GetLaunchpads()
		if err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		items := make([]launchpadSummary, 0, len(launchpads))
		for _, launchpad := range launchpads {
			items = append(items, launchpadSummaryFromDomain(launchpad))
		}
		writeJSON(w, r, http.StatusOK, launchpadList{Items: items})
	})

	mux.HandleFunc("POST /launchpad", func(w http.ResponseWriter, r *http.Request) {
		mutation, err := decodeLaunchpadMutation(r, true)
		if err != nil {
			writeLaunchpadError(w, r, http.StatusBadRequest, "invalid_launchpad_request")
			return
		}
		repo, err := proxy.GetLaunchpadRepo()
		if err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		description := ""
		if mutation.Description != nil {
			description = *mutation.Description
		}
		id, err := repo.CreateLaunchpad(*mutation.Name, description)
		if err != nil {
			writeLaunchpadError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response := launchpadSummary{ID: id, Name: *mutation.Name, Description: description}
		events.publish("launchpad.created", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("GET /launchpad/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseLaunchpadID(w, r)
		if !ok {
			return
		}
		repo, err := proxy.GetLaunchpadRepo()
		if err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		launchpad, err := repo.GetLaunchpad(id)
		if err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, r, http.StatusOK, launchpadDetail{
			launchpadSummary: launchpadSummaryFromDomain(launchpad),
			Items:            []trafficSummary{},
		})
	})

	mux.HandleFunc("POST /launchpad/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseLaunchpadID(w, r)
		if !ok {
			return
		}
		mutation, err := decodeLaunchpadMutation(r, false)
		if err != nil {
			writeLaunchpadError(w, r, http.StatusBadRequest, "invalid_launchpad_request")
			return
		}
		repo, err := proxy.GetLaunchpadRepo()
		if err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := repo.UpdateLaunchpad(id, mutation.Name, mutation.Description); err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		launchpad, err := repo.GetLaunchpad(id)
		if err != nil {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		response := launchpadSummaryFromDomain(launchpad)
		events.publish("launchpad.updated", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}

func launchpadSummaryFromDomain(launchpad *domain.Launchpad) launchpadSummary {
	return launchpadSummary{ID: launchpad.ID, Name: launchpad.Name, Description: launchpad.Description}
}

func parseLaunchpadID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeLaunchpadError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func decodeLaunchpadMutation(r *http.Request, requireName bool) (launchpadMutation, error) {
	if r.Body == nil {
		return launchpadMutation{}, errInvalidLaunchpadRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return launchpadMutation{}, errInvalidLaunchpadRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return launchpadMutation{}, errInvalidLaunchpadRequest
	}
	var mutation launchpadMutation
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return launchpadMutation{}, errInvalidLaunchpadRequest
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return launchpadMutation{}, errInvalidLaunchpadRequest
		}
		switch name {
		case "name":
			mutation.Name = &value
		case "description":
			mutation.Description = &value
		default:
			return launchpadMutation{}, errInvalidLaunchpadRequest
		}
	}
	if requireName && mutation.Name == nil || mutation.Name != nil && *mutation.Name == "" {
		return launchpadMutation{}, errInvalidLaunchpadRequest
	}
	return mutation, nil
}

func writeLaunchpadError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
