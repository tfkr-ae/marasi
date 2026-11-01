package service

import (
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/extensions"
)

// extensionLogEvent is the JSON body of an extension.log SSE event.
type extensionLogEvent struct {
	ID   uuid.UUID `json:"id"`   // extension UUID
	Name string    `json:"name"` // extension name
	Time time.Time `json:"time"` // when the extension printed the line
	Text string    `json:"text"` // the printed line
}

// extensionGUIRenderedEvent is the JSON body of an extension.gui.rendered SSE event.
type extensionGUIRenderedEvent struct {
	ID     uuid.UUID `json:"id"`     // extension UUID
	Name   string    `json:"name"`   // extension name
	Target string    `json:"target"` // "toast", or where the client draws schema
	Schema any       `json:"schema"` // the table passed to marasi.gui:render
}

// extensionGUIUpdatedEvent is the JSON body of an extension.gui.updated SSE event.
type extensionGUIUpdatedEvent struct {
	ID    uuid.UUID `json:"id"`    // extension UUID
	Name  string    `json:"name"`  // extension name
	Key   string    `json:"key"`   // UI state key
	Value any       `json:"value"` // the value passed to marasi.gui:update
}

// extensionUI is the body of GET /extension/{id}/ui: what the extension last
// drew per render target, other than a toast, and its last UI state per key.
type extensionUI struct {
	ID      uuid.UUID      `json:"id"`
	Name    string         `json:"name"`
	Renders map[string]any `json:"renders"`
	State   map[string]any `json:"state"`
}

// ExtensionEvents is an extension option for NewProjectLifecycle. It gives the
// extension marasi.gui and announces its print lines and marasi.gui calls on
// /events, which is how a client of a service instance draws extension UI.
func (l *listenerLifecycle) ExtensionEvents(runtime *extensions.Runtime) error {
	return extensionEvents(l.events, l.proxy.GetScope)(runtime)
}

func extensionEvents(events *eventBroadcaster, liveScope func() (*compass.Scope, error)) func(*extensions.Runtime) error {
	return func(runtime *extensions.Runtime) error {
		// A disabled extension still runs when called, but announces nothing
		// (docs/adr/2026-10-06-extension-enabled-honored.md).
		publish := func(name string, payload any) {
			if runtime.MetadataSnapshot().Enabled {
				events.publish(name, payload)
			}
		}
		// ID and Name never change after the runtime is published.
		id, name := runtime.Data.ID, runtime.Data.Name
		err := extensions.ExtensionWithLogHandler(func(entry extensions.ExtensionLog) error {
			publish("extension.log", extensionLogEvent{ID: id, Name: name, Time: entry.Time, Text: entry.Text})
			return nil
		})(runtime)
		if err != nil {
			return err
		}
		// Scope rules belong to the project, not the extension, so a change
		// is announced whether or not the extension that made it is enabled.
		// Only the live scope is announced: while a project opens, its Compass
		// fills a scope that goes live only if the open succeeds, and clients
		// reread the scope on project.opened.
		err = extensions.ExtensionWithScopeChangeHandler(func(scope *compass.Scope) {
			if live, err := liveScope(); err != nil || live != scope {
				return
			}
			events.publish("scope.updated", scopeListFromScope(scope))
		})(runtime)
		if err != nil {
			return err
		}
		// What an enabled extension draws is kept for GET /extension/{id}/ui,
		// so a client that joins later can draw it.
		return extensions.WithGUI(extensions.GUI{
			Type: extensions.ServiceGUIType,
			Render: func(target string, schema any) {
				if !runtime.MetadataSnapshot().Enabled {
					return
				}
				runtime.KeepRender(target, schema)
				events.publish("extension.gui.rendered", extensionGUIRenderedEvent{ID: id, Name: name, Target: target, Schema: schema})
			},
			Update: func(key string, value any) {
				if !runtime.MetadataSnapshot().Enabled {
					return
				}
				runtime.KeepUpdate(key, value)
				events.publish("extension.gui.updated", extensionGUIUpdatedEvent{ID: id, Name: name, Key: key, Value: value})
			},
		})(runtime)
	}
}
