package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/internal/update"
)

// Install states reported by GET /update.
const (
	installIdle      = "idle"
	installRunning   = "running"
	installInstalled = "installed"
	installFailed    = "failed"
)

// installState is the install of this instance, the running one or the
// last one to finish.
type installState struct {
	State    string          `json:"state"`
	Version  string          `json:"version,omitempty"`
	Step     update.Step     `json:"step,omitempty"`
	Received *int64          `json:"received,omitempty"`
	Total    *int64          `json:"total,omitempty"`
	Error    *installError   `json:"error,omitempty"`
	progress update.Progress // last progress, copied into Received and Total
}

// installError is why an install failed; also the update.failed payload
// without instance and version.
type installError struct {
	Kind    update.Kind `json:"kind"`
	Step    update.Step `json:"step"`
	Message string      `json:"message"`
	Page    string      `json:"page,omitempty"`
}

func (s installState) snapshot() installState {
	if s.State == installRunning || s.State == installInstalled || (s.State == installFailed && s.Step != "") {
		received, total := s.progress.Received, s.progress.Total
		s.Received, s.Total = &received, &total
	}
	return s
}

// runningInstall is the install this instance runs.
type runningInstall struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// updateInstaller runs at most one install per instance. Installs run on
// its own context, not the request's: an install outlives its client
// (ADR-0050). Across instances the install lock in the config dir keeps it
// to one per installed build.
type updateInstaller struct {
	ctx       context.Context
	cancelAll context.CancelFunc
	wg        sync.WaitGroup

	mu      sync.Mutex
	running *runningInstall
	state   installState
	// installed is the version this instance installed, so a repeated
	// request does not install it again before the restart.
	installed string

	// announced is the update last announced in update.available.
	announcedMu sync.Mutex
	announced   string
}

func newUpdateInstaller() *updateInstaller {
	ctx, cancel := context.WithCancel(context.Background())
	return &updateInstaller{ctx: ctx, cancelAll: cancel, state: installState{State: installIdle}}
}

// close cancels a running install before its swap and waits for it to end.
func (in *updateInstaller) close() {
	in.cancelAll()
	in.wg.Wait()
}

func (in *updateInstaller) current() installState {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state.snapshot()
}

// addUpdateRoutes registers the update routes. They do not touch the open
// project, so they are not admitted and a project switch does not block
// them.
func addUpdateRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /update", s.serveUpdate)
	mux.HandleFunc("POST /update/check", s.serveUpdateCheck)
	mux.HandleFunc("POST /update/install", s.serveUpdateInstall)
	mux.HandleFunc("DELETE /update/install", s.serveUpdateCancel)
	mux.HandleFunc("PUT /update/channel", s.serveUpdateChannel)
	mux.HandleFunc("POST /update/reinstall", s.serveUpdateReinstall)
}

// updateBuildBody is a build on a channel.
type updateBuildBody struct {
	Version   string         `json:"version"`
	Channel   update.Channel `json:"channel"`
	Published *time.Time     `json:"published"`
	Notes     string         `json:"notes"`
	Page      string         `json:"page"`
}

func updateBuildFromCandidate(c *update.Candidate) *updateBuildBody {
	if c == nil {
		return nil
	}
	body := &updateBuildBody{Version: c.Build.Version, Channel: c.Channel, Notes: c.Notes, Page: c.Page}
	if !c.Build.Published.IsZero() {
		published := c.Build.Published.UTC()
		body.Published = &published
	}
	return body
}

type updateCheckBody struct {
	At     time.Time        `json:"at"`
	Latest *updateBuildBody `json:"latest"`
	Error  *string          `json:"error"`
}

type updateInstalledBody struct {
	Version string  `json:"version"`
	Channel *string `json:"channel"`
}

// updateBody is the GET /update response.
type updateBody struct {
	Installed       updateInstalledBody `json:"installed"`
	Development     bool                `json:"development"`
	Channel         *update.Channel     `json:"channel"`
	AutoCheck       bool                `json:"auto_check"`
	LastCheck       *updateCheckBody    `json:"last_check"`
	Available       *updateBuildBody    `json:"available"`
	Install         installState        `json:"install"`
	RestartRequired bool                `json:"restart_required"`
	InstallPath     string              `json:"install_path"`
}

