package service

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestTrafficBulkMetadataAPI(t *testing.T) {
	t.Run("should set and clear a key and publish traffic.changed with change metadata", func(t *testing.T) {
		p := setupBulkProject(t)
		first := p.insertPair(t, "GET")
		second := p.insertPair(t, "GET")
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		selection := fmt.Sprintf(`{"ids":[%q,%q]}`, first, second)

		response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/metadata",
			`{"selection":`+selection+`,"key":"triage.state","value":{"done":true}}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")
		for _, id := range []uuid.UUID{first, second} {
			got, err := p.repo.GetMetadata(id)
			if err != nil {
				t.Fatalf("GetMetadata() error = %v", err)
			}
			want := map[string]any{"triage": map[string]any{"state": map[string]any{"done": true}}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
			}
		}

		response = requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/metadata",
			fmt.Sprintf(`{"selection":{"ids":[%q]},"key":"triage.state","value":null}`, first))
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":1}`+"\n")

		response = requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/metadata",
			`{"selection":`+selection+`,"key":"triage.state","clear":true}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")
		for _, id := range []uuid.UUID{first, second} {
			got, err := p.repo.GetMetadata(id)
			if err != nil {
				t.Fatalf("GetMetadata() error = %v", err)
			}
			if want := map[string]any{"triage": map[string]any{}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
			}
		}

		events := drainEvents(subscriber)
		wants := []string{
			`{"change":"metadata","count":2,"selection":` + selection + `}`,
			fmt.Sprintf(`{"change":"metadata","count":1,"selection":{"ids":[%q]}}`, first),
			`{"change":"metadata","count":2,"selection":` + selection + `}`,
		}
		if len(events) != len(wants) {
			t.Fatalf("\nwanted:\n%d events\ngot:\n%v", len(wants), eventSummary(events))
		}
		for i, want := range wants {
			if events[i].name != "traffic.changed" || string(events[i].data) != want {
				t.Errorf("event %d = %s %s, want traffic.changed %s", i, events[i].name, events[i].data, want)
			}
		}
	})

	t.Run("should refuse reserved keys with a 400 that names them", func(t *testing.T) {
		p := setupBulkProject(t)
		id := p.insertPair(t, "GET")
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		for _, key := range []string{"prettified-request", "prettified-response", "has_note"} {
			for _, op := range []string{`"value":1`, `"clear":true`} {
				body := fmt.Sprintf(`{"selection":{"ids":[%q]},"key":%q,%s}`, id, key, op)
				response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/metadata", body)
				want := fmt.Sprintf(`{"error":"invalid_metadata_key","message":"invalid metadata key: %s is written only by Marasi"}`, `\"`+key+`\"`)
				assertControlAPIResponse(t, response, http.StatusBadRequest, want+"\n")
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
	})

	t.Run("should reject invalid bodies and publish nothing", func(t *testing.T) {
		p := setupBulkProject(t)
		id := p.insertPair(t, "GET")
		ids := fmt.Sprintf(`{"ids":[%q]}`, id)
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		for _, body := range []string{
			`{"selection":` + ids + `,"key":"a"}`,
			`{"selection":` + ids + `,"value":1}`,
			`{"selection":` + ids + `,"key":"a","value":1,"clear":true}`,
			`{"selection":` + ids + `,"key":"a","clear":false}`,
			`{"selection":` + ids + `,"key":"a","value":1,"extra":1}`,
			`{"key":"a","value":1}`,
		} {
			response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/metadata", body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"invalid_bulk_request"}`+"\n")
		}
		response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/metadata", `{"selection":`+ids+`,"key":"a..b","value":1}`)
		assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"invalid_metadata_key","message":"invalid metadata key: \"a..b\" has an empty key"}`+"\n")
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
	})
}
