package marasi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// launchIDHeader carries a launch id from the proxy client to the request pipeline.
// The pipeline honors it only on internal requests and never forwards it.
const launchIDHeader = "x-marasi-launch-id"

// launchEntry is a launch held in the registry until it reaches a terminal state.
type launchEntry struct {
	launch      domain.Launch
	claimed     bool                 // the pipeline has used the id as a pair UUID
	cancel      context.CancelFunc   // cancels the proxy client's request context
	done        chan struct{}        // closed once the launch is terminal
	outcome     domain.Launch        // terminal state reported by the proxy client
	failReason  string               // failure the pipeline recorded; wins over outcome
	duplicate   bool                 // storing the pair hit an existing row
	cancelled   bool                 // CancelLaunch was called while pending; wins over everything
	interrupted bool                 // a project switch or service stop ended it; wins over failures
	launched    *domain.ProxyRequest // the request as launched, stored if the pipeline never wrote it
}

type launchClaim int

const (
	launchUnknown launchClaim = iota
	launchClaimed
	launchAlreadyClaimed
)

// launchOutcomeWrite finishes a launch from the database writer, so its
// metadata update is queued after the pair's request and response writes.
type launchOutcomeWrite struct {
	id uuid.UUID
}

// Launch sends a raw HTTP request from a launchpad through the proxy client.
// It returns the launch id once the launch is accepted. The launch id is the
// UUID of the request/response pair the launch creates.
//
// Malformed raw requests and a missing Host fail at once and create no launch.
// A normal HTTP launch completes once its response is drained. A WebSocket
// launch completes at the handshake, and the client body stays open until the
// peer closes, CloseWebSocket is used, or the proxy shuts down.
func (proxy *Proxy) Launch(raw string, launchpadID string, useHttps bool) (uuid.UUID, error) {
	entry, err := proxy.startLaunch(raw, launchpadID, useHttps)
	if err != nil {
		return uuid.Nil, err
	}
	return entry.launch.ID, nil
}

// LaunchSync starts a launch like Launch and waits for it to reach a terminal
// state, then returns the final launch. When ctx is cancelled first, it
// cancels the launch and returns it once it has ended.
//
// Errors from starting the launch are returned at once, as from Launch.
func (proxy *Proxy) LaunchSync(ctx context.Context, raw string, launchpadID string, useHttps bool) (domain.Launch, error) {
	entry, err := proxy.startLaunch(raw, launchpadID, useHttps)
	if err != nil {
		return domain.Launch{}, err
	}
	select {
	case <-entry.done:
	case <-ctx.Done():
		// A launch that finished meanwhile keeps its own outcome.
		_, _ = proxy.CancelLaunch(entry.launch.ID)
		<-entry.done
	}
	return entry.final(), nil
}

// startLaunch accepts a launch, sends it in the background, and returns its
// registry entry, which stays readable after the launch leaves the registry.
func (proxy *Proxy) startLaunch(raw string, launchpadID string, useHttps bool) (*launchEntry, error) {
	launchpad, err := uuid.Parse(launchpadID)
	if err != nil {
		return nil, fmt.Errorf("parsing launchpad id : %w", err)
	}
	updated, err := rawhttp.RecalculateContentLength([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("recalculating content length : %w", err)
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(updated)))
	if err != nil {
		return nil, fmt.Errorf("reading http request : %w", err)
	}

	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	if useHttps {
		scheme = "https"
	}
	host := req.Host
	if host == "" {
		return nil, fmt.Errorf("host header not found or is empty")
	}

	req.RequestURI, req.URL.Scheme, req.URL.Host = "", scheme, host

	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "")
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry, err := proxy.registerLaunch(launchpad, cancel)
	if err != nil {
		cancel()
		return nil, err
	}
	launched, err := launchedRequest(req, entry.launch)
	if err != nil {
		proxy.launchMu.Lock()
		delete(proxy.launches, entry.launch.ID)
		proxy.launchMu.Unlock()
		cancel()
		return nil, err
	}
	proxy.launchMu.Lock()
	entry.launched = launched
	proxy.launchMu.Unlock()

	if marasiws.IsUpgradeRequest(req) {
		req.Header.Set("x-marasi-metadata", `{"websocket.held_by":"launchpad"}`)
	}
	req.Header.Set(launchIDHeader, entry.launch.ID.String())
	req = req.WithContext(ctx)

	if proxy.OnLaunchStarted != nil {
		proxy.OnLaunchStarted(entry.launch)
	}
	go proxy.runLaunch(entry.launch.ID, proxy.launchClient(), req)
	return entry, nil
}