func (s *Server) updateBody(status UpdateStatus) updateBody {
	body := updateBody{
		Installed:       updateInstalledBody{Version: status.Installed.Version},
		Development:     status.Development,
		AutoCheck:       status.AutoCheck,
		Available:       updateBuildFromCandidate(status.Available),
		Install:         s.installer.current(),
		RestartRequired: s.updates.RestartRequired(),
		InstallPath:     s.updates.InstallPath(),
	}
	if s.channel != "" {
		channel := s.channel
		body.Installed.Channel = &channel
	}
	if status.Channel != "" {
		channel := status.Channel
		body.Channel = &channel
	}
	if last := status.LastCheck; last != nil {
		body.LastCheck = &updateCheckBody{At: last.At.UTC(), Latest: updateBuildFromCandidate(last.Latest)}
		if last.Error != "" {
			message := last.Error
			body.LastCheck.Error = &message
		}
	}
	return body
}

type updateErrorBody struct {
	Error    string `json:"error"`
	Message  string `json:"message,omitempty"`
	Instance string `json:"instance,omitempty"`
	Page     string `json:"page,omitempty"`
}

func writeUpdateError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, r, status, updateErrorBody{Error: code, Message: message})
}

// writeUpdateFailure maps an error from Updates to a response.
func writeUpdateFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, update.ErrDevBuild) {
		writeUpdateError(w, r, http.StatusConflict, "development_build", "a development build does not check for or install updates")
		return
	}
	writeUpdateError(w, r, http.StatusInternalServerError, "internal_server_error", "")
}

// updatesReady reports whether the instance has an updates component; unit
// test servers may not.
func (s *Server) updatesReady(w http.ResponseWriter, r *http.Request) bool {
	if s.updates == nil {
		writeUpdateError(w, r, http.StatusNotImplemented, "updates_unavailable", "")
		return false
	}
	return true
}

func (s *Server) serveUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.updatesReady(w, r) {
		return
	}
	status, err := s.updates.Status(r.Context())
	if err != nil {
		writeUpdateFailure(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.updateBody(status))
}

func (s *Server) serveUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.updatesReady(w, r) {
		return
	}
	status, err := s.updates.Check(r.Context())
	if err != nil {
		writeUpdateFailure(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.updateBody(status))
}

func (s *Server) serveUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if !s.updatesReady(w, r) {
		return
	}
	channel, err := decodeUpdateField[string](r, "channel")
	if err != nil {
		writeUpdateError(w, r, http.StatusBadRequest, "invalid_update_request", `the body must be {"channel": "release" or "nightly"}`)
		return
	}
	if s.updates.Updater() == nil {
		writeUpdateFailure(w, r, update.ErrDevBuild)
		return
	}
	if _, err := update.ParseChannel(channel); err != nil {
		writeUpdateError(w, r, http.StatusBadRequest, "invalid_update_request", err.Error())
		return
	}
	status, err := s.updates.SetChannel(r.Context(), update.Channel(channel))
	if err != nil {
		writeUpdateFailure(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.updateBody(status))
}

// installResponse is the POST /update/install and /update/reinstall
// response: the install that runs, and whether the request joined it.
type installResponse struct {
	Joined      bool         `json:"joined"`
	Install     installState `json:"install"`
	InstallPath string       `json:"install_path"`
}

func (s *Server) serveUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !s.updatesReady(w, r) {
		return
	}
	if s.updates.Updater() == nil {
		writeUpdateFailure(w, r, update.ErrDevBuild)
		return
	}
	if s.joinInstall(w, r) {
		return
	}
	status, err := s.updates.Check(r.Context())
	if err != nil {
		writeUpdateFailure(w, r, err)
		return
	}
	if status.Available == nil {
		if status.LastCheck != nil && status.LastCheck.Error != "" {
			writeUpdateError(w, r, http.StatusBadGateway, "check_failed", status.LastCheck.Error)
			return
		}
		writeUpdateError(w, r, http.StatusConflict, "no_update", fmt.Sprintf("%s is the latest build on the %s channel", status.Installed.Version, status.Channel))
		return
	}
	s.startInstall(w, r, *status.Available, false)
}

