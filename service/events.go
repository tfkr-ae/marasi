package service

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

const eventQueueSize = 256

// serviceEvent is one Server-Sent Event written to a subscriber.
type serviceEvent struct {
	name string // SSE event name
	data []byte // JSON payload
}

// eventSubscriber is one live /events connection.
type eventSubscriber struct {
	events chan serviceEvent // buffered events for this subscriber
	done   chan struct{}     // closed when the subscriber is removed
}

// eventBroadcaster fans traffic events out to live /events subscribers.
type eventBroadcaster struct {
	mu          sync.Mutex                    // guards subscribers and closed
	subscribers map[*eventSubscriber]struct{} // active event streams
	closed      bool                          // true after close
}

// trafficRequestEvent is the JSON body of a traffic.request SSE event.
type trafficRequestEvent struct {
	ID          uuid.UUID      `json:"id"`           // request UUID
	Scheme      string         `json:"scheme"`       // http or https
	Method      string         `json:"method"`       // HTTP method
	Host        string         `json:"host"`         // request host
	Path        string         `json:"path"`         // path including query
	Metadata    map[string]any `json:"metadata"`     // request metadata without prettified bodies
	RequestedAt time.Time      `json:"requested_at"` // when the request was made
}

// trafficResponseEvent is the JSON body of a traffic.response SSE event.
type trafficResponseEvent struct {
	ID          uuid.UUID      `json:"id"`           // request UUID
	Status      string         `json:"status"`       // HTTP status text
	StatusCode  int            `json:"status_code"`  // HTTP status code
	ContentType string         `json:"content_type"` // response content type
	Length      string         `json:"length"`       // content length
	Metadata    map[string]any `json:"metadata"`     // response metadata without prettified bodies
	RespondedAt time.Time      `json:"responded_at"` // when the response arrived
}

// newEventBroadcaster returns an empty broadcaster.
func newEventBroadcaster() *eventBroadcaster {
	return &eventBroadcaster{subscribers: make(map[*eventSubscriber]struct{})}
}

// subscribe adds a subscriber. If the broadcaster is closed, the returned
// subscriber's channels are already closed.
func (b *eventBroadcaster) subscribe() *eventSubscriber {
	subscriber := &eventSubscriber{
		events: make(chan serviceEvent, eventQueueSize),
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	if b.closed {
		close(subscriber.events)
		close(subscriber.done)
		b.mu.Unlock()
		return subscriber
	}
	b.subscribers[subscriber] = struct{}{}
	b.mu.Unlock()
	return subscriber
}

// close removes every subscriber and rejects later publishes.
func (b *eventBroadcaster) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	for subscriber := range b.subscribers {
		b.removeLocked(subscriber)
	}
	b.mu.Unlock()
}

// unsubscribe removes one subscriber.
func (b *eventBroadcaster) unsubscribe(subscriber *eventSubscriber) {
	b.mu.Lock()
	b.removeLocked(subscriber)
	b.mu.Unlock()
}

// publishRequest publishes a traffic.request event.
func (b *eventBroadcaster) publishRequest(request domain.ProxyRequest) {
	b.publish("traffic.request", trafficRequestEvent{
		ID:          request.ID,
		Scheme:      request.Scheme,
		Method:      request.Method,
		Host:        request.Host,
		Path:        request.Path,
		Metadata:    metadataWithoutPrettified(request.Metadata),
		RequestedAt: request.RequestedAt,
	})
}

// publishResponse publishes a traffic.response event.
func (b *eventBroadcaster) publishResponse(response domain.ProxyResponse) {
	b.publish("traffic.response", trafficResponseEvent{
		ID:          response.ID,
		Status:      response.Status,
		StatusCode:  response.StatusCode,
		ContentType: response.ContentType,
		Length:      response.Length,
		Metadata:    metadataWithoutPrettified(response.Metadata),
		RespondedAt: response.RespondedAt,
	})
}

// publish JSON-encodes payload and sends it to every subscriber.
// A full subscriber queue drops that subscriber.
func (b *eventBroadcaster) publish(name string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := serviceEvent{name: name, data: data}

	b.mu.Lock()
	defer b.mu.Unlock()
	for subscriber := range b.subscribers {
		select {
		case subscriber.events <- event:
		default:
			b.removeLocked(subscriber)
		}
	}
}

// removeLocked removes subscriber. The caller must hold b.mu.
func (b *eventBroadcaster) removeLocked(subscriber *eventSubscriber) {
	if _, ok := b.subscribers[subscriber]; !ok {
		return
	}
	delete(b.subscribers, subscriber)
	close(subscriber.events)
	close(subscriber.done)
}
