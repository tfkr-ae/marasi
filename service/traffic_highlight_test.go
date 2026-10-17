package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// listedHighlight returns the highlight GET /traffic and GET /traffic/{id}
// report for id, as JSON.
func listedHighlight(t *testing.T, p *bulkProject, id uuid.UUID) (string, string) {
	t.Helper()
	response := requestControlAPI(p.server, http.MethodGet, "/traffic?limit=500", "")
	var list struct {
		Items []struct {
			ID        uuid.UUID       `json:"id"`
			Highlight json.RawMessage `json:"highlight"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding GET /traffic: %v: %s", err, response.Body)
	}
	listed := "missing"
	for _, item := range list.Items {
		if item.ID == id {
			listed = string(item.Highlight)
		}
	}
	response = requestControlAPI(p.server, http.MethodGet, "/traffic/"+id.String(), "")
	var detail struct {
		Highlight json.RawMessage `json:"highlight"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decoding GET /traffic/{id}: %v: %s", err, response.Body)
	}
	return listed, string(detail.Highlight)
}

func TestTrafficBulkHighlightAPI(t *testing.T) {
	t.Run("should set and clear highlights and publish one traffic.changed event each", func(t *testing.T) {
		p := setupBulkProject(t)
		a := p.insertPair(t, "GET")
		b := p.insertPair(t, "GET")
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		if listed, detail := listedHighlight(t, p, a); listed != "null" || detail != "null" {
			t.Fatalf("highlight before setting = %s and %s, want null and null", listed, detail)
		}

		selection := fmt.Sprintf(`{"ids":[%q,%q]}`, a, b)
		steps := []struct {
			highlight string
			count     int
			want      string
		}{
			{highlight: "4", count: 2, want: "4"},
			{highlight: "4", count: 0, want: "4"},
			{highlight: "8", count: 2, want: "8"},
			{highlight: "null", count: 2, want: "null"},
			{highlight: "null", count: 0, want: "null"},
		}
		for _, step := range steps {
			body := `{"selection":` + selection + `,"highlight":` + step.highlight + `}`
			response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/highlight", body)
			assertControlAPIResponse(t, response, http.StatusOK, fmt.Sprintf(`{"count":%d}`+"\n", step.count))

			events := drainEvents(subscriber)
			want := fmt.Sprintf(`{"change":"highlight","count":%d,"selection":%s}`, step.count, selection)
			if len(events) != 1 || events[0].name != "traffic.changed" || string(events[0].data) != want {
				t.Fatalf("highlight %s:\nwanted:\none traffic.changed %s\ngot:\n%v", step.highlight, want, eventSummary(events))
			}
			if listed, detail := listedHighlight(t, p, b); listed != step.want || detail != step.want {
				t.Fatalf("highlight %s: read %s and %s, want %s", step.highlight, listed, detail, step.want)
			}
		}
	})

	t.Run("should highlight a query selection up to as_of", func(t *testing.T) {
		p := setupBulkProject(t)
		p.insertPair(t, "POST")
		asOf := p.insertPair(t, "POST")
		late := p.insertPair(t, "POST")

		body := fmt.Sprintf(`{"selection":{"query":"method = \"POST\"","as_of":%q},"highlight":2}`, asOf)
		response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/highlight", body)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")
		if listed, _ := listedHighlight(t, p, late); listed != "null" {
			t.Fatalf("highlight of the pair after as_of = %s, want null", listed)
		}

		response = requestControlAPI(p.server, http.MethodGet, "/traffic?q=highlight%20%3D%202", "")
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Items) != 2 {
			t.Fatalf("GET /traffic?q=highlight = 2: %d items, err %v: %s", len(list.Items), err, response.Body)
		}
	})

	t.Run("should reject invalid bodies and publish nothing", func(t *testing.T) {
		p := setupBulkProject(t)
		id := p.insertPair(t, "GET")
		ids := fmt.Sprintf(`{"ids":[%q]}`, id)
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		cases := []struct {
			body string
			want string
		}{
			{`{"selection":` + ids + `}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":` + ids + `,"highlight":0}`, `{"error":"invalid_bulk_request","message":"invalid highlight: 0 is not from 1 to 8"}`},
			{`{"selection":` + ids + `,"highlight":9}`, `{"error":"invalid_bulk_request","message":"invalid highlight: 9 is not from 1 to 8"}`},
			{`{"selection":` + ids + `,"highlight":2.5}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":` + ids + `,"highlight":"3"}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":` + ids + `,"highlight":3,"extra":1}`, `{"error":"invalid_bulk_request"}`},
			{`{"highlight":3}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":{"ids":[]},"highlight":3}`, `{"error":"invalid_selection","message":"invalid selection: ids is empty"}`},
			{fmt.Sprintf(`{"selection":{"query":"highlight = 9","as_of":%q},"highlight":3}`, id), `{"error":"invalid_query","message":"highlight needs a whole number from 1 to 8","position":13}`},
		}
		for _, c := range cases {
			response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/highlight", c.body)
			if response.Code != http.StatusBadRequest || response.Body.String() != c.want+"\n" {
				t.Errorf("body %s: response = %d %s, want 400 %s", c.body, response.Code, response.Body, c.want)
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
	})
}