func (s *Server) serveUpdateReinstall(w http.ResponseWriter, r *http.Request) {
	if !s.updatesReady(w, r) {
		return
	}
	confirm, err := decodeReinstall(r)
	if err != nil {
		writeUpdateError(w, r, http.StatusBadRequest, "invalid_update_request", `the body must be {"confirm": true}`)
		return
	}
	if !confirm {
		writeUpdateError(w, r, http.StatusBadRequest, "confirmation_required", "a reinstall replaces the installed build with the latest build on its channel even when that build is older, and projects opened in a newer build may not open in it; send {\"confirm\": true}")
		return
	}
	if s.updates.Updater() == nil {
		writeUpdateFailure(w, r, update.ErrDevBuild)
		return
	}
	if s.joinInstall(w, r) {
		return
	}
	latest, err := s.updates.Latest(r.Context())
	if errors.Is(err, update.ErrDevBuild) {
		writeUpdateFailure(w, r, err)
		return
	}
	if err != nil {
		writeUpdateError(w, r, http.StatusBadGateway, "check_failed", err.Error())
		return
	}
	if !latest.Signed() {
		writeJSON(w, r, http.StatusConflict, updateErrorBody{Error: "not_signed", Message: fmt.Sprintf("build %s is not signed, so Marasi cannot verify it; install it by hand", latest.Build.Version), Page: latest.Page})
		return
	}
	s.startInstall(w, r, latest, true)
}

// joinInstall answers with the install already running on this instance,
// if there is one.
func (s *Server) joinInstall(w http.ResponseWriter, r *http.Request) bool {
	in := s.installer
	in.mu.Lock()
	running := in.running != nil
	state := in.state.snapshot()
	in.mu.Unlock()
	if running {
		writeJSON(w, r, http.StatusAccepted, installResponse{Joined: true, Install: state, InstallPath: s.updates.InstallPath()})
	}
	return running
}

// startInstall starts installing c, or joins the install another request
// started meanwhile. reinstall allows the build this instance installed.
func (s *Server) startInstall(w http.ResponseWriter, r *http.Request, c update.Candidate, reinstall bool) {
	in := s.installer
	updater := s.updates.Updater()
	target := s.updates.InstallPath()
	in.mu.Lock()
	if in.running != nil {
		state := in.state.snapshot()
		in.mu.Unlock()
		writeJSON(w, r, http.StatusAccepted, installResponse{Joined: true, Install: state, InstallPath: target})
		return
	}
	defer in.mu.Unlock()
	if !reinstall && in.installed == c.Build.Version {
		writeUpdateError(w, r, http.StatusConflict, "already_installed", fmt.Sprintf("build %s is installed; restart the instance to run it", c.Build.Version))
		return
	}
	if target == "" {
		writeUpdateError(w, r, http.StatusInternalServerError, "install_failed", "the installed build could not be found")
		return
	}
	if _, ok := c.Asset(updater.AssetName()); !ok {
		writeJSON(w, r, http.StatusConflict, updateErrorBody{Error: "no_build", Message: fmt.Sprintf("no build for this platform (%s)", updater.AssetName()), Page: c.Page})
		return
	}
	if err := update.CheckWritable(target); err != nil {
		writeJSON(w, r, http.StatusConflict, updateErrorBody{Error: "install_path_not_writable", Message: err.Error(), Page: c.Page})
		return
	}
	lock, err := update.AcquireLock(s.updates.configDir, target, s.instance)
	var locked *update.LockedError
	if errors.As(err, &locked) {
		writeJSON(w, r, http.StatusConflict, updateErrorBody{Error: "update_in_progress", Message: locked.Error(), Instance: locked.Holder})
		return
	}
	if err != nil {
		writeUpdateError(w, r, http.StatusInternalServerError, "install_failed", err.Error())
		return
	}

	ctx, cancel := context.WithCancel(in.ctx)
	running := &runningInstall{cancel: cancel, done: make(chan struct{})}
	in.running = running
	in.state = installState{State: installRunning, Version: c.Build.Version, Step: update.StepDownload}
	state := in.state.snapshot()
	in.wg.Add(1)
	go s.runInstall(ctx, running, lock, updater, c, target)
	writeJSON(w, r, http.StatusAccepted, installResponse{Install: state, InstallPath: target})
}

type updateProgressEvent struct {
	Instance string      `json:"instance"`
	Version  string      `json:"version"`
	Step     update.Step `json:"step"`
	Received int64       `json:"received"`
	Total    int64       `json:"total"`
}

type updateInstalledEvent struct {
	Instance    string `json:"instance"`
	Version     string `json:"version"`
	InstallPath string `json:"install_path"`
}

type updateFailedEvent struct {
	Instance string `json:"instance"`
	Version  string `json:"version"`
	installError
}

