package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	armorymanager "github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/wordlist"
)

type stubArmoryRepository struct {
	domain.ArmoryRepository
	templates    map[uuid.UUID]*domain.ArmoryTemplate
	runs         map[uuid.UUID]*domain.ArmoryRun
	beforeDelete func()
}

func (repo *stubArmoryRepository) CreateArmoryTemplate(template *domain.ArmoryTemplate) error {
	if repo.templates == nil {
		repo.templates = make(map[uuid.UUID]*domain.ArmoryTemplate)
	}
	copy := *template
	repo.templates[template.ID] = &copy
	return nil
}

func (repo *stubArmoryRepository) GetArmoryTemplates() ([]*domain.ArmoryTemplate, error) {
	ids := make([]uuid.UUID, 0, len(repo.templates))
	for id := range repo.templates {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	templates := make([]*domain.ArmoryTemplate, 0, len(ids))
	for _, id := range ids {
		copy := *repo.templates[id]
		templates = append(templates, &copy)
	}
	return templates, nil
}

func (repo *stubArmoryRepository) GetArmoryTemplate(id uuid.UUID) (*domain.ArmoryTemplate, error) {
	template := repo.templates[id]
	if template == nil {
		return nil, domain.ErrArmoryTemplateNotFound
	}
	copy := *template
	return &copy, nil
}

func (repo *stubArmoryRepository) UpdateArmoryTemplate(template *domain.ArmoryTemplate) error {
	copy := *template
	repo.templates[template.ID] = &copy
	return nil
}

func (repo *stubArmoryRepository) DeleteArmoryTemplate(id uuid.UUID) error {
	delete(repo.templates, id)
	return nil
}

func (repo *stubArmoryRepository) GetArmoryRun(id uuid.UUID) (*domain.ArmoryRun, error) {
	run := repo.runs[id]
	if run == nil {
		return nil, nil
	}
	copy := *run
	return &copy, nil
}

func (repo *stubArmoryRepository) GetArmoryRuns(templateID uuid.UUID) ([]*domain.ArmoryRun, error) {
	runs := make([]*domain.ArmoryRun, 0)
	for _, run := range repo.runs {
		if run.TemplateID == templateID {
			copy := *run
			copy.Wordlists = append([]string(nil), run.Wordlists...)
			runs = append(runs, &copy)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].ID.String() < runs[j].ID.String()
		}
		return runs[i].CreatedAt.Before(runs[j].CreatedAt)
	})
	return runs, nil
}

func (repo *stubArmoryRepository) CreateArmoryRun(run *domain.ArmoryRun) error {
	if repo.runs == nil {
		repo.runs = make(map[uuid.UUID]*domain.ArmoryRun)
	}
	copy := *run
	copy.Wordlists = append([]string(nil), run.Wordlists...)
	repo.runs[run.ID] = &copy
	return nil
}

func (repo *stubArmoryRepository) UpdateArmoryRun(run *domain.ArmoryRun) error {
	copy := *run
	copy.Wordlists = append([]string(nil), run.Wordlists...)
	repo.runs[run.ID] = &copy
	return nil
}

func (repo *stubArmoryRepository) DeleteArmoryRun(id uuid.UUID) error {
	if repo.beforeDelete != nil {
		repo.beforeDelete()
	}
	delete(repo.runs, id)
	return nil
}

type stubArmoryService struct {
	marasi.ArmoryService
	repo       domain.ArmoryRepository
	active     []uuid.UUID
	validate   func(*domain.ArmoryRun) error
	start      func(uuid.UUID) error
	cancel     func(uuid.UUID) error
	validateMu sync.Mutex
	validated  []*domain.ArmoryRun
}

func (service *stubArmoryService) Repo() domain.ArmoryRepository { return service.repo }
func (service *stubArmoryService) ActiveRunIDs() []uuid.UUID     { return service.active }
func (service *stubArmoryService) ValidateRun(run *domain.ArmoryRun) error {
	service.validateMu.Lock()
	service.validated = append(service.validated, run)
	service.validateMu.Unlock()
	if service.validate != nil {
		return service.validate(run)
	}
	return nil
}
func (service *stubArmoryService) StartRun(id uuid.UUID) error {
	if service.start != nil {
		return service.start(id)
	}
	return nil
}
func (service *stubArmoryService) CancelRun(id uuid.UUID) error {
	if service.cancel != nil {
		return service.cancel(id)
	}
	return nil
}

