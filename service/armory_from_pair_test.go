package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
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

// unusedWordlists is a wordlist provider for tests that start no run.
type unusedWordlists struct{ wordlist.Provider }

func TestArmoryTemplateFromPair(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/armory.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := db.NewProxyRepo(conn)
	manager, err := armorymanager.NewManager(repo, unusedWordlists{}, func(context.Context, string, uuid.UUID, bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(&marasi.Proxy{TrafficRepo: repo, StreamRepo: repo, ArmoryRepo: repo, Armory: manager}, func() {})

	// The body holds every sequence the template preprocessor reads
	// specially, so an unescaped copy renders differently from the capture.
	captured := "POST /api/notes?q=a@b HTTP/1.1\r\nHost: notes.example\r\nContent-Type: application/json\r\nContent-Length: 61\r\n\r\n" +
		`{"path":"C:\\temp","at":"\@x","m":"@@y@@","t":"{{.x}} }}{{{"}`
	pairID, _ := uuid.NewV7()
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID: pairID, Scheme: "https", Method: "POST", Host: "notes.example", Path: "/api/notes?q=a@b",
		Raw: []byte(captured), Metadata: map[string]any{}, RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	create := func(t *testing.T, id string) armoryTemplate {
		t.Helper()
		response := requestControlAPI(server, http.MethodPost, "/armory/template/from-pair/"+id, "")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		var template armoryTemplate
		if err := json.Unmarshal(response.Body.Bytes(), &template); err != nil {
			t.Fatalf("decoding template: %v", err)
		}
		return template
	}

	t.Run("should name the template METHOD host path and store it", func(t *testing.T) {
		template := create(t, pairID.String())
		if want := "POST notes.example /api/notes?q=a@b"; template.Name != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, template.Name)
		}
		stored, err := repo.GetArmoryTemplate(template.ID)
		if err != nil || stored == nil {
			t.Fatalf("\nwanted:\nthe template stored\ngot:\n%v %v", stored, err)
		}
		if stored.RawTemplate != template.RawTemplate {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", template.RawTemplate, stored.RawTemplate)
		}
	})

	t.Run("should render back to the captured request byte for byte", func(t *testing.T) {
		template := create(t, pairID.String())
		want := "POST /api/notes?q=a\\@b HTTP/1.1\r\nHost: notes.example\r\nContent-Type: application/json\r\nContent-Length: 61\r\n\r\n" +
			`{"path":"C:\\\\temp","at":"\\\@x","m":"\@\@y\@\@","t":"` + "{{`{{`}}.x}} }}{{`{{`}}{\"}"
		if template.RawTemplate != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, template.RawTemplate)
		}
		rendered, err := armorymanager.Render(template.RawTemplate, template.Framing)
		if err != nil {
			t.Fatal(err)
		}
		if rendered != captured {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", captured, rendered)
		}
	})

	t.Run("should refuse a run on the template until it has a payload position", func(t *testing.T) {
		template := create(t, pairID.String())
		body := `{"raw_template":` + jsonString(template.RawTemplate) + `,"attack_type":"harpoon","wordlists":["w"]}`
		response := requestControlAPI(server, http.MethodPost, "/armory/run/validate", body)
		if !strings.Contains(response.Body.String(), "requires at least one payload position") {
			t.Fatalf("\nwanted:\nrequires at least one payload position\ngot:\n%d %s", response.Code, response.Body)
		}
	})

	t.Run("should answer not_found for a pair that is not stored", func(t *testing.T) {
		missing, _ := uuid.NewV7()
		response := requestControlAPI(server, http.MethodPost, "/armory/template/from-pair/"+missing.String(), "")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"not_found"`) {
			t.Fatalf("\nwanted:\n404 not_found\ngot:\n%d %s", response.Code, response.Body)
		}
	})

	t.Run("should answer bad request for an ID that is not a UUID", func(t *testing.T) {
		response := requestControlAPI(server, http.MethodPost, "/armory/template/from-pair/nope", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("\nwanted:\n400\ngot:\n%d %s", response.Code, response.Body)
		}
	})
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
