package service

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestControlAPIRefusesPendingLaunch(t *testing.T) {
	t.Run("should answer 409 launch_pending, publish nothing, and allow it once the launch finishes", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
		defer origin.Close()
		defer unblock()
		database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("opening database: %v", err)
		}
		defer database.Close()
		repo := db.NewProxyRepo(database)
		launchpadID, err := repo.CreateLaunchpad("Login", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		finished := make(chan domain.Launch, 1)
		proxy := &marasi.Proxy{LaunchpadRepo: repo, TrafficRepo: repo, ReportingRepo: repo, Client: origin.Client()}
		if err := proxy.WithOptions(marasi.WithLaunchFinishedHandler(func(launch domain.Launch) { finished <- launch })); err != nil {
			t.Fatalf("installing launch handler: %v", err)
		}
		server := newTestServer(proxy, func() {})
		p := &bulkProject{repo: repo, server: server}
		targetID, err := repo.CreateLaunchpad("Target", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		other := p.insertPair(t, "GET")
		id := startLaunch(t, server, launchpadID, launchBody(origin, "/slow"))
		move := `{"request_ids":["` + id.String() + `"],"target_launchpad_id":"` + targetID.String() + `"}`
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		refused := `{"error":"launch_pending","message":"launch pending: ` + id.String() + `","request_ids":["` + id.String() + `"]}` + "\n"
		selection := `{"selection":{"ids":["` + other.String() + `","` + id.String() + `"]}`
		for _, call := range []struct{ method, path, body string }{
			{http.MethodPost, "/traffic/bulk/delete", selection + `}`},
			{http.MethodPost, "/traffic/bulk/unlink", selection + `,"launchpad_id":"` + launchpadID.String() + `"}`},
			{http.MethodDelete, "/launchpad/" + launchpadID.String() + "/link/" + id.String(), ""},
			{http.MethodPost, "/launchpad/" + launchpadID.String() + "/move", move},
		} {
			response := requestControlAPI(server, call.method, call.path, call.body)
			assertControlAPIResponse(t, response, http.StatusConflict, refused)
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
		if response := requestControlAPI(server, http.MethodGet, "/traffic/"+other.String(), ""); response.Code != http.StatusOK {
			t.Fatalf("GET /traffic/{id} of the other pair = %d, want 200: %s", response.Code, response.Body)
		}

		unblock()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the launch to finish")
		}
		response := requestControlAPI(server, http.MethodPost, "/launchpad/"+launchpadID.String()+"/move", move)
		assertControlAPIResponse(t, response, http.StatusOK, `{"source_launchpad_id":"`+launchpadID.String()+`","target_launchpad_id":"`+targetID.String()+`","request_ids":["`+id.String()+`"]}`+"\n")
		response = requestControlAPI(server, http.MethodDelete, "/launchpad/"+targetID.String()+"/link/"+id.String(), "")
		assertControlAPIResponse(t, response, http.StatusOK, `{"launchpad_id":"`+targetID.String()+`","request_id":"`+id.String()+`"}`+"\n")
		response = requestControlAPI(server, http.MethodPost, "/traffic/bulk/delete", selection+`}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")
	})
}