func TestArmoryTemplateControlAPI(t *testing.T) {
	t.Run("should create a template with empty optional fields and publish it", func(t *testing.T) {
		repo := &stubArmoryRepository{}
		server := newArmoryServer(repo)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPost, "/armory/template", `{"name":"Login"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		var created struct {
			ID          uuid.UUID `json:"id"`
			Name        string    `json:"name"`
			Description string    `json:"description"`
			RawTemplate string    `json:"raw_template"`
		}
		decodeResponse(t, response, &created)
		if created.ID == uuid.Nil || created.Name != "Login" || created.Description != "" || created.RawTemplate != "" {
			t.Fatalf("\nwanted:\ncreated Login template with empty optional fields\ngot:\n%+v", created)
		}
		want := fmt.Sprintf(`{"id":%q,"name":"Login","description":"","raw_template":""}`, created.ID)
		if response.Body.String() != want+"\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "armory.template.created" || string(event.data) != want {
			t.Fatalf("\nwanted:\narmory.template.created %s\ngot:\n%s %s", want, event.name, event.data)
		}

		duplicate := requestControlAPI(server, http.MethodPost, "/armory/template", `{"name":"Login"}`)
		if duplicate.Code != http.StatusOK || len(repo.templates) != 2 {
			t.Fatalf("\nwanted:\nduplicate template name accepted\ngot:\n%d with %d templates", duplicate.Code, len(repo.templates))
		}
	})

	t.Run("should list templates by id without raw template", func(t *testing.T) {
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{
			newer: {ID: newer, Name: "New", Description: "newest", RawTemplate: "secret-new"},
			older: {ID: older, Name: "Old", Description: "oldest", RawTemplate: "secret-old"},
		}}
		server := newArmoryServer(repo)

		response := requestControlAPI(server, http.MethodGet, "/armory/template", "")
		want := `{"items":[{"id":"0193802f-f0e7-73d9-a764-06d21e367809","name":"Old","description":"oldest"},{"id":"01938032-1b17-7243-b035-e6a9f4645904","name":"New","description":"newest"}]}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)

		empty := newArmoryServer(&stubArmoryRepository{})
		assertControlAPIResponse(t, requestControlAPI(empty, http.MethodGet, "/armory/template", ""), http.StatusOK, "{\"items\":[]}\n")
	})

	t.Run("should get a template with raw template and reject an invalid id", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{
			id: {ID: id, Name: "Login", Description: "Try variants", RawTemplate: "GET / HTTP/1.1\r\n\r\n"},
		}}
		server := newArmoryServer(repo)

		response := requestControlAPI(server, http.MethodGet, "/armory/template/"+id.String(), "")
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","name":"Login","description":"Try variants","raw_template":"GET / HTTP/1.1\r\n\r\n"}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/armory/template/not-a-uuid", ""), http.StatusBadRequest, "{\"error\":\"bad_request\"}\n")
	})

	t.Run("should update only supplied fields and publish the result", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{
			id: {ID: id, Name: "Login", Description: "old", RawTemplate: "old raw"},
		}}
		server := newArmoryServer(repo)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPost, "/armory/template/"+id.String(), `{"description":"","raw_template":""}`)
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","name":"Login","description":"","raw_template":""}`
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		event := <-subscriber.events
		if event.name != "armory.template.updated" || string(event.data) != want {
			t.Fatalf("\nwanted:\narmory.template.updated %s\ngot:\n%s %s", want, event.name, event.data)
		}
	})

	t.Run("should delete an inactive template but reject one with an active run", func(t *testing.T) {
		deletedID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		activeID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		runID := uuid.MustParse("01938033-298e-73dc-b640-eb321b621154")
		repo := &stubArmoryRepository{
			templates: map[uuid.UUID]*domain.ArmoryTemplate{
				deletedID: {ID: deletedID, Name: "Delete"},
				activeID:  {ID: activeID, Name: "Active"},
			},
			runs: map[uuid.UUID]*domain.ArmoryRun{runID: {ID: runID, TemplateID: activeID}},
		}
		service := &stubArmoryService{repo: repo, active: []uuid.UUID{runID}}
		server := newTestServer(&marasi.Proxy{Armory: service}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodDelete, "/armory/template/"+deletedID.String(), "")
		want := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809"}`
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		event := <-subscriber.events
		if event.name != "armory.template.deleted" || string(event.data) != want {
			t.Fatalf("\nwanted:\narmory.template.deleted %s\ngot:\n%s %s", want, event.name, event.data)
		}

		response = requestControlAPI(server, http.MethodDelete, "/armory/template/"+activeID.String(), "")
		assertControlAPIResponse(t, response, http.StatusConflict, "{\"error\":\"template_has_active_run\"}\n")
		if repo.templates[activeID] == nil {
			t.Fatal("active template was deleted")
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should reject invalid bodies", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{id: {ID: id, Name: "Login"}}}
		server := newArmoryServer(repo)
		for _, body := range []string{
			`{}`,
			`{"name":""}`,
			`{"name":"   "}`,
			`{"name":null}`,
			`{"name":"Login","description":null}`,
			`{"name":"Login","raw_template":null}`,
			`{"name":"Login","extra":true}`,
			`{"name":"Login"} {}`,
		} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/template", body), http.StatusBadRequest, "{\"error\":\"invalid_armory_request\"}\n")
		}
		for _, body := range []string{
			`{"name":""}`,
			`{"name":null}`,
			`{"description":null}`,
			`{"raw_template":null}`,
			`{"extra":true}`,
			`{} {}`,
		} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/template/"+id.String(), body), http.StatusBadRequest, "{\"error\":\"invalid_armory_request\"}\n")
		}
	})

	t.Run("should return not found for missing templates and Armory dependencies", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		server := newArmoryServer(&stubArmoryRepository{})
		for _, request := range []struct {
			method string
			body   string
		}{
			{method: http.MethodGet},
			{method: http.MethodPost, body: `{}`},
			{method: http.MethodDelete},
		} {
			assertControlAPIResponse(t, requestControlAPI(server, request.method, "/armory/template/"+id.String(), request.body), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		}

		for _, proxy := range []*marasi.Proxy{{}, {Armory: &stubArmoryService{}}} {
			server := newTestServer(proxy, func() {})
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/armory/template", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		}
	})
}

