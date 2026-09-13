package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestEventBroadcaster(t *testing.T) {
	t.Run("should publish exact request and response events", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		subscriber := broadcaster.subscribe()
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")

		broadcaster.publishRequest(domain.ProxyRequest{
			ID:          id,
			Scheme:      "https",
			Method:      "GET",
			Host:        "example.com",
			Path:        "/a?b=c",
			Raw:         []byte("raw request"),
			Metadata:    map[string]any{"foo": "bar", "prettified-request": "pretty request", "prettified-response": "pretty response"},
			RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		})
		broadcaster.publishResponse(domain.ProxyResponse{
			ID:          id,
			Status:      "200 OK",
			StatusCode:  200,
			ContentType: "application/json",
			Length:      "12",
			Raw:         []byte("raw response"),
			Metadata:    map[string]any{"foo": "bar", "prettified-request": "pretty request", "prettified-response": "pretty response"},
			RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
		})

		requestEvent := <-subscriber.events
		if requestEvent.name != "traffic.request" {
			t.Fatalf("\nwanted:\ntraffic.request\ngot:\n%s", requestEvent.name)
		}
		wantRequest := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","scheme":"https","method":"GET","host":"example.com","path":"/a?b=c","metadata":{"foo":"bar"},"requested_at":"2026-01-02T03:04:05Z"}`
		if got := string(requestEvent.data); got != wantRequest {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantRequest, got)
		}

		responseEvent := <-subscriber.events
		if responseEvent.name != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.response\ngot:\n%s", responseEvent.name)
		}
		wantResponse := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","status":"200 OK","status_code":200,"content_type":"application/json","length":"12","metadata":{"foo":"bar"},"responded_at":"2026-01-02T03:04:06Z"}`
		if got := string(responseEvent.data); got != wantResponse {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantResponse, got)
		}
	})

	t.Run("should send one encoding to multiple subscribers in publication order", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		first := broadcaster.subscribe()
		second := broadcaster.subscribe()

		broadcaster.publishRequest(domain.ProxyRequest{Path: "/first"})
		broadcaster.publishResponse(domain.ProxyResponse{Status: "second"})

		firstRequest := <-first.events
		firstResponse := <-first.events
		secondRequest := <-second.events
		secondResponse := <-second.events
		if firstRequest.name != "traffic.request" || firstResponse.name != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.request then traffic.response\ngot:\n%s then %s", firstRequest.name, firstResponse.name)
		}
		if secondRequest.name != "traffic.request" || secondResponse.name != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.request then traffic.response\ngot:\n%s then %s", secondRequest.name, secondResponse.name)
		}
		if &firstRequest.data[0] != &secondRequest.data[0] {
			t.Fatal("\nwanted:\nshared encoded request bytes\ngot:\ndifferent byte slices")
		}
		if &firstResponse.data[0] != &secondResponse.data[0] {
			t.Fatal("\nwanted:\nshared encoded response bytes\ngot:\ndifferent byte slices")
		}
	})

	t.Run("should remove an unsubscribed subscriber", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		subscriber := broadcaster.subscribe()

		broadcaster.unsubscribe(subscriber)
		broadcaster.unsubscribe(subscriber)
		broadcaster.publishRequest(domain.ProxyRequest{})

		select {
		case <-subscriber.done:
		default:
			t.Fatal("\nwanted:\nsubscriber closed\ngot:\nsubscriber open")
		}
		if got := len(broadcaster.subscribers); got != 0 {
			t.Fatalf("\nwanted:\n0 subscribers\ngot:\n%d", got)
		}
		if got := len(subscriber.events); got != 0 {
			t.Fatalf("\nwanted:\n0 queued events\ngot:\n%d", got)
		}
		if _, open := <-subscriber.events; open {
			t.Fatal("\nwanted:\nsubscriber queue closed\ngot:\nsubscriber queue open")
		}
	})

	t.Run("should disconnect only a subscriber whose queue overflows", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		slow := broadcaster.subscribe()
		healthy := broadcaster.subscribe()

		for index := range eventQueueSize + 1 {
			broadcaster.publishRequest(domain.ProxyRequest{Path: fmt.Sprintf("/%d", index)})
			select {
			case <-healthy.events:
			default:
				t.Fatalf("\nwanted:\nhealthy subscriber event %d\ngot:\nno event", index)
			}
		}

		select {
		case <-slow.done:
		default:
			t.Fatal("\nwanted:\noverflowed subscriber closed\ngot:\nsubscriber open")
		}
		select {
		case <-healthy.done:
			t.Fatal("\nwanted:\nhealthy subscriber open\ngot:\nsubscriber closed")
		default:
		}
		if got := len(broadcaster.subscribers); got != 1 {
			t.Fatalf("\nwanted:\n1 subscriber\ngot:\n%d", got)
		}

		broadcaster.publishResponse(domain.ProxyResponse{Status: "still connected"})
		if got := (<-healthy.events).name; got != "traffic.response" {
			t.Fatalf("\nwanted:\ntraffic.response\ngot:\n%s", got)
		}
	})

	t.Run("should support concurrent publication subscription and cancellation", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		start := make(chan struct{})
		finished := make(chan struct{}, 3)

		go func() {
			<-start
			for range 100 {
				broadcaster.publishRequest(domain.ProxyRequest{})
			}
			finished <- struct{}{}
		}()
		for range 2 {
			go func() {
				<-start
				for range 100 {
					subscriber := broadcaster.subscribe()
					broadcaster.unsubscribe(subscriber)
				}
				finished <- struct{}{}
			}()
		}

		close(start)
		for range 3 {
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("concurrent broadcaster operation timed out")
			}
		}
	})
}
