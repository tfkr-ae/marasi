package websocket

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func registryTestUUID(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating UUID: %v", err)
	}

	return id
}

func TestRegistry_NewRegistry(t *testing.T) {
	registry := NewRegistry()

	if registry == nil {
		t.Fatalf("\nwanted registry:\n%v\ngot:\n%v", "initialized", registry)
	}
	if registry.connections == nil {
		t.Fatalf("\nwanted connections map:\n%v\ngot:\n%v", "initialized", registry.connections)
	}
	if registry.requestConnections == nil {
		t.Fatalf("\nwanted request connections map:\n%v\ngot:\n%v", "initialized", registry.requestConnections)
	}
	if got := len(registry.connections); got != 0 {
		t.Fatalf("\nwanted connection count:\n%d\ngot:\n%d", 0, got)
	}
}

func TestRegistry_AddAndGet(t *testing.T) {
	registry := NewRegistry()
	connection := &Connection{ID: registryTestUUID(t)}

	if err := registry.Add(connection); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}

	got, exists := registry.Get(connection.ID)
	if !exists {
		t.Fatalf("\nwanted connection exists:\n%v\ngot:\n%v", true, exists)
	}
	if got != connection {
		t.Fatalf("\nwanted connection:\n%p\ngot:\n%p", connection, got)
	}
}

func TestRegistry_GetMissingConnection(t *testing.T) {
	registry := NewRegistry()

	got, exists := registry.Get(registryTestUUID(t))
	if exists {
		t.Fatalf("\nwanted connection exists:\n%v\ngot:\n%v", false, exists)
	}
	if got != nil {
		t.Fatalf("\nwanted connection:\n%v\ngot:\n%p", nil, got)
	}
}

func TestRegistry_GetByRequestID(t *testing.T) {
	registry := NewRegistry()
	connection := &Connection{
		ID:        registryTestUUID(t),
		RequestID: registryTestUUID(t),
	}

	if err := registry.Add(connection); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}

	got, exists := registry.GetByRequestID(connection.RequestID)
	if !exists {
		t.Fatalf("\nwanted connection exists:\n%v\ngot:\n%v", true, exists)
	}
	if got != connection {
		t.Fatalf("\nwanted connection:\n%p\ngot:\n%p", connection, got)
	}

	registry.Remove(connection.ID)
	got, exists = registry.GetByRequestID(connection.RequestID)
	if exists {
		t.Fatalf("\nwanted connection exists after removal:\n%v\ngot:\n%v", false, exists)
	}
	if got != nil {
		t.Fatalf("\nwanted connection:\n%v\ngot:\n%p", nil, got)
	}
}

func TestRegistry_AddNilConnection(t *testing.T) {
	registry := NewRegistry()

	err := registry.Add(nil)
	if err == nil {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "websocket connection is nil", err)
	}
	if !strings.Contains(err.Error(), "websocket connection is nil") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "websocket connection is nil", err)
	}
	if got := len(registry.connections); got != 0 {
		t.Fatalf("\nwanted connection count:\n%d\ngot:\n%d", 0, got)
	}
}

func TestRegistry_AddDuplicateConnection(t *testing.T) {
	registry := NewRegistry()
	id := registryTestUUID(t)
	first := &Connection{ID: id}
	second := &Connection{ID: id}

	if err := registry.Add(first); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}
	err := registry.Add(second)
	if err == nil {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "websocket connection already registered", err)
	}
	if !strings.Contains(err.Error(), "websocket connection already registered") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "websocket connection already registered", err)
	}

	got, exists := registry.Get(id)
	if !exists {
		t.Fatalf("\nwanted connection exists:\n%v\ngot:\n%v", true, exists)
	}
	if got != first {
		t.Fatalf("\nwanted connection:\n%p\ngot:\n%p", first, got)
	}
}

func TestRegistry_AddDuplicateRequest(t *testing.T) {
	registry := NewRegistry()
	requestID := registryTestUUID(t)
	first := &Connection{ID: registryTestUUID(t), RequestID: requestID}
	second := &Connection{ID: registryTestUUID(t), RequestID: requestID}

	if err := registry.Add(first); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}
	err := registry.Add(second)
	if err == nil {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "websocket request already registered", err)
	}
	if !strings.Contains(err.Error(), "websocket request already registered") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "websocket request already registered", err)
	}

	got, exists := registry.GetByRequestID(requestID)
	if !exists {
		t.Fatalf("\nwanted connection exists:\n%v\ngot:\n%v", true, exists)
	}
	if got != first {
		t.Fatalf("\nwanted connection:\n%p\ngot:\n%p", first, got)
	}
}

