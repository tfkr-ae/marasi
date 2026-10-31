package service

import (
	"time"

	"github.com/google/uuid"
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

// ExtensionEvents is an extension option for NewProjectLifecycle. It gives the
// extension marasi.gui and announces its print lines and marasi.gui calls on
// /events, which is how a client of a service instance draws extension UI.
func (l *listenerLifecycle) ExtensionEvents(runtime *extensions.Runtime) error {
	return extensionEvents(l.events)(runtime)
}

func extensionEvents(events *eventBroadcaster) func(*extensions.Runtime) error {
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
		return extensions.WithGUI(extensions.GUI{
			Type: extensions.ServiceGUIType,
			Render: func(target string, schema any) {
				publish("extension.gui.rendered", extensionGUIRenderedEvent{ID: id, Name: name, Target: target, Schema: schema})
			},
			Update: func(key string, value any) {
				publish("extension.gui.updated", extensionGUIUpdatedEvent{ID: id, Name: name, Key: key, Value: value})
			},
		})(runtime)
	}
}
