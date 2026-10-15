package service

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestTrafficBulkNoteAPI(t *testing.T) {
	t.Run("should append to and clear notes and publish traffic.changed with change note", func(t *testing.T) {
		p := setupBulkProject(t)
		noted := p.insertPair(t, "GET")
		bare := p.insertPair(t, "GET")
		if err := p.repo.UpdateNote(noted, "seen"); err != nil {
			t.Fatalf("UpdateNote() error = %v", err)
		}
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		selection := fmt.Sprintf(`{"ids":[%q,%q]}`, noted, bare)

		response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/note", `{"selection":`+selection+`,"append":"triaged"}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")
		for id, want := range map[uuid.UUID]string{noted: "seen\n\ntriaged", bare: "triaged"} {
			if got, err := p.repo.GetNote(id); err != nil || got != want {
				t.Errorf("GetNote(%s) = %q, %v, want %q", id, got, err, want)
			}
		}

		response = requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/note", `{"selection":`+selection+`,"clear":true}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")
		response = requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/note", `{"selection":`+selection+`,"clear":true}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":0}`+"\n")

		events := drainEvents(subscriber)
		want := []string{
			`{"change":"note","count":2,"selection":` + selection + `}`,
			`{"change":"note","count":2,"selection":` + selection + `}`,
			`{"change":"note","count":0,"selection":` + selection + `}`,
		}
		if len(events) != len(want) {
			t.Fatalf("\nwanted:\n%d events\ngot:\n%v", len(want), eventSummary(events))
		}
		for i := range want {
			if events[i].name != "traffic.changed" || string(events[i].data) != want[i] {
				t.Errorf("event %d = %s %s, want traffic.changed %s", i, events[i].name, events[i].data, want[i])
			}
		}
	})

	t.Run("should reject a body without exactly one non-empty operation and publish nothing", func(t *testing.T) {
		p := setupBulkProject(t)
		id := p.insertPair(t, "GET")
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		selection := fmt.Sprintf(`{"ids":[%q]}`, id)

		for _, fields := range []string{
			``,
			`,"append":""`,
			`,"clear":false`,
			`,"append":"x","clear":true`,
		} {
			response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/note", `{"selection":`+selection+fields+`}`)
			assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"invalid_bulk_request"}`+"\n")
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
	})
}