func TestRegistry_Remove(t *testing.T) {
	registry := NewRegistry()
	connection := &Connection{ID: registryTestUUID(t)}
	if err := registry.Add(connection); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}

	registry.Remove(registryTestUUID(t))
	got, exists := registry.Get(connection.ID)
	if !exists {
		t.Fatalf("\nwanted connection exists after removing missing ID:\n%v\ngot:\n%v", true, exists)
	}
	if got != connection {
		t.Fatalf("\nwanted connection:\n%p\ngot:\n%p", connection, got)
	}

	registry.Remove(connection.ID)
	got, exists = registry.Get(connection.ID)
	if exists {
		t.Fatalf("\nwanted connection exists after removal:\n%v\ngot:\n%v", false, exists)
	}
	if got != nil {
		t.Fatalf("\nwanted connection:\n%v\ngot:\n%p", nil, got)
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	const connectionCount = 100

	registry := NewRegistry()
	connections := make([]*Connection, connectionCount)
	for i := range connections {
		connections[i] = &Connection{ID: registryTestUUID(t)}
	}

	errorsChannel := make(chan error, connectionCount)
	var waitGroup sync.WaitGroup
	for _, connection := range connections {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := registry.Add(connection); err != nil {
				errorsChannel <- err
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}

	for _, connection := range connections {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			got, exists := registry.Get(connection.ID)
			if !exists {
				t.Errorf("\nwanted connection exists:\n%v\ngot:\n%v", true, exists)
				return
			}
			if got != connection {
				t.Errorf("\nwanted connection:\n%p\ngot:\n%p", connection, got)
			}
		}()
	}
	waitGroup.Wait()

	for _, connection := range connections {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			registry.Remove(connection.ID)
		}()
	}
	waitGroup.Wait()

	if got := len(registry.connections); got != 0 {
		t.Fatalf("\nwanted connection count:\n%d\ngot:\n%d", 0, got)
	}
}

func TestRegistry_CloseAll(t *testing.T) {
	registry := NewRegistry()
	client := &testReadWriteCloser{}
	upstream := &testReadWriteCloser{}
	connection := NewConnection(
		registryTestUUID(t),
		registryTestUUID(t),
		client,
		client,
		upstream,
		nil,
	)

	if err := registry.Add(connection); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}

	if err := registry.CloseAll(CloseGoingAway, "shutting down"); err != nil {
		t.Fatalf("CloseAll() returned unexpected error: %v", err)
	}

	if client.closeCount != 1 {
		t.Fatalf("wanted client close count:\n%d\ngot:\n%d", 1, client.closeCount)
	}
	if upstream.closeCount != 1 {
		t.Fatalf("wanted upstream close count:\n%d\ngot:\n%d", 1, upstream.closeCount)
	}
	if got := len(registry.connections); got != 0 {
		t.Fatalf("wanted connection count:\n%d\ngot:\n%d", 0, got)
	}
	if got := len(registry.requestConnections); got != 0 {
		t.Fatalf("wanted request connection count:\n%d\ngot:\n%d", 0, got)
	}

	frame, err := ReadFrame(bytes.NewReader(client.Bytes()))
	if err != nil {
		t.Fatalf("reading close frame: %v", err)
	}
	if frame.Opcode != OpClose {
		t.Fatalf("wanted opcode:\n%d\ngot:\n%d", OpClose, frame.Opcode)
	}
	code, reason, err := ParseClosePayload(frame.Payload)
	if err != nil {
		t.Fatalf("parsing close payload: %v", err)
	}
	if code != CloseGoingAway {
		t.Fatalf("wanted close code:\n%d\ngot:\n%d", CloseGoingAway, code)
	}
	if reason != "shutting down" {
		t.Fatalf("wanted close reason:\n%q\ngot:\n%q", "shutting down", reason)
	}
}
