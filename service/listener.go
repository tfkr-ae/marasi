package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/tfkr-ae/marasi"
)

// ListenerState is an externally visible proxy-listener state.
type ListenerState string

const (
	ListenerActive   ListenerState = "active"
	ListenerInactive ListenerState = "inactive"
)

var (
	ErrListenerAlreadyActive = errors.New("listener already active")
	ErrListenerInactive      = errors.New("listener inactive")
	ErrListenerUnavailable   = errors.New("listener unavailable")
	ErrListenerCleanup       = errors.New("listener cleanup failed")
	errListenerClosed        = errors.New("listener lifecycle closed")
)

// ListenerSettings contains optional proxy-listener endpoint overrides.
type ListenerSettings struct {
	Address *string
	Port    *uint16
}

// ListenerStatus is the last completed proxy-listener state.
type ListenerStatus struct {
	Status        ListenerState `json:"status"`
	ProxyListener *string       `json:"proxy_listener"`
}

type listenerProxy interface {
	GetListener(string, string) (net.Listener, error)
	Serve(net.Listener) error
	CloseWebSocketsAndFlush() error
	Close() error
}

type listenerOperation uint8

const (
	startListener listenerOperation = iota
	stopListener
	updateListener
	shutdownListener
)

type listenerRequest struct {
	ctx       context.Context
	operation listenerOperation
	settings  ListenerSettings
	result    chan listenerResult
	state     *atomic.Uint32
}

type listenerResult struct {
	status ListenerStatus
	err    error
}

type listenerServeResult struct {
	run *listenerRun
	err error
}

type listenerRun struct {
	listener net.Listener
	started  chan struct{}
	finished chan struct{}
	state    atomic.Uint32
	serveErr error
}

const (
	listenerRunServing uint32 = iota
	listenerRunExpectedClose
	listenerRunUnexpectedEnd
)

// ListenerLifecycle starts, stops, updates, and reports one proxy listener.
type ListenerLifecycle interface {
	Status() ListenerStatus
	Start(context.Context, ListenerSettings) (ListenerStatus, error)
	Stop(context.Context) (ListenerStatus, error)
	Update(context.Context, ListenerSettings) (ListenerStatus, error)
	Shutdown() error
}

type listenerLifecycle struct {
	proxy      listenerProxy
	logWriter  io.Writer
	events     *eventBroadcaster
	requests   chan listenerRequest
	serveEnded chan listenerServeResult
	done       chan struct{}
	statusMu   sync.RWMutex
	status     ListenerStatus
}

// NewListenerLifecycle creates an inactive proxy-listener lifecycle.
func NewListenerLifecycle(proxy *marasi.Proxy, logWriter io.Writer) ListenerLifecycle {
	return newListenerLifecycle(proxy, logWriter)
}

func newListenerLifecycle(proxy listenerProxy, logWriter io.Writer) ListenerLifecycle {
	if logWriter == nil {
		logWriter = io.Discard
	}
	lifecycle := &listenerLifecycle{
		proxy:      proxy,
		logWriter:  logWriter,
		events:     newEventBroadcaster(),
		requests:   make(chan listenerRequest, 64),
		serveEnded: make(chan listenerServeResult, 1),
		done:       make(chan struct{}),
		status:     ListenerStatus{Status: ListenerInactive},
	}
	go lifecycle.run()
	return lifecycle
}

// Status returns the last completed listener state.
func (l *listenerLifecycle) Status() ListenerStatus {
	l.statusMu.RLock()
	defer l.statusMu.RUnlock()
	return cloneListenerStatus(l.status)
}

// Start binds and serves the retained endpoint with optional overrides.
func (l *listenerLifecycle) Start(ctx context.Context, settings ListenerSettings) (ListenerStatus, error) {
	return l.mutate(ctx, startListener, settings)
}

// Stop closes the proxy listener and active WebSockets without closing the proxy.
func (l *listenerLifecycle) Stop(ctx context.Context) (ListenerStatus, error) {
	return l.mutate(ctx, stopListener, ListenerSettings{})
}

// Update replaces an active proxy listener after first binding its replacement.
func (l *listenerLifecycle) Update(ctx context.Context, settings ListenerSettings) (ListenerStatus, error) {
	return l.mutate(ctx, updateListener, settings)
}

// Shutdown closes the proxy through its established full-service cleanup path.
func (l *listenerLifecycle) Shutdown() error {
	result := make(chan listenerResult, 1)
	state := &atomic.Uint32{}
	request := listenerRequest{ctx: context.Background(), operation: shutdownListener, result: result, state: state}
	select {
	case l.requests <- request:
		return (<-result).err
	case <-l.done:
		return nil
	}
}

