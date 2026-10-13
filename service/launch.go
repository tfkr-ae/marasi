package service

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// launchRecord is the control API shape of a launch.
type launchRecord struct {
	LaunchID    uuid.UUID `json:"launch_id"`
	LaunchpadID uuid.UUID `json:"launchpad_id"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
}

type launchList struct {
	Items []launchRecord `json:"items"`
}

// launchStarted is the response to starting a launch.
type launchStarted struct {
	LaunchID uuid.UUID `json:"launch_id"`
	Status   string    `json:"status"`
}

// addLaunchRoutes registers launch reads. The registry lives in memory on the
// proxy, so these routes need no project admission.
func addLaunchRoutes(raw *http.ServeMux, proxy *marasi.Proxy) {
	raw.HandleFunc("GET /launch", func(w http.ResponseWriter, r *http.Request) {
		launches := proxy.Launches()
		items := make([]launchRecord, 0, len(launches))
		for _, launch := range launches {
			items = append(items, launchRecordFromDomain(launch))
		}
		writeJSON(w, r, http.StatusOK, launchList{Items: items})
	})

	raw.HandleFunc("GET /launch/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeLaunchpadError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		launch, err := proxy.GetLaunch(id)
		if errors.Is(err, domain.ErrLaunchNotFound) {
			writeLaunchpadError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			writeLaunchpadError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		writeJSON(w, r, http.StatusOK, launchRecordFromDomain(launch))
	})
}

func launchRecordFromDomain(launch domain.Launch) launchRecord {
	return launchRecord{
		LaunchID:    launch.ID,
		LaunchpadID: launch.LaunchpadID,
		Status:      string(launch.Status),
		Reason:      launch.Reason,
	}
}

// HandleLaunchStarted publishes launch.started once a launch is accepted.
func (s *Server) HandleLaunchStarted(launch domain.Launch) {
	s.events.publish("launch.started", launchRecordFromDomain(launch))
}

// HandleLaunchFinished publishes launch.<status> when a launch reaches a terminal state.
func (s *Server) HandleLaunchFinished(launch domain.Launch) {
	s.events.publish("launch."+string(launch.Status), launchRecordFromDomain(launch))
}
