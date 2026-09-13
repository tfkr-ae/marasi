package service

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

const eventQueueSize = 256

type serviceEvent struct {
	name string
	data []byte
}

type eventSubscriber struct {
	events chan serviceEvent
	done   chan struct{}
}

type eventBroadcaster struct {
	mu          sync.Mutex
	subscribers map[*eventSubscriber]struct{}
}

type trafficRequestEvent struct {
	ID          uuid.UUID      `json:"id"`
	Scheme      string         `json:"scheme"`
	Method      string         `json:"method"`
	Host        string         `json:"host"`
	Path        string         `json:"path"`
	Metadata    map[string]any `json:"metadata"`
	RequestedAt time.Time      `json:"requested_at"`
}

type trafficResponseEvent struct {
	ID          uuid.UUID      `json:"id"`
	Status      string         `json:"status"`
	StatusCode  int            `json:"status_code"`
	ContentType string         `json:"content_type"`
	Length      string         `json:"length"`
	Metadata    map[string]any `json:"metadata"`
	RespondedAt time.Time      `json:"responded_at"`
}

func newEventBroadcaster() *eventBroadcaster {
	return &eventBroadcaster{subscribers: make(map[*eventSubscriber]struct{})}
}

func (b *eventBroadcaster) subscribe() *eventSubscriber {
	subscriber := &eventSubscriber{
		events: make(chan serviceEvent, eventQueueSize),
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	b.subscribers[subscriber] = struct{}{}
	b.mu.Unlock()
	return subscriber
}

func (b *eventBroadcaster) unsubscribe(subscriber *eventSubscriber) {
	b.mu.Lock()
	b.removeLocked(subscriber)
	b.mu.Unlock()
}

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

func (b *eventBroadcaster) removeLocked(subscriber *eventSubscriber) {
	if _, ok := b.subscribers[subscriber]; !ok {
		return
	}
	delete(b.subscribers, subscriber)
	close(subscriber.events)
	close(subscriber.done)
}
