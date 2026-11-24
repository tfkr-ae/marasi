package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	armorymanager "github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/wordlist"
)

// failedAttemptsBody is a GET /armory/run/{id}/failed-attempts body.
type failedAttemptsBody struct {
	Items []struct {
		ID       uuid.UUID `json:"id"`
		RunID    uuid.UUID `json:"run_id"`
		Payloads []struct {
			Position int    `json:"position"`
			Value    string `json:"value"`
		} `json:"payloads"`
		Error     string    `json:"error"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"items"`
}

func TestArmoryFailedAttemptsControlAPI(t *testing.T) {
	t.Run("should list a run's failed attempts, count them in its progress and announce each one", func(t *testing.T) {
		database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("opening database: %v", err)
		}
		t.Cleanup(func() { database.Close() })
		repo := db.NewProxyRepo(database)
		configDir := t.TempDir()
		wordlists, err := wordlist.NewManager(configDir)
		if err != nil {
			t.Fatalf("creating wordlist manager: %v", err)
		}
		if err := os.WriteFile(configDir+"/wordlists/ids.txt", []byte("1\nabc\n3\n"), 0600); err != nil {
			t.Fatalf("writing wordlist: %v", err)
		}
		manager, err := armorymanager.NewManager(repo, wordlists, func(context.Context, string, uuid.UUID, bool) error { return nil })
		if err != nil {
			t.Fatalf("creating Armory manager: %v", err)
		}
		server := newTestServer(&marasi.Proxy{Armory: manager, TrafficRepo: repo}, func() {})
		manager.SetRunUpdated(func(run *domain.ArmoryRun) { publishArmoryRunUpdated(server.events, run) })
		manager.SetFailedAttemptRecorded(func(attempt *domain.ArmoryFailedAttempt) { publishArmoryFailedAttempt(server.events, attempt) })
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		raw := "POST /test.Echo/Say HTTP/2\r\n:authority: example.com\r\ncontent-type: application/grpc\r\n\r\n--- message ---\n1:VARINT @@1@@\n"
		body, _ := json.Marshal(map[string]string{"name": "Echo", "raw_template": raw, "framing": "grpc"})
		response := requestControlAPI(server, http.MethodPost, "/armory/template", string(body))
		var template armoryTemplate
		decodeResponse(t, response, &template)
		body, _ = json.Marshal(map[string]any{"template_id": template.ID, "attack_type": "harpoon", "wordlists": []string{"ids.txt"}})
		response = requestControlAPI(server, http.MethodPost, "/armory/run", string(body))
		var run armoryRun
		decodeResponse(t, response, &run)
		if response := requestControlAPI(server, http.MethodPost, "/armory/run/"+run.ID.String()+"/start", ""); response.Code != http.StatusOK {
			t.Fatalf("starting the run\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}

		var announced, finished serviceEvent
		for finished.name == "" {
			event := waitForArmoryEvent(t, subscriber.events)
			switch {
			case event.name == "armory.failed_attempt.created":
				announced = event
			case event.name == "armory.run.updated" && strings.Contains(string(event.data), `"status":"complete"`):
				finished = event
			}
		}
		if !strings.Contains(string(announced.data), `"payloads":[{"position":0,"value":"abc"}]`) || !strings.Contains(string(announced.data), `"run_id":"`+run.ID.String()+`"`) {
			t.Fatalf("the announced failed attempt\nwanted:\nrun %s, payload abc\ngot:\n%s", run.ID, announced.data)
		}
		if !strings.Contains(string(finished.data), `"progress":{"entries":0,"failed_attempts":1}`) {
			t.Fatalf("the finished run's progress\nwanted:\n1 failed attempt\ngot:\n%s", finished.data)
		}

		response = requestControlAPI(server, http.MethodGet, "/armory/run/"+run.ID.String()+"/failed-attempts", "")
		var listed failedAttemptsBody
		decodeResponse(t, response, &listed)
		if len(listed.Items) != 1 {
			t.Fatalf("failed attempts\nwanted:\n1\ngot:\n%s", response.Body.String())
		}
		item := listed.Items[0]
		if item.RunID != run.ID || len(item.Payloads) != 1 || item.Payloads[0].Value != "abc" || item.Payloads[0].Position != 0 ||
			!strings.Contains(item.Error, "line 6 column 10") || item.CreatedAt.IsZero() || string(announced.data) != mustJSON(t, item) {
			t.Fatalf("the failed attempt\nwanted:\npayload abc at position 0, its parse error, as announced\ngot:\n%s\nannounced:\n%s", response.Body.String(), announced.data)
		}

		response = requestControlAPI(server, http.MethodGet, "/armory/run/"+run.ID.String(), "")
		if !strings.Contains(response.Body.String(), `"progress":{"entries":0,"failed_attempts":1}`) {
			t.Fatalf("the run's progress\nwanted:\n1 failed attempt\ngot:\n%s", response.Body.String())
		}
	})

	t.Run("should answer not found for a run that does not exist", func(t *testing.T) {
		server := newArmoryServer(&stubArmoryRepository{})
		response := requestControlAPI(server, http.MethodGet, "/armory/run/"+uuid.Must(uuid.NewV7()).String()+"/failed-attempts", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("\nwanted:\n404\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})
}
