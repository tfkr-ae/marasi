package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type stubArmoryRepository struct {
	domain.ArmoryRepository
	templates map[uuid.UUID]*domain.ArmoryTemplate
	runs      map[uuid.UUID]*domain.ArmoryRun
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

type stubArmoryService struct {
	marasi.ArmoryService
	repo   domain.ArmoryRepository
	active []uuid.UUID
}

func (service *stubArmoryService) Repo() domain.ArmoryRepository { return service.repo }
func (service *stubArmoryService) ActiveRunIDs() []uuid.UUID     { return service.active }

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
