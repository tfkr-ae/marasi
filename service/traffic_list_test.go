package service

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// trafficListProject is a real project with responded pairs of chosen
// content types, oldest first in ids.
type trafficListProject struct {
	repo   *db.Repository
	server *Server
	ids    []uuid.UUID
}

func setupTrafficListProject(t *testing.T, contentTypes ...string) *trafficListProject {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewProxyRepo(database)
	p := &trafficListProject{repo: repo, server: newTestServer(&marasi.Proxy{TrafficRepo: repo, LaunchpadRepo: repo}, func() {})}
	for _, contentType := range contentTypes {
		p.ids = append(p.ids, p.insert(t, contentType))
	}
	return p
}

// insert captures one responded pair with contentType, a millisecond after
// the previous one so ids follow insertion order.
func (p *trafficListProject) insert(t *testing.T, contentType string) uuid.UUID {
	t.Helper()
	time.Sleep(time.Millisecond)
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := p.repo.InsertRequest(&domain.ProxyRequest{ID: id, Scheme: "https", Method: "GET", Host: "example.com", Path: "/", Raw: []byte("GET / HTTP/1.1\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: now}); err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	if err := p.repo.InsertResponse(&domain.ProxyResponse{ID: id, Status: "200 OK", StatusCode: 200, ContentType: contentType, Length: "0", Raw: []byte{}, Metadata: map[string]any{}, RespondedAt: now}); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	return id
}

// trafficPageBody is the part of a GET /traffic body these tests read.
type trafficPageBody struct {
	Items []struct {
		ID uuid.UUID `json:"id"`
	} `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	PrevCursor *string    `json:"prev_cursor"`
	AsOf       *uuid.UUID `json:"as_of"`
}

func (b trafficPageBody) ids() []uuid.UUID {
	ids := make([]uuid.UUID, len(b.Items))
	for i, item := range b.Items {
		ids[i] = item.ID
	}
	return ids
}

func (p *trafficListProject) get(t *testing.T, path string) (*httptest.ResponseRecorder, trafficPageBody) {
	t.Helper()
	response := httptest.NewRecorder()
	p.server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	var body trafficPageBody
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
	}
	return response, body
}

func TestTrafficListQuery(t *testing.T) {
	t.Run("should hide excluded content types and keep pairs with none", func(t *testing.T) {
		p := setupTrafficListProject(t, "image/png", "application/json", "", "text/html", "image/svg+xml")

		response, body := p.get(t, "/traffic?exclude=image/*&exclude="+url.QueryEscape("Text/HTML; charset=utf-8"))

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d %s", http.StatusOK, response.Code, response.Body)
		}
		// Each page is oldest first.
		if want := []uuid.UUID{p.ids[1], p.ids[2]}; !reflect.DeepEqual(body.ids(), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, body.ids())
		}
		if body.AsOf == nil || *body.AsOf != p.ids[4] {
			t.Fatalf("\nwanted:\nas_of %v\ngot:\n%v", p.ids[4], body.AsOf)
		}
	})

	t.Run("should return 400 invalid_query naming an invalid exclusion", func(t *testing.T) {
		p := setupTrafficListProject(t)

		response, _ := p.get(t, "/traffic?exclude="+url.QueryEscape("*/json"))

		if response.Code != http.StatusBadRequest {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusBadRequest, response.Code)
		}
		want := `{"error":"invalid_query","message":"invalid content-type exclusion \"*/json\": * may only stand for a whole subtype, as in image/*"}` + "\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should return 400 invalid_cursor for a UUID cursor", func(t *testing.T) {
		p := setupTrafficListProject(t, "text/plain")

		response, _ := p.get(t, "/traffic?cursor="+p.ids[0].String())

		if response.Code != http.StatusBadRequest {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusBadRequest, response.Code)
		}
	})

	t.Run("should page forward and back under one as_of", func(t *testing.T) {
		p := setupTrafficListProject(t, "text/plain", "text/plain", "text/plain", "text/plain", "text/plain")

		_, first := p.get(t, "/traffic?limit=2")
		if want := []uuid.UUID{p.ids[3], p.ids[4]}; !reflect.DeepEqual(first.ids(), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, first.ids())
		}
		if first.NextCursor == nil || first.PrevCursor != nil {
			t.Fatalf("\nwanted:\nnext_cursor only\ngot:\nnext %v prev %v", first.NextCursor, first.PrevCursor)
		}
		_ = p.insert(t, "text/plain")
		asOf := first.AsOf.String()

		_, second := p.get(t, "/traffic?limit=2&as_of="+asOf+"&cursor="+url.QueryEscape(*first.NextCursor))
		if want := []uuid.UUID{p.ids[1], p.ids[2]}; !reflect.DeepEqual(second.ids(), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, second.ids())
		}
		if second.PrevCursor == nil || second.AsOf == nil || second.AsOf.String() != asOf {
			t.Fatalf("\nwanted:\nprev_cursor and as_of %s\ngot:\nprev %v as_of %v", asOf, second.PrevCursor, second.AsOf)
		}

		_, back := p.get(t, "/traffic?limit=5&direction=backward&as_of="+asOf+"&cursor="+url.QueryEscape(*second.PrevCursor))
		if want := []uuid.UUID{p.ids[3], p.ids[4]}; !reflect.DeepEqual(back.ids(), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, back.ids())
		}
		if back.PrevCursor != nil {
			t.Fatalf("\nwanted:\nnull prev_cursor at the start\ngot:\n%v", *back.PrevCursor)
		}
	})

	t.Run("should apply the exclusion to launchpad entries", func(t *testing.T) {
		p := setupTrafficListProject(t, "image/png", "application/json")
		launchpadID, err := p.repo.CreateLaunchpad("lp", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		for _, id := range p.ids {
			if err := p.repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
				t.Fatalf("linking: %v", err)
			}
		}

		response, body := p.get(t, "/launchpad/"+launchpadID.String()+"?exclude=image/*")

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d %s", http.StatusOK, response.Code, response.Body)
		}
		if want := []uuid.UUID{p.ids[1]}; !reflect.DeepEqual(body.ids(), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, body.ids())
		}

		response, _ = p.get(t, "/launchpad/"+launchpadID.String()+"?exclude=ima*")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusBadRequest, response.Code)
		}
	})
}
