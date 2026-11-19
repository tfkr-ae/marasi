package service

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// protocolsBody is the part of a pair read or list item these tests read.
type protocolsBody struct {
	ID             uuid.UUID `json:"id"`
	ClientProtocol string    `json:"client_protocol"`
	OriginProtocol string    `json:"origin_protocol"`
}

// insertProtocols captures one responded pair with the given protocols.
func (p *trafficListProject) insertProtocols(t *testing.T, client, origin string) uuid.UUID {
	t.Helper()
	time.Sleep(time.Millisecond)
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := p.repo.InsertRequest(&domain.ProxyRequest{ID: id, Scheme: "https", Method: "GET", Host: "example.com", Path: "/",
		Raw: []byte("GET / HTTP/2\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: now, ClientProtocol: client, OriginProtocol: origin}); err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	if err := p.repo.InsertResponse(&domain.ProxyResponse{ID: id, Status: "200 OK", StatusCode: 200, Raw: []byte{}, Metadata: map[string]any{}, RespondedAt: now}); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	return id
}

func (p *trafficListProject) getJSON(t *testing.T, path string, into any) {
	t.Helper()
	response := httptest.NewRecorder()
	p.server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s\nwanted:\n200\ngot:\n%d %s", path, response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), into); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
}

func TestTrafficProtocols(t *testing.T) {
	t.Run("should show each pair's client and origin protocol in a pair read and the traffic list", func(t *testing.T) {
		p := setupTrafficListProject(t)
		h2 := p.insertProtocols(t, domain.ProtocolHTTP2, domain.ProtocolHTTP2)
		h1 := p.insertProtocols(t, domain.ProtocolHTTP1, domain.ProtocolHTTP1)

		for id, want := range map[uuid.UUID]string{h2: "h2", h1: "http/1.1"} {
			var pair protocolsBody
			p.getJSON(t, "/traffic/"+id.String(), &pair)
			if pair.ClientProtocol != want || pair.OriginProtocol != want {
				t.Fatalf("pair read\nwanted:\n%s %s\ngot:\n%+v", want, want, pair)
			}
		}
		var list struct {
			Items []protocolsBody `json:"items"`
		}
		p.getJSON(t, "/traffic", &list)
		got := map[uuid.UUID]string{}
		for _, item := range list.Items {
			got[item.ID] = item.ClientProtocol + " " + item.OriginProtocol
		}
		if want := map[uuid.UUID]string{h2: "h2 h2", h1: "http/1.1 http/1.1"}; !maps.Equal(got, want) {
			t.Fatalf("traffic list\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should select pairs by client_protocol in a query", func(t *testing.T) {
		p := setupTrafficListProject(t)
		h2 := p.insertProtocols(t, domain.ProtocolHTTP2, domain.ProtocolHTTP2)
		p.insertProtocols(t, domain.ProtocolHTTP1, domain.ProtocolHTTP1)

		_, body := p.get(t, "/traffic?q="+url.QueryEscape(`client_protocol = "h2"`))
		if ids := body.ids(); len(ids) != 1 || ids[0] != h2 {
			t.Fatalf("\nwanted:\n[%v]\ngot:\n%v", h2, ids)
		}
	})
}
