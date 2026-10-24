package service

import (
	"context"
	"net"
	"net/http"
	"sync"
)

// connTracker keeps the open connections of one http.Server, so they can be
// closed on demand. Closing a connection ends the requests on it, including
// event streams, because their contexts end with it. Install track as the
// server's ConnState.
type connTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newConnTracker() *connTracker {
	return &connTracker{conns: make(map[net.Conn]struct{})}
}

// track records conn while it is open. It is an http.Server ConnState hook.
func (t *connTracker) track(conn net.Conn, state http.ConnState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch state {
	case http.StateNew:
		t.conns[conn] = struct{}{}
	case http.StateClosed, http.StateHijacked:
		delete(t.conns, conn)
	}
}

// closeWhere closes every open connection for which match returns true.
func (t *connTracker) closeWhere(match func(net.Conn) bool) {
	t.mu.Lock()
	var matched []net.Conn
	for conn := range t.conns {
		if match(conn) {
			matched = append(matched, conn)
		}
	}
	t.mu.Unlock()
	for _, conn := range matched {
		_ = conn.Close()
	}
}

// contains reports whether conn is one of the open connections.
func (t *connTracker) contains(conn net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.conns[conn]
	return ok
}

type connKey struct{}

// contextConn returns the connection a request with context ctx arrived on,
// when its server stored it under connKey in ConnContext, or nil.
func contextConn(ctx context.Context) net.Conn {
	conn, _ := ctx.Value(connKey{}).(net.Conn)
	return conn
}
