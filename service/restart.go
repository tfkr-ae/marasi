package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/domain"
)

// Successor is the instance that took over after a restart.
type Successor struct {
	Version       string  // version the successor reports
	ProxyListener *string // successor's proxy listener, nil when stopped
}

// RestartError is a successor that failed or did not become ready in time.
type RestartError struct {
	Log string // log file the successor wrote to
	Err error
}

func (e *RestartError) Error() string { return e.Err.Error() }
func (e *RestartError) Unwrap() error { return e.Err }

// SuccessorStart hands the instance name to a successor started from the
// installed build, with project open and proxyListener bound (none when nil),
// and waits for it to report ready. The Server has already stopped the proxy
// listener and released the project. On failure SuccessorStart has taken the
// instance name back before it returns, and the error is a *RestartError.
type SuccessorStart func(ctx context.Context, project string, proxyListener *string) (Successor, error)

// EnableRestart serves POST /service/restart with start (ADR-0051).
func (s *Server) EnableRestart(start SuccessorStart) {
	s.restartMu.Lock()
	s.restart = start
	s.restartMu.Unlock()
}

// heldCheckpointItem is a held Checkpoint item that a restart would drop.
type heldCheckpointItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

func heldCheckpointItems(items []domain.CheckpointItem) []heldCheckpointItem {
	held := make([]heldCheckpointItem, 0, len(items))
	for _, item := range items {
		entry := heldCheckpointItem{ID: item.ID.String(), Type: item.Type}
		if item.Type == domain.CheckpointTypeWebSocket {
			entry.Summary = item.Direction
		} else {
			entry.Summary, _, _ = strings.Cut(string(item.Raw), "\n")
			entry.Summary = strings.TrimSuffix(entry.Summary, "\r")
		}
		held = append(held, entry)
	}
	return held
}

var errInvalidRestartRequest = errors.New("invalid restart request")

// decodeRestart reads an optional {"force": bool} body.
func decodeRestart(r *http.Request) (bool, error) {
	if r.Body == nil {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return false, errInvalidRestartRequest
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return false, errInvalidRestartRequest
	}
	force := false
	for name, raw := range fields {
		if name != "force" {
			return false, errInvalidRestartRequest
		}
		if err := json.Unmarshal(raw, &force); err != nil || string(raw) == "null" {
			return false, errInvalidRestartRequest
		}
	}
	return force, nil
}

// serveRestart hands this instance over to a successor, or keeps it running
// on the old build when the successor fails (ADR-0051).
func (s *Server) serveRestart(w http.ResponseWriter, r *http.Request) {
	s.restartMu.Lock()
	start := s.restart
	if start == nil || s.projects == nil || s.listener == nil {
		s.restartMu.Unlock()
		writeJSON(w, r, http.StatusNotImplemented, map[string]string{"error": "restart_unavailable"})
		return
	}
	if s.restarting {
		s.restartMu.Unlock()
		writeJSON(w, r, http.StatusConflict, map[string]string{"error": "restart_in_progress"})
		return
	}
	s.restarting = true
	s.restartMu.Unlock()
	handedOver := false
	defer func() {
		if !handedOver {
			s.restartMu.Lock()
			s.restarting = false
			s.restartMu.Unlock()
		}
	}()
	// A restart never cancels or races an install, swap included: it waits
	// for none to run and keeps new ones from starting until it fails.
	if !s.installer.holdForRestart() {
		writeJSON(w, r, http.StatusConflict, updateErrorBody{Error: "update_in_progress", Message: "an update is installing on this instance; restart once it ends"})
		return
	}
	defer func() {
		if !handedOver {
			s.installer.releaseRestart()
		}
	}()

	force, err := decodeRestart(r)
	if err != nil {
		writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_restart_request", "message": `body must be empty or {"force": true|false}`})
		return
	}
	// Without force nothing held is dropped: every drop below checks first,
	// and the last check runs once no project work is left to hold an item.
	dropHeld := func() error {
		if force {
			s.dropPendingCheckpoint()
			return nil
		}
		if held := heldCheckpointItems(s.proxy.CheckpointItems()); len(held) != 0 {
			return &heldItemsError{items: held}
		}
		return nil
	}
	if err := dropHeld(); err != nil {
		writeHeldItems(w, r, err.(*heldItemsError))
		return
	}

	// A restart finishes once begun, even if its client leaves: half a
	// handover would leave the project and listener unowned.
	ctx := context.WithoutCancel(r.Context())
	proxyListener := s.listener.Status().ProxyListener
	s.events.publish("service.restarting", struct {
		Instance      string  `json:"instance"`
		Project       string  `json:"project"`
		ProxyListener *string `json:"proxy_listener"`
	}{Instance: s.instance, Project: s.projects.Path(), ProxyListener: proxyListener})

	var stepErr error
	if proxyListener != nil {
		_, stepErr = s.listener.Stop(ctx)
	}
	var release projectRelease
	if stepErr == nil {
		release, stepErr = s.projects.release(ctx, dropHeld)
	}
	var successor Successor
	if stepErr == nil {
		successor, stepErr = start(ctx, release.path, proxyListener)
	}
	if stepErr != nil {
		s.rollBackRestart(w, r, ctx, release, proxyListener, stepErr)
		return
	}

	handedOver = true
	release.abandon()
	s.exit()
	writeJSON(w, r, http.StatusOK, struct {
		Instance      string  `json:"instance"`
		Status        string  `json:"status"`
		Version       string  `json:"version"`
		ProxyListener *string `json:"proxy_listener"`
	}{Instance: s.instance, Status: "restarted", Version: successor.Version, ProxyListener: successor.ProxyListener})
}