// launchedRequest records a launch's request as the proxy client sends it,
// before any internal header is added, under the launch id.
func launchedRequest(req *http.Request, launch domain.Launch) (*domain.ProxyRequest, error) {
	metadata := map[string]any{"launchpad": true, "launchpad_id": launch.LaunchpadID}
	snapshot := core.ContextWithMetadata(core.ContextWithRequestTime(req, time.Now()), metadata)
	launched, err := NewProxyRequest(snapshot, launch.ID)
	if err != nil {
		return nil, fmt.Errorf("recording launched request : %w", err)
	}
	// NewProxyRequest buffers the body on the snapshot; hand it back to the request.
	req.Body = snapshot.Body
	return launched, nil
}

// Launches returns the pending launches, oldest first.
func (proxy *Proxy) Launches() []domain.Launch {
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	launches := make([]domain.Launch, 0, len(proxy.launches))
	for _, entry := range proxy.launches {
		launches = append(launches, entry.launch)
	}
	slices.SortFunc(launches, func(a, b domain.Launch) int {
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	return launches
}

// GetLaunch returns a launch by id. A pending launch is read from the
// registry. Once the launch has left the registry it is read from its stored
// pair's metadata, so a launch id stays valid for good. It returns
// domain.ErrLaunchNotFound when the id is neither live nor a launch's pair.
func (proxy *Proxy) GetLaunch(id uuid.UUID) (domain.Launch, error) {
	proxy.launchMu.Lock()
	entry, ok := proxy.launches[id]
	if ok {
		launch := entry.launch
		proxy.launchMu.Unlock()
		return launch, nil
	}
	proxy.launchMu.Unlock()
	return proxy.storedLaunch(id)
}

// CancelLaunch cancels a pending launch by cancelling the proxy client's
// request context, and returns the launch as it will end: cancelled.
//
// A launch that has already reached a terminal state is returned with its
// final state and domain.ErrLaunchFinished. An id that names no launch
// returns domain.ErrLaunchNotFound.
func (proxy *Proxy) CancelLaunch(id uuid.UUID) (domain.Launch, error) {
	proxy.launchMu.Lock()
	entry, ok := proxy.launches[id]
	if ok {
		if entry.outcome.Status != "" || entry.interrupted {
			final := entry.final()
			proxy.launchMu.Unlock()
			return final, domain.ErrLaunchFinished
		}
		entry.cancelled = true
		launch := entry.launch
		proxy.launchMu.Unlock()
		entry.cancel()
		launch.Status = domain.LaunchCancelled
		return launch, nil
	}
	proxy.launchMu.Unlock()

	stored, err := proxy.storedLaunch(id)
	if err != nil {
		return domain.Launch{}, err
	}
	return stored, domain.ErrLaunchFinished
}

// InterruptLaunches ends every pending launch as failed with reason
// interrupted, because the open project is being switched or the service is
// stopping. It cancels each launch and returns once all of them have ended,
// so their outcomes are written to the project they were launched in.
//
// A launch already cancelled stays cancelled, and one that already reached a
// terminal state keeps it. Both are still waited for.
func (proxy *Proxy) InterruptLaunches() {
	proxy.launchMu.Lock()
	entries := make([]*launchEntry, 0, len(proxy.launches))
	for _, entry := range proxy.launches {
		if entry.outcome.Status == "" && !entry.cancelled {
			entry.interrupted = true
		}
		entries = append(entries, entry)
	}
	proxy.launchMu.Unlock()

	for _, entry := range entries {
		entry.cancel()
	}
	for _, entry := range entries {
		<-entry.done
	}
}

// final returns the launch's terminal state. A cancel wins over everything,
// an interruption wins over failures it caused, and a failure the pipeline
// recorded wins over the proxy client's outcome.
// It must be called with launchMu held, or after the entry left the registry.
func (entry *launchEntry) final() domain.Launch {
	final := entry.outcome
	final.ID, final.LaunchpadID = entry.launch.ID, entry.launch.LaunchpadID
	switch {
	case entry.cancelled:
		final.Status, final.Reason = domain.LaunchCancelled, ""
	case entry.interrupted:
		final.Status, final.Reason = domain.LaunchFailed, domain.LaunchReasonInterrupted
	case entry.failReason != "":
		final.Status, final.Reason = domain.LaunchFailed, entry.failReason
	}
	return final
}

// storedLaunch reads a finished launch from its stored pair's metadata.
// It returns domain.ErrLaunchNotFound when the pair is missing or did not
// come from a finished launch.
func (proxy *Proxy) storedLaunch(id uuid.UUID) (domain.Launch, error) {
	if proxy.TrafficRepo == nil {
		return domain.Launch{}, domain.ErrLaunchNotFound
	}
	metadata, err := proxy.TrafficRepo.GetMetadata(id)
	if err != nil {
		return domain.Launch{}, domain.ErrLaunchNotFound
	}
	outcome, ok := metadata["launch"].(map[string]any)
	if !ok {
		return domain.Launch{}, domain.ErrLaunchNotFound
	}
	status, _ := outcome["status"].(string)
	if status == "" {
		return domain.Launch{}, domain.ErrLaunchNotFound
	}
	reason, _ := outcome["reason"].(string)
	launchpadID, _ := metadata["launchpad_id"].(string)
	launchpad, _ := uuid.Parse(launchpadID)
	return domain.Launch{ID: id, LaunchpadID: launchpad, Status: domain.LaunchStatus(status), Reason: reason}, nil
}

// registerLaunch adds a pending launch under an id the registry does not already hold.
func (proxy *Proxy) registerLaunch(launchpadID uuid.UUID, cancel context.CancelFunc) (*launchEntry, error) {
	newID := proxy.newLaunchID
	if newID == nil {
		newID = uuid.NewV7
	}
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	if proxy.launches == nil {
		proxy.launches = make(map[uuid.UUID]*launchEntry)
	}
	for {
		id, err := newID()
		if err != nil {
			return nil, fmt.Errorf("generating launch id : %w", err)
		}
		if _, held := proxy.launches[id]; held {
			continue
		}
		entry := &launchEntry{
			launch: domain.Launch{ID: id, LaunchpadID: launchpadID, Status: domain.LaunchPending},
			cancel: cancel,
			done:   make(chan struct{}),
		}
		proxy.launches[id] = entry
		return entry, nil
	}
}

// claimLaunch marks a pending launch as used by the request pipeline.
// Only the first claim of an id succeeds.
func (proxy *Proxy) claimLaunch(id uuid.UUID) (domain.Launch, launchClaim) {
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	entry, ok := proxy.launches[id]
	if !ok {
		return domain.Launch{}, launchUnknown
	}
	if entry.claimed {
		return entry.launch, launchAlreadyClaimed
	}
	entry.claimed = true
	return entry.launch, launchClaimed
}

// launchAwaitingClaim reports whether id is a pending launch the pipeline has not claimed yet.
func (proxy *Proxy) launchAwaitingClaim(id uuid.UUID) bool {
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	entry, ok := proxy.launches[id]
	return ok && !entry.claimed
}

// recordLaunchFailure records why the pipeline failed a claimed launch.
// It does nothing for ordinary traffic. The first recorded reason stands.
func (proxy *Proxy) recordLaunchFailure(id uuid.UUID, reason string) {
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	entry, ok := proxy.launches[id]
	if !ok || !entry.claimed || entry.failReason != "" {
		return
	}
	entry.failReason = reason
	if reason == domain.LaunchReasonDuplicateID {
		entry.duplicate = true
	}
}

// recordLaunchDropped records that the pipeline dropped a claimed launch.
// A request that never claimed a launch, such as a second claim, has no
// request id in its context, so it cannot fail the launch it named.
func (proxy *Proxy) recordLaunchDropped(req *http.Request) {
	if id, ok := core.RequestIDFromContext(req.Context()); ok {
		proxy.recordLaunchFailure(id, domain.LaunchReasonDropped)
	}
}

// isClaimedLaunch reports whether id is a pending launch the pipeline has claimed.
func (proxy *Proxy) isClaimedLaunch(id uuid.UUID) bool {
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	entry, ok := proxy.launches[id]
	return ok && entry.claimed
}

// launchStoredElsewhere reports whether id belongs to a launch whose pair
// collided with an existing row, so writes for it must not touch that row.
func (proxy *Proxy) launchStoredElsewhere(id uuid.UUID) bool {
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	entry, ok := proxy.launches[id]
	return ok && entry.duplicate
}

// launchClient returns a client that opens one connection per launch and
// never reuses it, so the transport cannot retry a launch on a reused connection.
func (proxy *Proxy) launchClient() *http.Client {
	base := proxy.Client
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	transport, ok := client.Transport.(*http.Transport)
	if client.Transport == nil {
		transport, ok = http.DefaultTransport.(*http.Transport)
	}
	if ok {
		transport = transport.Clone()
		transport.DisableKeepAlives = true
		client.Transport = transport
	}
	return &client
}

// runLaunch sends a launch and reports how it ended.
func (proxy *Proxy) runLaunch(id uuid.UUID, client *http.Client, req *http.Request) {
	res, err := client.Do(req)
	if err != nil {
		log.Printf("launch %s : %v", id, err)
		proxy.endLaunch(id, domain.LaunchFailed, domain.LaunchReasonNetworkError)
		return
	}

	if marasiws.IsUpgradeResponse(res) {
		proxy.holdLaunchpadWebSocket(res.Body)
		proxy.endLaunch(id, domain.LaunchCompleted, "")
		return
	}

	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	proxy.endLaunch(id, domain.LaunchCompleted, "")
}

// endLaunch records the proxy client's outcome and finishes the launch.
// When the database writer runs, the launch finishes from the writer so its
// metadata update follows the pair's own writes.
func (proxy *Proxy) endLaunch(id uuid.UUID, status domain.LaunchStatus, reason string) {
	proxy.launchMu.Lock()
	entry, ok := proxy.launches[id]
	if ok {
		entry.outcome = domain.Launch{ID: id, LaunchpadID: entry.launch.LaunchpadID, Status: status, Reason: reason}
	}
	proxy.launchMu.Unlock()
	if !ok {
		return
	}
	if proxy.dbWriterStarted.Load() && proxy.DBWriteChannel != nil {
		proxy.DBWriteChannel <- &launchOutcomeWrite{id: id}
		return
	}
	proxy.finishLaunch(id)
}

// finishLaunch removes a launch from the registry, records its terminal state
// in the pair's metadata, and reports it.
func (proxy *Proxy) finishLaunch(id uuid.UUID) {
	proxy.launchMu.Lock()
	entry, ok := proxy.launches[id]
	if ok {
		delete(proxy.launches, id)
	}
	proxy.launchMu.Unlock()
	if !ok {
		return
	}

	final := entry.final()
	if !entry.duplicate {
		proxy.storeLaunchedRequest(entry)
		if err := proxy.applyLaunchOutcome(final); err != nil {
			log.Printf("recording launch %s outcome : %v", id, err)
		}
	}
	entry.cancel()
	close(entry.done)
	if proxy.OnLaunchFinished != nil {
		proxy.OnLaunchFinished(final)
	}
}

// storeLaunchedRequest stores the request as launched when the launch ended
// before the pipeline wrote its pair, so every launch leaves its request in its launchpad.
func (proxy *Proxy) storeLaunchedRequest(entry *launchEntry) {
	if proxy.TrafficRepo == nil || entry.launched == nil {
		return
	}
	if _, err := proxy.TrafficRepo.GetMetadata(entry.launch.ID); err == nil {
		return
	}
	proxy.writeRequest(entry.launched)
}

// applyLaunchOutcome merges the launch's terminal state into its pair's metadata.
// A launch whose pair was never stored has nothing to update.
func (proxy *Proxy) applyLaunchOutcome(launch domain.Launch) error {
	if proxy.TrafficRepo == nil {
		return nil
	}
	metadata, err := proxy.TrafficRepo.GetMetadata(launch.ID)
	if err != nil {
		return nil
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	outcome := map[string]any{"status": string(launch.Status)}
	if launch.Reason != "" {
		outcome["reason"] = launch.Reason
	}
	metadata["launch"] = outcome
	return proxy.TrafficRepo.UpdateMetadata(metadata, launch.ID)
}

// launchFailureRoundTripper records upstream round-trip failures of claimed launches.
type launchFailureRoundTripper struct {
	proxy *Proxy
	base  http.RoundTripper
}

// RoundTrip satisfies http.RoundTripper.
func (rt *launchFailureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := rt.base.RoundTrip(req)
	if err != nil {
		if id, ok := core.RequestIDFromContext(req.Context()); ok {
			rt.proxy.recordLaunchFailure(id, domain.LaunchReasonUpstreamError)
		}
	}
	return res, err
}

// holdLaunchpadWebSocket keeps a Launchpad WebSocket response body alive and drains it.
func (proxy *Proxy) holdLaunchpadWebSocket(body io.ReadCloser) {
	if body == nil {
		return
	}

	proxy.launchpadWSMu.Lock()
	if proxy.launchpadWS == nil {
		proxy.launchpadWS = make(map[io.Closer]struct{})
	}
	proxy.launchpadWS[body] = struct{}{}
	proxy.launchpadWSMu.Unlock()

	go func() {
		defer func() {
			proxy.launchpadWSMu.Lock()
			delete(proxy.launchpadWS, body)
			proxy.launchpadWSMu.Unlock()
			_ = body.Close()
		}()
		_, _ = io.Copy(io.Discard, body)
	}()
}

// closeLaunchpadWebSockets closes every held Launchpad WebSocket response body.
func (proxy *Proxy) closeLaunchpadWebSockets() {
	proxy.launchpadWSMu.Lock()
	bodies := make([]io.Closer, 0, len(proxy.launchpadWS))
	for body := range proxy.launchpadWS {
		bodies = append(bodies, body)
	}
	proxy.launchpadWS = make(map[io.Closer]struct{})
	proxy.launchpadWSMu.Unlock()

	for _, body := range bodies {
		_ = body.Close()
	}
}