func TestArmoryTemplateEventFrames(t *testing.T) {
	repo := &stubArmoryRepository{}
	server := newArmoryServer(repo)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	stream, reader := connectEventStream(t, httpServer.URL)
	defer stream.Body.Close()

	response := sendArmoryRequest(t, http.MethodPost, httpServer.URL+"/armory/template", `{"name":"Login"}`)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("reading create response: %v", err)
	}
	var created armoryTemplate
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	createdJSON := fmt.Sprintf(`{"id":%q,"name":"Login","description":"","raw_template":""}`, created.ID)
	if got := readEventFrame(t, reader); got != "event: armory.template.created\ndata: "+createdJSON+"\n\n" {
		t.Fatalf("unexpected created event:\n%s", got)
	}

	response = sendArmoryRequest(t, http.MethodPost, httpServer.URL+"/armory/template/"+created.ID.String(), `{"name":"Login v2"}`)
	response.Body.Close()
	updatedJSON := fmt.Sprintf(`{"id":%q,"name":"Login v2","description":"","raw_template":""}`, created.ID)
	if got := readEventFrame(t, reader); got != "event: armory.template.updated\ndata: "+updatedJSON+"\n\n" {
		t.Fatalf("unexpected updated event:\n%s", got)
	}

	response = sendArmoryRequest(t, http.MethodDelete, httpServer.URL+"/armory/template/"+created.ID.String(), "")
	response.Body.Close()
	deletedJSON := fmt.Sprintf(`{"id":%q}`, created.ID)
	if got := readEventFrame(t, reader); got != "event: armory.template.deleted\ndata: "+deletedJSON+"\n\n" {
		t.Fatalf("unexpected deleted event:\n%s", got)
	}
}