// rollBackRestart takes the project and listener back after a failed
// handover and reports the failure.
func (s *Server) rollBackRestart(w http.ResponseWriter, r *http.Request, ctx context.Context, release projectRelease, proxyListener *string, cause error) {
	var rollbackErr error
	if release.reopen != nil {
		rollbackErr = wrapRestartError("reopening project", release.reopen(ctx))
	}
	if proxyListener != nil && s.listener.Status().ProxyListener == nil {
		rollbackErr = errors.Join(rollbackErr, wrapRestartError("restarting proxy listener", startListenerAt(ctx, s.listener, *proxyListener)))
	}
	var held *heldItemsError
	if errors.As(cause, &held) {
		s.publishRestartFailed(cause.Error(), "")
		if rollbackErr != nil {
			writeJSON(w, r, http.StatusInternalServerError, updateErrorBody{Error: "restart_failed", Message: fmt.Sprintf("%v; %v", cause, rollbackErr)})
			return
		}
		writeHeldItems(w, r, held)
		return
	}
	var restartErr *RestartError
	logPath := ""
	if errors.As(cause, &restartErr) {
		logPath = restartErr.Log
	}
	message := cause.Error()
	if logPath != "" {
		message = fmt.Sprintf("%s (successor log: %s)", message, logPath)
	}
	if rollbackErr != nil {
		message = fmt.Sprintf("%s; %v", message, rollbackErr)
	}
	s.publishRestartFailed(cause.Error(), logPath)
	writeJSON(w, r, http.StatusInternalServerError, struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Log     string `json:"log,omitempty"`
	}{Error: "restart_failed", Message: message, Log: logPath})
}

func (s *Server) publishRestartFailed(message, logPath string) {
	s.events.publish("service.restart_failed", struct {
		Instance string `json:"instance"`
		Error    string `json:"error"`
		Log      string `json:"log,omitempty"`
	}{Instance: s.instance, Error: message, Log: logPath})
}

// heldItemsError refuses a restart that would drop held Checkpoint items.
type heldItemsError struct {
	items []heldCheckpointItem
}

func (e *heldItemsError) Error() string {
	return fmt.Sprintf("%d held Checkpoint items would be dropped; forward or drop them, or force the restart", len(e.items))
}

func writeHeldItems(w http.ResponseWriter, r *http.Request, held *heldItemsError) {
	writeJSON(w, r, http.StatusConflict, struct {
		Error   string               `json:"error"`
		Message string               `json:"message"`
		Items   []heldCheckpointItem `json:"items"`
	}{Error: "checkpoint_items_held", Message: held.Error(), Items: held.items})
}

func wrapRestartError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

// startListenerAt starts the proxy listener on endpoint, a host:port.
func startListenerAt(ctx context.Context, listener ListenerLifecycle, endpoint string) error {
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		return err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return err
	}
	value := uint16(port)
	_, err = listener.Start(ctx, ListenerSettings{Address: &host, Port: &value})
	return err
}
