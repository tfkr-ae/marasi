package service

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// eventQueueSize is a subscriber's queue length. The last slot is reserved
// for stream.dropped, so a subscriber counts as full at eventQueueSize-1.
const eventQueueSize = 1024

// The replay buffer keeps the newest published events up to both limits
// (docs/adr/2026-11-07-the-event-stream-resumes-from-a-shared-replay-buffer.md).
const (
	eventReplayEvents = 4096     // most events kept
	eventReplayBytes  = 16 << 20 // most payload bytes kept
)

// Reasons a resuming subscriber gets stream.reset instead of a replay.
const (
	resetExpired   = "expired"   // the id is older than the replay buffer
	resetRestarted = "restarted" // the id is from another run of the instance
	resetInvalid   = "invalid"   // the id cannot be parsed or was never issued
)

// serviceEvent is one Server-Sent Event written to a subscriber.
type serviceEvent struct {
	name string // SSE event name
	data []byte // JSON payload
	seq  uint64 // position in this run, written as the id; 0 for no id
}

// droppedEvent is the final event of a subscriber whose queue overflowed.
var droppedEvent = serviceEvent{name: "stream.dropped", data: []byte(`{"reason":"overflow"}`)}

// eventSubscriber is one live /events connection.
type eventSubscriber struct {
	events chan serviceEvent // buffered events for this subscriber
	done   chan struct{}     // closed when the subscriber is removed
}

// eventBroadcaster sends service events to live /events subscribers.
type eventBroadcaster struct {
	mu          sync.Mutex                    // guards every field below
	subscribers map[*eventSubscriber]struct{} // active event streams
	closed      bool                          // true after close
	runID       string                        // first part of every event id
	seq         uint64                        // seq of the newest published event
	replay      eventReplay                   // newest published events, shared by all subscribers
}

// eventReplay is a ring of the newest published events. Their seqs are
// consecutive and end at the broadcaster's seq.
type eventReplay struct {
	events [eventReplayEvents]serviceEvent // ring storage
	head   int                             // index of the oldest event
	count  int                             // events held
	bytes  int                             // payload bytes held
}

// add appends event, evicting the oldest events past either limit. The
// newest event is always kept, even when it alone passes the byte limit.
func (r *eventReplay) add(event serviceEvent) {
	if r.count == eventReplayEvents {
		r.evict()
	}
	r.events[(r.head+r.count)%eventReplayEvents] = event
	r.count++
	r.bytes += len(event.data)
	for r.bytes > eventReplayBytes && r.count > 1 {
		r.evict()
	}
}

// evict drops the oldest event.
func (r *eventReplay) evict() {
	r.bytes -= len(r.events[r.head].data)
	r.events[r.head] = serviceEvent{}
	r.head = (r.head + 1) % eventReplayEvents
	r.count--
}

// after copies the newest n events, oldest first.
func (r *eventReplay) after(n int) []serviceEvent {
	if n == 0 {
		return nil
	}
	events := make([]serviceEvent, n)
	start := r.head + r.count - n
	for index := range events {
		events[index] = r.events[(start+index)%eventReplayEvents]
	}
	return events
}

// eventID formats the id of the event at seq in run runID.
func eventID(runID string, seq uint64) string {
	return runID + "." + strconv.FormatUint(seq, 10)
}

// eventResume is where a new subscriber starts.
type eventResume struct {
	replay   []serviceEvent // missed events to write before live ones
	reset    string         // stream.reset reason, or "" for none
	resetSeq uint64         // seq the stream.reset id names
}

// trafficRequestEvent is the JSON body of a traffic.request SSE event.
type trafficRequestEvent struct {
	ID          uuid.UUID      `json:"id"`           // request UUID
	Number      int64          `json:"number"`       // pair number in capture order (docs/adr/2026-10-16-pairs-are-numbered-in-capture-order.md)
	Scheme      string         `json:"scheme"`       // http or https
	Method      string         `json:"method"`       // HTTP method
	Host        string         `json:"host"`         // request host
	Path        string         `json:"path"`         // path including query
	Metadata    map[string]any `json:"metadata"`     // request metadata without prettified bodies
	RequestedAt time.Time      `json:"requested_at"` // when the request was made
	SitemapNode string         `json:"sitemap_node"` // name of the pair's leaf node in the sitemap
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
	// UpstreamProxy is the route that carried the pair, as pair reads show it.
	UpstreamProxy *trafficRoute `json:"upstream_proxy"`
}