func (l *listenerLifecycle) mutate(ctx context.Context, operation listenerOperation, settings ListenerSettings) (ListenerStatus, error) {
	result := make(chan listenerResult, 1)
	state := &atomic.Uint32{}
	request := listenerRequest{ctx: ctx, operation: operation, settings: settings, result: result, state: state}
	select {
	case l.requests <- request:
	case <-ctx.Done():
		return l.Status(), ctx.Err()
	case <-l.done:
		return l.Status(), errListenerClosed
	}
	select {
	case response := <-result:
		return response.status, response.err
	case <-ctx.Done():
		if state.CompareAndSwap(listenerRequestWaiting, listenerRequestCanceled) {
			return l.Status(), ctx.Err()
		}
		response := <-result
		return response.status, response.err
	case <-l.done:
		return l.Status(), errListenerClosed
	}
}

const (
	listenerRequestWaiting uint32 = iota
	listenerRequestStarted
	listenerRequestCanceled
)

func (l *listenerLifecycle) run() {
	var retained string
	var current *listenerRun
	for {
		select {
		case request := <-l.requests:
			if err := request.ctx.Err(); err != nil || !request.state.CompareAndSwap(listenerRequestWaiting, listenerRequestStarted) {
				if err == nil {
					err = context.Canceled
				}
				request.result <- listenerResult{status: l.Status(), err: err}
				continue
			}
			switch request.operation {
			case startListener:
				status, run, endpoint, err := l.start(current, retained, request.settings)
				if err == nil {
					current, retained = run, endpoint
					l.setStatus(status)
					l.events.publish("listener.started", status)
				}
				request.result <- listenerResult{status: l.Status(), err: err}
			case stopListener:
				if current == nil {
					request.result <- listenerResult{status: l.Status()}
					continue
				}
				err := l.stopRun(current, true)
				unexpected := current.state.Load() == listenerRunUnexpectedEnd
				if unexpected {
					l.logUnexpectedServe(current.serveErr)
				}
				current = nil
				status := ListenerStatus{Status: ListenerInactive}
				l.setStatus(status)
				if unexpected {
					l.publishStopped(status, "failed")
				} else {
					l.publishStopped(status, "requested")
				}
				request.result <- listenerResult{status: l.Status(), err: err}
			case updateListener:
				status, run, endpoint, changed, err := l.update(current, retained, request.settings)
				if changed {
					if current.state.Load() == listenerRunUnexpectedEnd {
						l.logUnexpectedServe(current.serveErr)
						inactive := ListenerStatus{Status: ListenerInactive}
						l.publishStopped(inactive, "failed")
					}
					current, retained = run, endpoint
					l.setStatus(status)
					l.events.publish("listener.updated", status)
				}
				request.result <- listenerResult{status: l.Status(), err: err}
			case shutdownListener:
				err := l.proxy.Close()
				if current != nil {
					<-current.finished
				}
				l.setStatus(ListenerStatus{Status: ListenerInactive})
				request.result <- listenerResult{status: l.Status(), err: err}
				close(l.done)
				return
			}
		case ended := <-l.serveEnded:
			if ended.run != current {
				continue
			}
			current = nil
			l.logUnexpectedServe(ended.err)
			if err := l.proxy.CloseWebSocketsAndFlush(); err != nil {
				fmt.Fprintf(l.logWriter, "cleaning up WebSockets after proxy listener failure: %v\n", err)
			}
			status := ListenerStatus{Status: ListenerInactive}
			l.setStatus(status)
			l.publishStopped(status, "failed")
		}
	}
}

func (l *listenerLifecycle) logUnexpectedServe(err error) {
	if err != nil {
		fmt.Fprintf(l.logWriter, "proxy listener stopped unexpectedly: %v\n", err)
	} else {
		fmt.Fprintln(l.logWriter, "proxy listener stopped unexpectedly")
	}
}

func (l *listenerLifecycle) publishStopped(status ListenerStatus, reason string) {
	l.events.publish("listener.stopped", struct {
		Status        ListenerState `json:"status"`
		ProxyListener *string       `json:"proxy_listener"`
		Reason        string        `json:"reason"`
	}{Status: status.Status, ProxyListener: status.ProxyListener, Reason: reason})
}

