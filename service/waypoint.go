package service

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type waypointSummary struct {
	Hostname string `json:"hostname"`
	Override string `json:"override"`
}

type waypointList struct {
	Items []waypointSummary `json:"items"`
}

type waypointRequest struct {
	Hostname *string `json:"hostname"`
	Override *string `json:"override"`
}

type waypointRemoveRequest struct {
	Hostname *string `json:"hostname"`
}

var errInvalidWaypointRequest = errors.New("invalid waypoint request")

func addWaypointRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /waypoint", func(w http.ResponseWriter, r *http.Request) {
		repo, err := proxy.GetWaypointRepo()
		if err != nil {
			writeWaypointError(w, r, http.StatusNotFound, "not_found")
			return
		}
		response, err := getWaypointList(repo)
		if err != nil {
			writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /waypoint", func(w http.ResponseWriter, r *http.Request) {
		waypoint, err := decodeWaypointRequest(r)
		if err != nil {
			writeWaypointError(w, r, http.StatusBadRequest, "invalid_waypoint_request")
			return
		}
		repo, err := proxy.GetWaypointRepo()
		if err != nil {
			writeWaypointError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := repo.CreateWaypoint(waypoint.Hostname, waypoint.Override); err != nil {
			if errors.Is(err, domain.ErrWaypointAlreadyExists) {
				writeWaypointError(w, r, http.StatusConflict, "waypoint_already_exists")
			} else {
				writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
			}
			return
		}
		if err := proxy.SyncWaypoints(); err != nil {
			writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response, err := getWaypointList(repo)
		if err != nil {
			writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		events.publish("waypoint.added", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /waypoint/update", func(w http.ResponseWriter, r *http.Request) {
		waypoint, err := decodeWaypointRequest(r)
		if err != nil {
			writeWaypointError(w, r, http.StatusBadRequest, "invalid_waypoint_request")
			return
		}
		repo, err := proxy.GetWaypointRepo()
		if err != nil {
			writeWaypointError(w, r, http.StatusNotFound, "not_found")
			return
		}
		changed, err := repo.UpdateWaypoint(waypoint.Hostname, waypoint.Override)
		if err != nil {
			writeWaypointRepositoryError(w, r, err)
			return
		}
		if changed {
			if err := proxy.SyncWaypoints(); err != nil {
				writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
				return
			}
		}
		response, err := getWaypointList(repo)
		if err != nil {
			writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		if changed {
			events.publish("waypoint.updated", response)
		}
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /waypoint", func(w http.ResponseWriter, r *http.Request) {
		hostname, err := decodeWaypointRemoveRequest(r)
		if err != nil {
			writeWaypointError(w, r, http.StatusBadRequest, "invalid_waypoint_request")
			return
		}
		repo, err := proxy.GetWaypointRepo()
		if err != nil {
			writeWaypointError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := repo.DeleteWaypoint(hostname); err != nil {
			writeWaypointRepositoryError(w, r, err)
			return
		}
		if err := proxy.SyncWaypoints(); err != nil {
			writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response, err := getWaypointList(repo)
		if err != nil {
			writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		events.publish("waypoint.removed", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}

func decodeWaypointRequest(r *http.Request) (waypointSummary, error) {
	if r.Body == nil {
		return waypointSummary{}, errInvalidWaypointRequest
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request waypointRequest
	if err := decoder.Decode(&request); err != nil || request.Hostname == nil || request.Override == nil {
		return waypointSummary{}, errInvalidWaypointRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return waypointSummary{}, errInvalidWaypointRequest
	}
	hostname := strings.TrimSpace(*request.Hostname)
	override := strings.TrimSpace(*request.Override)
	if hostname == "" || override == "" {
		return waypointSummary{}, errInvalidWaypointRequest
	}
	if _, _, err := net.SplitHostPort(hostname); err != nil {
		return waypointSummary{}, errInvalidWaypointRequest
	}
	if _, _, err := net.SplitHostPort(override); err != nil {
		return waypointSummary{}, errInvalidWaypointRequest
	}
	return waypointSummary{Hostname: hostname, Override: override}, nil
}

func decodeWaypointRemoveRequest(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", errInvalidWaypointRequest
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request waypointRemoveRequest
	if err := decoder.Decode(&request); err != nil || request.Hostname == nil {
		return "", errInvalidWaypointRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errInvalidWaypointRequest
	}
	hostname := strings.TrimSpace(*request.Hostname)
	if hostname == "" {
		return "", errInvalidWaypointRequest
	}
	if _, _, err := net.SplitHostPort(hostname); err != nil {
		return "", errInvalidWaypointRequest
	}
	return hostname, nil
}

func getWaypointList(repo domain.WaypointRepository) (waypointList, error) {
	waypoints, err := repo.GetWaypoints()
	if err != nil {
		return waypointList{}, err
	}
	items := make([]waypointSummary, 0, len(waypoints))
	for _, waypoint := range waypoints {
		items = append(items, waypointSummary{Hostname: waypoint.Hostname, Override: waypoint.Override})
	}
	return waypointList{Items: items}, nil
}

func writeWaypointError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeWaypointRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrNoWaypointForHostname) {
		writeWaypointError(w, r, http.StatusNotFound, "not_found")
		return
	}
	writeWaypointError(w, r, http.StatusInternalServerError, "internal_server_error")
}