func TestArmoryRunEventFrames(t *testing.T) {
	templateID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{
		templateID: {
			ID:          templateID,
			Name:        "Login",
			RawTemplate: "GET /?value=@@value@@ HTTP/1.1\r\nHost: example.com\r\n\r\n",
		},
	}}
	server := newArmoryServer(repo)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	stream, reader := connectEventStream(t, httpServer.URL)
	defer stream.Body.Close()

	createBody := fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon","wordlists":["passwords.txt"]}`, templateID)
	response := sendArmoryRequest(t, http.MethodPost, httpServer.URL+"/armory/run", createBody)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("reading create run response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("creating run: %d %s", response.StatusCode, body)
	}
	var created armoryRun
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decoding create run response: %v", err)
	}
	createdJSON := strings.TrimSpace(string(body))
	if got := readEventFrame(t, reader); got != "event: armory.run.created\ndata: "+createdJSON+"\n\n" {
		t.Fatalf("unexpected created run event:\n%s", got)
	}

	response = sendArmoryRequest(t, http.MethodDelete, httpServer.URL+"/armory/run/"+created.ID.String(), "")
	response.Body.Close()
	deletedJSON := fmt.Sprintf(`{"id":%q}`, created.ID)
	if got := readEventFrame(t, reader); got != "event: armory.run.deleted\ndata: "+deletedJSON+"\n\n" {
		t.Fatalf("unexpected deleted run event:\n%s", got)
	}
}

