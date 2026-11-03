package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// controlRetireTimeout bounds how long a closed network address waits for the
// request that closed it to write its response before cutting it off.
const controlRetireTimeout = 5 * time.Second

// errControlAddressHandedOver refuses a move or stop while a restart has
// handed the network address to a successor: the successor or a rollback
// decides where it ends up.
var errControlAddressHandedOver = errors.New("control listener network address is handed over to a restart")

// errControlAddressClosed refuses a start once the instance is stopping.
var errControlAddressClosed = errors.New("the instance is stopping")

// Update moves the network address to settings, and replaces the control
// password with password: "" removes it, and nil keeps it. A nil settings
// keeps the address. The new address is bound before the old one closes, so
// a failed bind leaves the old one serving and the password unchanged.
// Moving to the address already bound and setting the password already held
// change nothing.
func (c *ControlAddress) Update(ctx context.Context, settings *ListenerSettings, password *string) (ControlListenerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return ControlListenerStatus{}, ErrListenerInactive
	}
	if c.run.suspended {
		return ControlListenerStatus{}, errControlAddressHandedOver
	}
	moved := false
	if settings != nil {
		endpoint, err := listenerEndpoint(*settings)
		if err != nil {
			return ControlListenerStatus{}, err
		}
		if endpoint != c.run.listener.Addr().String() {
			run, err := c.bind(ctx, endpoint)
			if err != nil {
				return ControlListenerStatus{}, err
			}
			c.retire(ctx, c.run)
			c.run = run
			moved = true
		}
	}
	changed := password != nil && c.setPassword(ctx, *password)
	status := c.activeStatus(c.run)
	if !moved && !changed {
		return status, nil
	}
	c.setStatus(status)
	c.events.publish("control_listener.updated", status)
	return status, nil
}

// Stop closes the network address and keeps no endpoint, so the next Start
// names its address. The socket and the control password are not affected.
func (c *ControlAddress) Stop(ctx context.Context) (ControlListenerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return ControlListenerStatus{}, ErrListenerInactive
	}
	if c.run.suspended {
		return ControlListenerStatus{}, errControlAddressHandedOver
	}
	c.retire(ctx, c.run)
	c.run = nil
	c.setStatus(ControlListenerStatus{Status: ListenerInactive})
	status := c.Status()
	c.events.publish("control_listener.stopped", status)
	return status, nil
}

// retire closes run for a Stop or Update made by the request with context
// ctx. The listener and every other connection close now, which ends their
// event streams. A request that arrived on run itself keeps its connection
// until its response is written; the server then closes it.
func (c *ControlAddress) retire(ctx context.Context, run *controlAddressRun) {
	if err := run.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		fmt.Fprintf(c.logWriter, "closing control listener network address: %v\n", err)
	}
	trigger, _ := ctx.Value(networkConnKey{}).(*networkConn)
	c.connsMu.Lock()
	for _, tracked := range c.conns {
		if tracked.run == run && tracked != trigger {
			_ = tracked.conn.Close()
		}
	}
	c.connsMu.Unlock()
	c.retiring.Add(1)
	go func() {
		defer c.retiring.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), controlRetireTimeout)
		defer cancel()
		if err := run.server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, net.ErrClosed) {
			_ = run.server.Close()
		}
		<-run.served
	}()
}

// closesConnection reports whether the network address that r arrived on is
// no longer the active one, or r authenticated with a password that is no
// longer the current one, so r's connection must close after its response.
func (c *ControlAddress) closesConnection(r *http.Request) bool {
	tracked, ok := r.Context().Value(networkConnKey{}).(*networkConn)
	if !ok {
		return false
	}
	c.mu.Lock()
	retired := tracked.run != c.run
	c.mu.Unlock()
	c.connsMu.Lock()
	defer c.connsMu.Unlock()
	return retired || tracked.generation != c.password.currentGeneration()
}

