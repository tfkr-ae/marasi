package service

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

func TestTrafficMetadataControlAPI(t *testing.T) {
	id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")

	t.Run("should return an empty metadata object", func(t *testing.T) {
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: id}}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		response := requestControlAPI(server, http.MethodGet, "/traffic/"+id.String()+"/metadata", "")
		assertControlAPIResponse(t, response, http.StatusOK, "{}\n")
	})

	t.Run("should omit prettified metadata keys", func(t *testing.T) {
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{
			Request: domain.ProxyRequest{ID: id},
			Metadata: map[string]any{
				"foo":                 "bar",
				"prettified-request":  "GET /",
				"prettified-response": "200 OK",
			},
		}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		response := requestControlAPI(server, http.MethodGet, "/traffic/"+id.String()+"/metadata", "")
		assertControlAPIResponse(t, response, http.StatusOK, "{\"foo\":\"bar\"}\n")
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should reject a missing pair a junk id and a nil repository", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/traffic/"+id.String()+"/metadata", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/traffic/not-a-uuid/metadata", ""), http.StatusBadRequest, "{\"error\":\"bad_request\"}\n")
		nilRepo := newTestServer(&marasi.Proxy{}, func() {})
		assertControlAPIResponse(t, requestControlAPI(nilRepo, http.MethodGet, "/traffic/"+id.String()+"/metadata", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should replace metadata and publish the mutation", func(t *testing.T) {
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{
			Request:  domain.ProxyRequest{ID: id},
			Metadata: map[string]any{"launchpad": true, "intercepted": true, "highlight": "blue"},
		}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPut, "/traffic/"+id.String()+"/metadata", `{"highlight":"red"}`)
		want := `{"highlight":"red"}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		if repo.row.Metadata["highlight"] != "red" {
			t.Fatalf("\nwanted:\nstored highlight %q\ngot:\n%q", "red", repo.row.Metadata["highlight"])
		}
		if _, ok := repo.row.Metadata["launchpad"]; ok {
			t.Fatalf("\nwanted:\nlaunchpad removed\ngot:\n%v", repo.row.Metadata["launchpad"])
		}
		if _, ok := repo.row.Metadata["intercepted"]; ok {
			t.Fatalf("\nwanted:\nintercepted removed\ngot:\n%v", repo.row.Metadata["intercepted"])
		}
		event := <-subscriber.events
		wantEvent := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","metadata":{"highlight":"red"}}`
		if event.name != "metadata.updated" || string(event.data) != wantEvent {
			t.Fatalf("\nwanted:\nmetadata.updated %s\ngot:\n%s %s", wantEvent, event.name, event.data)
		}
	})

	t.Run("should overlay prettified bodies and recompute has_note", func(t *testing.T) {
		repo := &stubTrafficRepository{
			row: &domain.RequestResponseRow{
				Request: domain.ProxyRequest{ID: id},
				Metadata: map[string]any{
					"highlight":           "blue",
					"has_note":            false,
					"prettified-request":  "GET /",
					"prettified-response": "200 OK",
				},
			},
			notes: map[uuid.UUID]string{id: "needs review"},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPut, "/traffic/"+id.String()+"/metadata", `{"highlight":"red","has_note":false,"prettified-request":"client","prettified-response":"client"}`)
		want := `{"has_note":true,"highlight":"red"}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		if repo.row.Metadata["highlight"] != "red" || repo.row.Metadata["has_note"] != true {
			t.Fatalf("\nwanted:\nhighlight red and has_note true\ngot:\n%v", repo.row.Metadata)
		}
		if repo.row.Metadata["prettified-request"] != "GET /" || repo.row.Metadata["prettified-response"] != "200 OK" {
			t.Fatalf("\nwanted:\nprevious prettified bodies\ngot:\n%v", repo.row.Metadata)
		}
		event := <-subscriber.events
		wantEvent := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","metadata":{"has_note":true,"highlight":"red"}}`
		if event.name != "metadata.updated" || string(event.data) != wantEvent {
			t.Fatalf("\nwanted:\nmetadata.updated %s\ngot:\n%s %s", wantEvent, event.name, event.data)
		}
	})

	t.Run("should accept an empty object then apply the overlay", func(t *testing.T) {
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{
			Request: domain.ProxyRequest{ID: id},
			Metadata: map[string]any{
				"highlight":          "red",
				"prettified-request": "GET /",
			},
		}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		response := requestControlAPI(server, http.MethodPut, "/traffic/"+id.String()+"/metadata", `{}`)
		assertControlAPIResponse(t, response, http.StatusOK, "{}\n")
		if _, ok := repo.row.Metadata["highlight"]; ok {
			t.Fatalf("\nwanted:\nhighlight removed\ngot:\n%v", repo.row.Metadata["highlight"])
		}
		if repo.row.Metadata["prettified-request"] != "GET /" {
			t.Fatalf("\nwanted:\nprevious prettified-request\ngot:\n%v", repo.row.Metadata["prettified-request"])
		}
		if _, ok := repo.row.Metadata["has_note"]; ok {
			t.Fatalf("\nwanted:\nhas_note absent\ngot:\n%v", repo.row.Metadata["has_note"])
		}
	})

	t.Run("should reject invalid metadata bodies", func(t *testing.T) {
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: id}}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		for _, body := range []string{
			`[]`,
			`"metadata"`,
			`null`,
			`{"highlight":"red"} {}`,
			``,
			`highlight`,
		} {
			response := requestControlAPI(server, http.MethodPut, "/traffic/"+id.String()+"/metadata", body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, "{\"error\":\"invalid_metadata_request\"}\n")
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should reject a missing pair a junk id and a nil repository on put", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPut, "/traffic/"+id.String()+"/metadata", `{}`), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPut, "/traffic/not-a-uuid/metadata", `{}`), http.StatusBadRequest, "{\"error\":\"bad_request\"}\n")
		nilRepo := newTestServer(&marasi.Proxy{}, func() {})
		assertControlAPIResponse(t, requestControlAPI(nilRepo, http.MethodPut, "/traffic/"+id.String()+"/metadata", `{}`), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})
}
