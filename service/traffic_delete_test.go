package service

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func TestTrafficBulkDeleteAPI(t *testing.T) {
	t.Run("should delete a query selection up to as_of and publish traffic.changed then sitemap.changed", func(t *testing.T) {
		p := setupBulkProject(t)
		kept := p.insertPair(t, "GET")
		gone := p.insertPair(t, "POST")
		asOf := p.insertPair(t, "POST")
		late := p.insertPair(t, "POST") // after as_of
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		selection := fmt.Sprintf(`{"query":"method = \"POST\"","as_of":%q}`, asOf)
		response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/delete", `{"selection":`+selection+`}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")

		got := eventSummary(drainEvents(subscriber))
		want := []string{
			`traffic.changed {"change":"delete","count":2,"selection":` + selection + `}`,
			`sitemap.changed {"reason":"delete"}`,
		}
		if !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
		for name, id := range map[string]fmt.Stringer{"deleted": gone, "as_of": asOf} {
			response := requestControlAPI(p.server, http.MethodGet, "/traffic/"+id.String(), "")
			if response.Code != http.StatusNotFound {
				t.Fatalf("GET /traffic/{id} of the %s pair = %d, want 404: %s", name, response.Code, response.Body)
			}
		}
		for _, id := range []fmt.Stringer{kept, late} {
			response := requestControlAPI(p.server, http.MethodGet, "/traffic/"+id.String(), "")
			if response.Code != http.StatusOK {
				t.Fatalf("GET /traffic/{id} of a kept pair = %d, want 200: %s", response.Code, response.Body)
			}
		}
	})

	t.Run("should reject a bad body and publish nothing", func(t *testing.T) {
		p := setupBulkProject(t)
		p.insertPair(t, "GET")
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		for _, body := range []string{`{}`, `{"selection":{"ids":[]}}`, `{"selection":{"ids":[]},"extra":1}`} {
			response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/delete", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("POST /traffic/bulk/delete %s = %d, want 400: %s", body, response.Code, response.Body)
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
	})
}