// newEventBroadcaster returns an empty broadcaster.
func newEventBroadcaster() *eventBroadcaster {
	return &eventBroadcaster{subscribers: make(map[*eventSubscriber]struct{}), runID: processRunID}
}

// subscribe adds a subscriber that receives events from now on. If the
// broadcaster is closed, the returned subscriber's channels are already closed.
func (b *eventBroadcaster) subscribe() *eventSubscriber {
	subscriber, _ := b.resume("")
	return subscriber
}

// resume adds a subscriber positioned after lastEventID, which is empty for a
// fresh subscribe. It reads the replay buffer and registers the subscriber
// under one lock, so no event is missed or repeated between the replay and
// the live queue. An id it cannot resume from gives a reset and no replay.
func (b *eventBroadcaster) resume(lastEventID string) (*eventSubscriber, eventResume) {
	subscriber := &eventSubscriber{
		events: make(chan serviceEvent, eventQueueSize),
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(subscriber.events)
		close(subscriber.done)
		return subscriber, eventResume{}
	}
	b.subscribers[subscriber] = struct{}{}
	if lastEventID == "" {
		return subscriber, eventResume{}
	}
	missed, reason := b.missedLocked(lastEventID)
	if reason != "" {
		return subscriber, eventResume{reset: reason, resetSeq: b.seq}
	}
	return subscriber, eventResume{replay: b.replay.after(missed)}
}

// missedLocked returns how many buffered events follow lastEventID, or why
// the stream cannot resume from it. The caller must hold b.mu.
func (b *eventBroadcaster) missedLocked(lastEventID string) (int, string) {
	dot := strings.LastIndexByte(lastEventID, '.')
	if dot <= 0 {
		return 0, resetInvalid
	}
	seq, err := strconv.ParseUint(lastEventID[dot+1:], 10, 64)
	if err != nil {
		return 0, resetInvalid
	}
	if lastEventID[:dot] != b.runID {
		return 0, resetRestarted
	}
	if seq > b.seq {
		return 0, resetInvalid
	}
	missed := b.seq - seq
	if missed > uint64(b.replay.count) {
		return 0, resetExpired
	}
	return int(missed), ""
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
	sitemapNode := request.SitemapNode
	if sitemapNode == "" {
		sitemapNode = domain.SitemapLeaf(request.Scheme, request.Host, request.Path)
	}
	b.publish("traffic.request", trafficRequestEvent{
		SitemapNode: sitemapNode,
		ID:          request.ID,
		Number:      request.Number,
		Scheme:      request.Scheme,
		Method:      request.Method,
		Host:        request.Host,
		Path:        request.Path,
		Metadata:    metadataWithoutPrettified(request.Metadata),
		RequestedAt: request.RequestedAt,
	})
}

// publishSitemapChanged publishes a sitemap.changed event, after which
// clients re-read the sitemap nodes they have expanded. reason is "rebuild"
// or "delete".
func (b *eventBroadcaster) publishSitemapChanged(reason string) {
	b.publish("sitemap.changed", struct {
		Reason string `json:"reason"`
	}{Reason: reason})
}

// publishResponse publishes a traffic.response event.
func (b *eventBroadcaster) publishResponse(response domain.ProxyResponse) {
	b.publish("traffic.response", trafficResponseEvent{
		ID:            response.ID,
		Status:        response.Status,
		StatusCode:    response.StatusCode,
		ContentType:   response.ContentType,
		Length:        response.Length,
		Metadata:      metadataWithoutPrettified(response.Metadata),
		RespondedAt:   response.RespondedAt,
		UpstreamProxy: trafficRouteFromDomain(response.Route),
	})
}

// publish JSON-encodes payload, gives it the next seq, keeps it for replay
// and sends it to every subscriber. A subscriber whose queue is full gets
// stream.dropped in its reserved last slot and is removed.
func (b *eventBroadcaster) publish(name string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.seq++
	event := serviceEvent{name: name, data: data, seq: b.seq}
	b.replay.add(event)
	for subscriber := range b.subscribers {
		// Only publish sends, under b.mu, so the length cannot grow here.
		if len(subscriber.events) >= eventQueueSize-1 {
			subscriber.events <- droppedEvent
			b.removeLocked(subscriber)
			continue
		}
		subscriber.events <- event
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