func (s *Server) runInstall(ctx context.Context, running *runningInstall, lock *update.Lock, updater *update.Updater, c update.Candidate, target string) {
	in := s.installer
	defer in.wg.Done()
	defer running.cancel()
	version := c.Build.Version
	err := updater.Install(ctx, c, target, func(p update.Progress) {
		in.mu.Lock()
		defer in.mu.Unlock()
		in.state.Step = p.Step
		in.state.progress = p
		s.events.publish("update.progress", updateProgressEvent{Instance: s.instance, Version: version, Step: p.Step, Received: p.Received, Total: p.Total})
	})
	_ = lock.Release()

	in.mu.Lock()
	defer in.mu.Unlock()
	in.running = nil
	defer close(running.done)
	if err == nil {
		in.state.State = installInstalled
		in.installed = version
		s.events.publish("update.installed", updateInstalledEvent{Instance: s.instance, Version: version, InstallPath: target})
		return
	}
	failure := &update.Error{Kind: update.KindInstall, Step: in.state.Step, Err: err}
	errors.As(err, &failure)
	in.state.State = installFailed
	in.state.Step = failure.Step
	in.state.Error = &installError{Kind: failure.Kind, Step: failure.Step, Message: failure.Error(), Page: failure.Page}
	s.events.publish("update.failed", updateFailedEvent{Instance: s.instance, Version: version, installError: *in.state.Error})
}

// cancelResponse is the DELETE /update/install response.
type cancelResponse struct {
	Install installState `json:"install"`
}

func (s *Server) serveUpdateCancel(w http.ResponseWriter, r *http.Request) {
	if !s.updatesReady(w, r) {
		return
	}
	in := s.installer
	in.mu.Lock()
	running := in.running
	swapping := in.state.Step == update.StepInstall
	in.mu.Unlock()
	if running == nil {
		writeUpdateError(w, r, http.StatusConflict, "no_install", "no install is running on this instance")
		return
	}
	if swapping {
		writeUpdateError(w, r, http.StatusConflict, "install_not_cancellable", "the install is replacing the installed build and can no longer be cancelled")
		return
	}
	running.cancel()
	select {
	case <-running.done:
	case <-r.Context().Done():
		return
	}
	state := in.current()
	if state.State == installInstalled {
		writeUpdateError(w, r, http.StatusConflict, "install_not_cancellable", "the install replaced the installed build before it could be cancelled")
		return
	}
	writeJSON(w, r, http.StatusOK, cancelResponse{Install: state})
}

type updateAvailableEvent struct {
	Instance string `json:"instance"`
	updateBuildBody
}

// announceAvailable publishes update.available when the available update
// differs from the one last announced. Checks served from the cache report
// the same update again; they publish nothing.
func (s *Server) announceAvailable(status UpdateStatus) {
	in := s.installer
	key := ""
	if c := status.Available; c != nil {
		key = strings.Join([]string{c.Build.Version, c.Build.Commit, c.Build.Published.UTC().String()}, "|")
	}
	in.announcedMu.Lock()
	defer in.announcedMu.Unlock()
	if key == in.announced {
		return
	}
	in.announced = key
	if key != "" {
		s.events.publish("update.available", updateAvailableEvent{Instance: s.instance, updateBuildBody: *updateBuildFromCandidate(status.Available)})
	}
}

var errInvalidUpdateRequest = errors.New("invalid update request")

// decodeUpdateField reads a body of exactly one non-null field.
func decodeUpdateField[T any](r *http.Request, field string) (T, error) {
	var value T
	fields, err := decodeUpdateFields(r)
	if err != nil || len(fields) != 1 {
		return value, errInvalidUpdateRequest
	}
	raw, ok := fields[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return value, errInvalidUpdateRequest
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, errInvalidUpdateRequest
	}
	return value, nil
}

// decodeReinstall reads {"confirm": bool}. An empty body or {} is no
// confirmation.
func decodeReinstall(r *http.Request) (bool, error) {
	fields, err := decodeUpdateFields(r)
	if err != nil {
		return false, err
	}
	if len(fields) == 0 {
		return false, nil
	}
	raw, ok := fields["confirm"]
	if !ok || len(fields) != 1 {
		return false, errInvalidUpdateRequest
	}
	var confirm bool
	if err := json.Unmarshal(raw, &confirm); err != nil {
		return false, errInvalidUpdateRequest
	}
	return confirm, nil
}

// decodeUpdateFields reads a JSON object body; an empty body has no fields.
func decodeUpdateFields(r *http.Request) (map[string]json.RawMessage, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return nil, errInvalidUpdateRequest
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, errInvalidUpdateRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errInvalidUpdateRequest
	}
	return fields, nil
}