func TestArmoryRunControlAPI(t *testing.T) {
	templateID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	runID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
	rawTemplate := "GET /?value=@@value@@ HTTP/1.1\r\nHost: example.com\r\n\r\n"
	createdAt := time.Date(2026, time.September, 17, 10, 30, 0, 0, time.UTC)

	t.Run("should create a validated draft from a template using defaults", func(t *testing.T) {
		repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{
			templateID: {ID: templateID, Name: "Login", RawTemplate: rawTemplate},
		}}
		armoryService := &stubArmoryService{repo: repo}
		server := newTestServer(&marasi.Proxy{Armory: armoryService}, func() {})
		response := requestControlAPI(server, http.MethodPost, "/armory/run", fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon","wordlists":["passwords.txt"]}`, templateID))
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		var created armoryRun
		decodeResponse(t, response, &created)
		if created.ID == uuid.Nil || created.TemplateID != templateID || created.TemplateSnapshot != rawTemplate || !created.UseHTTPS || created.MaxConcurrent != 10 || created.Status != domain.ArmoryRunDraft {
			t.Fatalf("\nwanted:\ndefaulted draft with template snapshot\ngot:\n%+v", created)
		}
		if len(armoryService.validated) != 1 || armoryService.validated[0].ID != created.ID {
			t.Fatalf("\nwanted:\ncreated run validated once\ngot:\n%+v", armoryService.validated)
		}
		if len(repo.runs) != 1 {
			t.Fatalf("\nwanted:\none persisted run\ngot:\n%d", len(repo.runs))
		}
		want := fmt.Sprintf(`{"id":%q,"template_id":%q,"template_snapshot":"GET /?value=@@value@@ HTTP/1.1\r\nHost: example.com\r\n\r\n","use_https":true,"wordlists":["passwords.txt"],"status":"draft","attack_type":"harpoon","max_concurrent":10,"created_at":%q,"started_at":null,"finished_at":null}`+"\n", created.ID, templateID, created.CreatedAt.Format(time.RFC3339Nano))
		assertControlAPIResponse(t, response, http.StatusOK, want)
	})

	t.Run("should list runs oldest first and get a run", func(t *testing.T) {
		olderID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367810")
		newerID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645910")
		repo := &stubArmoryRepository{
			templates: map[uuid.UUID]*domain.ArmoryTemplate{templateID: {ID: templateID, Name: "Login"}},
			runs: map[uuid.UUID]*domain.ArmoryRun{
				newerID: testServiceArmoryRun(newerID, templateID, createdAt.Add(time.Minute)),
				olderID: testServiceArmoryRun(olderID, templateID, createdAt),
			},
		}
		server := newArmoryServer(repo)

		response := requestControlAPI(server, http.MethodGet, "/armory/template/"+templateID.String()+"/run", "")
		olderJSON := fmt.Sprintf(`{"id":%q,"template_id":%q,"template_snapshot":"GET /?value=@@value@@ HTTP/1.1\r\nHost: example.com\r\n\r\n","use_https":true,"wordlists":["passwords.txt"],"status":"draft","attack_type":"harpoon","max_concurrent":10,"created_at":"2026-09-17T10:30:00Z","started_at":null,"finished_at":null}`, olderID, templateID)
		newerJSON := fmt.Sprintf(`{"id":%q,"template_id":%q,"template_snapshot":"GET /?value=@@value@@ HTTP/1.1\r\nHost: example.com\r\n\r\n","use_https":true,"wordlists":["passwords.txt"],"status":"draft","attack_type":"harpoon","max_concurrent":10,"created_at":"2026-09-17T10:31:00Z","started_at":null,"finished_at":null}`, newerID, templateID)
		assertControlAPIResponse(t, response, http.StatusOK, fmt.Sprintf(`{"items":[%s,%s]}`+"\n", olderJSON, newerJSON))
		get := requestControlAPI(server, http.MethodGet, "/armory/run/"+olderID.String(), "")
		assertControlAPIResponse(t, get, http.StatusOK, olderJSON+"\n")

		emptyRepo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{templateID: {ID: templateID}}}
		assertControlAPIResponse(t, requestControlAPI(newArmoryServer(emptyRepo), http.MethodGet, "/armory/template/"+templateID.String()+"/run", ""), http.StatusOK, "{\"items\":[]}\n")
		missingID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645999")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/armory/template/"+missingID.String()+"/run", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/armory/run/"+missingID.String(), ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	})

	t.Run("should validate an unsaved snapshot without persisting", func(t *testing.T) {
		repo := &stubArmoryRepository{}
		armoryService := &stubArmoryService{repo: repo}
		server := newTestServer(&marasi.Proxy{Armory: armoryService}, func() {})
		body := fmt.Sprintf(`{"raw_template":%q,"attack_type":"harpoon","wordlists":["passwords.txt"]}`, rawTemplate)
		response := requestControlAPI(server, http.MethodPost, "/armory/run/validate", body)
		assertControlAPIResponse(t, response, http.StatusOK, "{\"status\":\"ok\"}\n")
		if len(repo.runs) != 0 || len(armoryService.validated) != 1 || !armoryService.validated[0].UseHTTPS || armoryService.validated[0].MaxConcurrent != 10 {
			t.Fatalf("\nwanted:\ndefaulted validation without persistence\ngot:\nruns=%d validated=%+v", len(repo.runs), armoryService.validated)
		}
	})

	t.Run("should reject invalid create and validate bodies", func(t *testing.T) {
		repo := &stubArmoryRepository{templates: map[uuid.UUID]*domain.ArmoryTemplate{templateID: {ID: templateID, RawTemplate: rawTemplate}}}
		armoryService := &stubArmoryService{repo: repo, validate: func(*domain.ArmoryRun) error { return errors.New("invalid run") }}
		server := newTestServer(&marasi.Proxy{Armory: armoryService}, func() {})
		for _, body := range []string{
			`{}`,
			fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon"}`, templateID),
			fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon","wordlists":null}`, templateID),
			fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon","wordlists":["words.txt"],"max_concurrent":0}`, templateID),
			fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon","wordlists":["words.txt"],"max_concurrent":101}`, templateID),
			fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon","wordlists":["words.txt"],"extra":true}`, templateID),
		} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/run", body), http.StatusBadRequest, "{\"error\":\"invalid_armory_request\"}\n")
		}
		validateBody := fmt.Sprintf(`{"raw_template":%q,"attack_type":"harpoon","wordlists":["words.txt"]}`, rawTemplate)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/run/validate", validateBody), http.StatusBadRequest, "{\"error\":\"invalid_armory_request\"}\n")
		if len(repo.runs) != 0 {
			t.Fatalf("invalid run was persisted: %+v", repo.runs)
		}
	})

	t.Run("should start cancel and delete according to active state", func(t *testing.T) {
		run := testServiceArmoryRun(runID, templateID, createdAt)
		repo := &stubArmoryRepository{runs: map[uuid.UUID]*domain.ArmoryRun{runID: run}}
		armoryService := &stubArmoryService{repo: repo}
		armoryService.start = func(id uuid.UUID) error {
			stored := repo.runs[id]
			stored.Status = domain.ArmoryRunInProgress
			startedAt := createdAt.Add(time.Minute)
			stored.StartedAt = &startedAt
			armoryService.active = []uuid.UUID{id}
			return nil
		}
		cancelled := false
		armoryService.cancel = func(uuid.UUID) error { cancelled = true; return nil }
		server := newTestServer(&marasi.Proxy{Armory: armoryService}, func() {})

		started := requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/start", "")
		startedJSON := fmt.Sprintf(`{"id":%q,"template_id":%q,"template_snapshot":"GET /?value=@@value@@ HTTP/1.1\r\nHost: example.com\r\n\r\n","use_https":true,"wordlists":["passwords.txt"],"status":"in_progress","attack_type":"harpoon","max_concurrent":10,"created_at":"2026-09-17T10:30:00Z","started_at":"2026-09-17T10:31:00Z","finished_at":null}`+"\n", runID, templateID)
		assertControlAPIResponse(t, started, http.StatusOK, startedJSON)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/start", ""), http.StatusConflict, "{\"error\":\"run_active\"}\n")

		cancelResponse := requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/cancel", "")
		assertControlAPIResponse(t, cancelResponse, http.StatusOK, startedJSON)
		if !cancelled {
			t.Fatal("active run was not cancelled")
		}
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/armory/run/"+runID.String(), ""), http.StatusConflict, "{\"error\":\"run_active\"}\n")

		armoryService.active = nil
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/cancel", ""), http.StatusConflict, "{\"error\":\"run_not_active\"}\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/start", ""), http.StatusConflict, "{\"error\":\"not_a_draft\"}\n")

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/armory/run/"+runID.String(), ""), http.StatusOK, fmt.Sprintf("{\"id\":%q}\n", runID))
		if repo.runs[runID] != nil {
			t.Fatal("inactive run was not deleted")
		}
	})

	t.Run("should return run not active when a run finishes during cancel", func(t *testing.T) {
		run := testServiceArmoryRun(runID, templateID, createdAt)
		run.Status = domain.ArmoryRunInProgress
		repo := &stubArmoryRepository{runs: map[uuid.UUID]*domain.ArmoryRun{runID: run}}
		armoryService := &stubArmoryService{repo: repo, active: []uuid.UUID{runID}}
		armoryService.cancel = func(uuid.UUID) error {
			armoryService.active = nil
			return errors.New("run is no longer active")
		}
		server := newTestServer(&marasi.Proxy{Armory: armoryService}, func() {})

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/cancel", ""), http.StatusConflict, "{\"error\":\"run_not_active\"}\n")
	})

	t.Run("should not delete a run while its start request is in progress", func(t *testing.T) {
		run := testServiceArmoryRun(runID, templateID, createdAt)
		deleteEntered := make(chan struct{})
		repo := &stubArmoryRepository{
			runs: map[uuid.UUID]*domain.ArmoryRun{runID: run},
			beforeDelete: func() {
				close(deleteEntered)
			},
		}
		startEntered := make(chan struct{})
		releaseStart := make(chan struct{})
		armoryService := &stubArmoryService{repo: repo}
		armoryService.start = func(id uuid.UUID) error {
			close(startEntered)
			<-releaseStart
			stored := repo.runs[id]
			if stored == nil {
				stored = testServiceArmoryRun(id, templateID, createdAt)
				repo.runs[id] = stored
			}
			stored.Status = domain.ArmoryRunInProgress
			startedAt := createdAt.Add(time.Minute)
			stored.StartedAt = &startedAt
			armoryService.active = []uuid.UUID{id}
			return nil
		}
		server := newTestServer(&marasi.Proxy{Armory: armoryService}, func() {})
		startResponse := make(chan *httptest.ResponseRecorder, 1)
		deleteResponse := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			startResponse <- requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/start", "")
		}()
		<-startEntered
		go func() {
			deleteResponse <- requestControlAPI(server, http.MethodDelete, "/armory/run/"+runID.String(), "")
		}()

		var deleted *httptest.ResponseRecorder
		select {
		case <-deleteEntered:
			deleted = <-deleteResponse
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseStart)
		started := <-startResponse
		if deleted == nil {
			deleted = <-deleteResponse
		}
		if started.Code != http.StatusOK {
			t.Fatalf("start failed: %d %s", started.Code, started.Body.String())
		}
		assertControlAPIResponse(t, deleted, http.StatusConflict, "{\"error\":\"run_active\"}\n")
	})
}

