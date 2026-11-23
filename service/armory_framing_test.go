package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	armorymanager "github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/wordlist"
)

// grpcTemplate is a native gRPC template with a message-form body.
const grpcTemplate = "POST /test.Echo/Say HTTP/2\r\n:authority: example.com\r\ncontent-type: application/grpc\r\n\r\n--- message ---\n1:LEN {\"@@a@@\"}\n"

// newFramingServer serves the Armory routes with templates checked by the
// real Armory manager.
func newFramingServer(t *testing.T, repo *stubArmoryRepository) *Server {
	t.Helper()
	wordlists, err := wordlist.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := armorymanager.NewManager(repo, wordlists, func(context.Context, string, uuid.UUID, bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	service := &stubArmoryService{repo: repo, validateTemplate: manager.ValidateTemplate}
	return newTestServer(&marasi.Proxy{Armory: service, TrafficRepo: &stubTrafficRepository{}}, func() {})
}

func TestArmoryTemplateFramingControlAPI(t *testing.T) {
	t.Run("should carry the framing on create, get, list and update", func(t *testing.T) {
		repo := &stubArmoryRepository{}
		server := newFramingServer(t, repo)
		body, _ := json.Marshal(map[string]string{"name": "Say", "raw_template": grpcTemplate, "framing": "grpc"})

		response := requestControlAPI(server, http.MethodPost, "/armory/template", string(body))
		var created struct {
			ID      uuid.UUID `json:"id"`
			Framing string    `json:"framing"`
		}
		decodeResponse(t, response, &created)
		if response.Code != http.StatusOK || created.Framing != "grpc" || repo.templates[created.ID].Framing != "grpc" {
			t.Fatalf("the created template\nwanted:\nframing grpc\ngot:\n%d %s", response.Code, response.Body.String())
		}
		got := requestControlAPI(server, http.MethodGet, "/armory/template/"+created.ID.String(), "")
		if !strings.Contains(got.Body.String(), `"framing":"grpc"`) {
			t.Fatalf("the template\nwanted:\nframing grpc\ngot:\n%s", got.Body.String())
		}
		list := requestControlAPI(server, http.MethodGet, "/armory/template", "")
		if !strings.Contains(list.Body.String(), `"name":"Say","description":"","framing":"grpc"`) {
			t.Fatalf("the template list\nwanted:\nframing grpc\ngot:\n%s", list.Body.String())
		}

		renamed := requestControlAPI(server, http.MethodPost, "/armory/template/"+created.ID.String(), `{"name":"Renamed"}`)
		if renamed.Code != http.StatusOK || repo.templates[created.ID].Framing != "grpc" {
			t.Fatalf("a rename\nwanted:\nframing kept\ngot:\n%d %s", renamed.Code, renamed.Body.String())
		}
		cleared := requestControlAPI(server, http.MethodPost, "/armory/template/"+created.ID.String(), `{"framing":""}`)
		if cleared.Code != http.StatusOK || repo.templates[created.ID].Framing != "" || strings.Contains(cleared.Body.String(), "framing") {
			t.Fatalf("clearing the framing\nwanted:\nno framing\ngot:\n%d %s", cleared.Code, cleared.Body.String())
		}
	})

	t.Run("should refuse to save a gRPC template whose messages do not encode, naming the line and column", func(t *testing.T) {
		repo := &stubArmoryRepository{}
		server := newFramingServer(t, repo)
		body, _ := json.Marshal(map[string]string{"name": "Say", "raw_template": strings.Replace(grpcTemplate, "{", "{ 2:VARINT x", 1), "framing": "grpc"})

		response := requestControlAPI(server, http.MethodPost, "/armory/template", string(body))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "line 6 column") || len(repo.templates) != 0 {
			t.Fatalf("\nwanted:\n400 naming line 6, nothing stored\ngot:\n%d %s", response.Code, response.Body.String())
		}

		id := uuid.Must(uuid.NewV7())
		repo.templates = map[uuid.UUID]*domain.ArmoryTemplate{id: {ID: id, Name: "Plain", RawTemplate: "GET / HTTP/1.1\r\nHost: a\r\n\r\n"}}
		update := requestControlAPI(server, http.MethodPost, "/armory/template/"+id.String(), `{"framing":"grpc"}`)
		if update.Code != http.StatusBadRequest || repo.templates[id].Framing != "" {
			t.Fatalf("an update naming a framing the template cannot have\nwanted:\n400, unchanged\ngot:\n%d %s", update.Code, update.Body.String())
		}
	})

	t.Run("should keep the template's framing on a run created from it", func(t *testing.T) {
		id := uuid.Must(uuid.NewV7())
		repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{id: {ID: id, Name: "Say", RawTemplate: grpcTemplate, Framing: "grpc"}}}
		server := newFramingServer(t, repo)

		response := requestControlAPI(server, http.MethodPost, "/armory/run", `{"template_id":"`+id.String()+`","attack_type":"harpoon","wordlists":["w"]}`)
		var run struct {
			ID              uuid.UUID `json:"id"`
			TemplateFraming string    `json:"template_framing"`
		}
		decodeResponse(t, response, &run)
		if response.Code != http.StatusOK || run.TemplateFraming != "grpc" || repo.runs[run.ID].TemplateFraming != "grpc" {
			t.Fatalf("the run\nwanted:\ntemplate framing grpc\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})
}