func (l *listenerLifecycle) start(current *listenerRun, retained string, settings ListenerSettings) (ListenerStatus, *listenerRun, string, error) {
	if current != nil {
		return ListenerStatus{}, nil, retained, ErrListenerAlreadyActive
	}
	endpoint, err := applyListenerSettings(retained, settings)
	if err != nil {
		return ListenerStatus{}, nil, retained, err
	}
	listener, err := l.bind(endpoint)
	if err != nil {
		return ListenerStatus{}, nil, retained, err
	}
	actual := listener.Addr().String()
	run := l.serve(listener)
	return activeListenerStatus(actual), run, actual, nil
}

func (l *listenerLifecycle) update(current *listenerRun, retained string, settings ListenerSettings) (ListenerStatus, *listenerRun, string, bool, error) {
	if current == nil {
		return ListenerStatus{}, nil, retained, false, ErrListenerInactive
	}
	endpoint, err := applyListenerSettings(retained, settings)
	if err != nil {
		return ListenerStatus{}, current, retained, false, err
	}
	if endpoint == retained {
		return activeListenerStatus(retained), current, retained, false, nil
	}
	replacement, err := l.bind(endpoint)
	if err != nil {
		return ListenerStatus{}, current, retained, false, err
	}
	actual := replacement.Addr().String()
	cleanupErr := l.stopRun(current, true)
	run := l.serve(replacement)
	return activeListenerStatus(actual), run, actual, true, cleanupErr
}

func (l *listenerLifecycle) bind(endpoint string) (net.Listener, error) {
	address, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, listenerFailure{kind: ErrListenerUnavailable, err: err}
	}
	listener, err := l.proxy.GetListener(address, port)
	if err != nil {
		fmt.Fprintf(l.logWriter, "binding proxy listener on %s: %v\n", endpoint, err)
		return nil, listenerFailure{kind: ErrListenerUnavailable, err: err}
	}
	return listener, nil
}

func (l *listenerLifecycle) serve(listener net.Listener) *listenerRun {
	ready := &listenerReadyListener{Listener: listener, started: make(chan struct{})}
	run := &listenerRun{listener: listener, started: ready.started, finished: make(chan struct{})}
	go func() {
		err := l.proxy.Serve(ready)
		run.serveErr = err
		run.state.CompareAndSwap(listenerRunServing, listenerRunUnexpectedEnd)
		close(run.finished)
		l.serveEnded <- listenerServeResult{run: run, err: err}
	}()
	select {
	case <-run.started:
	case <-run.finished:
	}
	return run
}

func (l *listenerLifecycle) stopRun(run *listenerRun, cleanup bool) error {
	run.state.CompareAndSwap(listenerRunServing, listenerRunExpectedClose)
	closeErr := run.listener.Close()
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		fmt.Fprintf(l.logWriter, "closing proxy listener: %v\n", closeErr)
	}
	<-run.finished
	if !cleanup {
		return closeErr
	}
	if err := l.proxy.CloseWebSocketsAndFlush(); err != nil {
		fmt.Fprintf(l.logWriter, "cleaning up WebSockets after proxy listener stop: %v\n", err)
		return listenerFailure{kind: ErrListenerCleanup, err: err}
	}
	return nil
}

func applyListenerSettings(retained string, settings ListenerSettings) (string, error) {
	var address, port string
	if retained != "" {
		var err error
		address, port, err = net.SplitHostPort(retained)
		if err != nil {
			return "", listenerFailure{kind: ErrListenerUnavailable, err: err}
		}
	}
	if settings.Address != nil {
		address = *settings.Address
	}
	if settings.Port != nil {
		port = strconv.FormatUint(uint64(*settings.Port), 10)
	}
	if address == "" || port == "" {
		return "", ErrListenerUnavailable
	}
	return net.JoinHostPort(address, port), nil
}

type listenerFailure struct {
	kind error
	err  error
}

func (e listenerFailure) Error() string   { return e.err.Error() }
func (e listenerFailure) Unwrap() []error { return []error{e.kind, e.err} }

func activeListenerStatus(endpoint string) ListenerStatus {
	return ListenerStatus{Status: ListenerActive, ProxyListener: &endpoint}
}

func cloneListenerStatus(status ListenerStatus) ListenerStatus {
	if status.ProxyListener == nil {
		return status
	}
	endpoint := *status.ProxyListener
	status.ProxyListener = &endpoint
	return status
}

func (l *listenerLifecycle) setStatus(status ListenerStatus) {
	l.statusMu.Lock()
	l.status = cloneListenerStatus(status)
	l.statusMu.Unlock()
}

type listenerReadyListener struct {
	net.Listener
	started chan struct{}
	once    sync.Once
}

func (l *listenerReadyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.started) })
	return l.Listener.Accept()
}