func TestArmoryRunStatusEvents(t *testing.T) {
	templateID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	runID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
	repo := &statusEventArmoryRepository{run: testServiceArmoryRun(runID, templateID, time.Now())}
	configDir := t.TempDir()
	wordlists, err := wordlist.NewManager(configDir)
	if err != nil {
		t.Fatalf("creating wordlist manager: %v", err)
	}
	if err := os.WriteFile(configDir+"/wordlists/passwords.txt", []byte("secret\n"), 0600); err != nil {
		t.Fatalf("writing wordlist: %v", err)
	}
	releaseSend := make(chan struct{})
	manager, err := armorymanager.NewManager(repo, wordlists, func(context.Context, string, uuid.UUID, bool) error {
		<-releaseSend
		return nil
	})
	if err != nil {
		t.Fatalf("creating Armory manager: %v", err)
	}
	server := newTestServer(&marasi.Proxy{Armory: manager}, func() {})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)

	response := requestControlAPI(server, http.MethodPost, "/armory/run/"+runID.String()+"/start", "")
	if response.Code != http.StatusOK {
		t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
	}
	started := waitForArmoryEvent(t, subscriber.events)
	if started.name != "armory.run.updated" || !strings.Contains(string(started.data), `"status":"in_progress"`) {
		t.Fatalf("\nwanted:\nin-progress armory.run.updated\ngot:\n%s %s", started.name, started.data)
	}
	close(releaseSend)
	completed := waitForArmoryEvent(t, subscriber.events)
	if completed.name != "armory.run.updated" || !strings.Contains(string(completed.data), `"status":"complete"`) {
		t.Fatalf("\nwanted:\ncomplete armory.run.updated\ngot:\n%s %s", completed.name, completed.data)
	}
	select {
	case event := <-subscriber.events:
		t.Fatalf("\nwanted:\nexactly two status events\ngot extra:\n%s", event.name)
	default:
	}
}