// activeStatus is the status of the network address serving run.
func (c *ControlAddress) activeStatus(run *controlAddressRun) ControlListenerStatus {
	address := run.listener.Addr().String()
	return c.withPassword(ControlListenerStatus{Status: ListenerActive, ControlListener: &address})
}

func (s *Server) serveControlListenerStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, s.control.Status())
}

func (s *Server) serveControlListenerStart(w http.ResponseWriter, r *http.Request) {
	settings, password, err := decodeControlListenerRequest(r, true)
	if err != nil {
		writeControlListenerError(w, r, err)
		return
	}
	status, err := s.control.Start(r.Context(), *settings, password)
	s.writeControlListenerResult(w, r, status, err)
}

func (s *Server) serveControlListenerUpdate(w http.ResponseWriter, r *http.Request) {
	settings, password, err := decodeControlListenerRequest(r, false)
	if err != nil {
		writeControlListenerError(w, r, err)
		return
	}
	status, err := s.control.Update(r.Context(), settings, password)
	s.writeControlListenerResult(w, r, status, err)
}

// decodeControlListenerRequest decodes the body of a start or update of the
// network address: address and port, plus password to set the control
// password or no_password true to remove it. With neither, the password is
// kept, and the returned password is nil. An update may leave out address
// and port to keep the address, returning nil settings, but must change
// something.
func decodeControlListenerRequest(r *http.Request, addressRequired bool) (*ListenerSettings, *string, error) {
	fields, err := decodeListenerFields(r)
	if err != nil {
		return nil, nil, err
	}
	var password *string
	if raw, ok := fields["password"]; ok {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || value == "" {
			return nil, nil, errInvalidListenerRequest
		}
		if err := CheckControlPassword(value); err != nil {
			return nil, nil, err
		}
		password = &value
		delete(fields, "password")
	}
	if raw, ok := fields["no_password"]; ok {
		var remove bool
		if err := json.Unmarshal(raw, &remove); err != nil || !remove || password != nil {
			return nil, nil, errInvalidListenerRequest
		}
		password = new(string)
		delete(fields, "no_password")
	}
	if len(fields) == 0 && !addressRequired {
		if password == nil {
			return nil, nil, errInvalidListenerRequest
		}
		return nil, password, nil
	}
	settings, err := listenerSettingsFrom(fields)
	if err != nil {
		return nil, nil, err
	}
	return &settings, password, nil
}

func (s *Server) serveControlListenerStop(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			writeControlListenerError(w, r, errInvalidListenerRequest)
			return
		}
	}
	status, err := s.control.Stop(r.Context())
	s.writeControlListenerResult(w, r, status, err)
}

// writeControlListenerResult writes the outcome of a lifecycle change. A
// request that arrived on an address the change closed is told so with
// Connection: close, and the server closes its connection after this
// response.
func (s *Server) writeControlListenerResult(w http.ResponseWriter, r *http.Request, status ControlListenerStatus, err error) {
	if err != nil {
		writeControlListenerError(w, r, err)
		return
	}
	if s.control.closesConnection(r) {
		w.Header().Set("Connection", "close")
	}
	writeJSON(w, r, http.StatusOK, status)
}

// writeControlListenerError writes a control listener lifecycle failure
// with the error codes the proxy listener routes use, plus
// restart_in_progress. A failed bind also carries the reason, such as an
// address already in use.
func writeControlListenerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrInvalidControlPassword) {
		writeJSON(w, r, http.StatusBadRequest, controlRefusalBody{Error: "invalid_password", Message: err.Error()})
		return
	}
	if errors.Is(err, errControlAddressHandedOver) {
		writeJSON(w, r, http.StatusConflict, controlRefusalBody{Error: "restart_in_progress", Message: "the instance is restarting"})
		return
	}
	if errors.Is(err, ErrListenerUnavailable) {
		writeJSON(w, r, http.StatusConflict, controlRefusalBody{Error: "listener_unavailable", Message: err.Error()})
		return
	}
	writeListenerError(w, r, err)
}