type statusEventArmoryRepository struct {
	domain.ArmoryRepository
	mu  sync.Mutex
	run *domain.ArmoryRun
}

func (repo *statusEventArmoryRepository) GetArmoryRun(id uuid.UUID) (*domain.ArmoryRun, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.run == nil || repo.run.ID != id {
		return nil, nil
	}
	copy := *repo.run
	copy.Wordlists = append([]string(nil), repo.run.Wordlists...)
	return &copy, nil
}

func (repo *statusEventArmoryRepository) UpdateArmoryRun(run *domain.ArmoryRun) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	copy := *run
	copy.Wordlists = append([]string(nil), run.Wordlists...)
	repo.run = &copy
	return nil
}

func waitForArmoryEvent(t *testing.T, events <-chan serviceEvent) serviceEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Armory event")
		return serviceEvent{}
	}
}

func testServiceArmoryRun(id, templateID uuid.UUID, createdAt time.Time) *domain.ArmoryRun {
	return &domain.ArmoryRun{
		ID:               id,
		TemplateID:       templateID,
		TemplateSnapshot: "GET /?value=@@value@@ HTTP/1.1\r\nHost: example.com\r\n\r\n",
		UseHTTPS:         true,
		Wordlists:        []string{"passwords.txt"},
		Status:           domain.ArmoryRunDraft,
		AttackType:       domain.ArmoryAttackHarpoon,
		MaxConcurrent:    10,
		CreatedAt:        createdAt,
	}
}

func newArmoryServer(repo domain.ArmoryRepository) *Server {
	proxy := &marasi.Proxy{Armory: &stubArmoryService{repo: repo}}
	return newTestServer(proxy, func() {})
}

func requestControlAPI(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func sendArmoryRequest(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("creating Armory request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("sending Armory request: %v", err)
	}
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
}

func assertControlAPIResponse(t *testing.T, response *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if response.Code != status || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != body {
		t.Fatalf("\nwanted:\n%d application/json %s\ngot:\n%d %s %s", status, body, response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}
