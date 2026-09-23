package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type armoryTemplate struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	RawTemplate string    `json:"raw_template"`
}

type armoryTemplateSummary struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
}

type armoryTemplateList struct {
	Items []armoryTemplateSummary `json:"items"`
}

type armoryID struct {
	ID uuid.UUID `json:"id"`
}

type armoryTemplateMutation struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	RawTemplate *string `json:"raw_template"`
}

type armoryRun struct {
	ID               uuid.UUID               `json:"id"`
	TemplateID       uuid.UUID               `json:"template_id"`
	TemplateSnapshot string                  `json:"template_snapshot"`
	UseHTTPS         bool                    `json:"use_https"`
	Wordlists        []string                `json:"wordlists"`
	Status           domain.ArmoryRunStatus  `json:"status"`
	AttackType       domain.ArmoryAttackType `json:"attack_type"`
	MaxConcurrent    int                     `json:"max_concurrent"`
	CreatedAt        time.Time               `json:"created_at"`
	StartedAt        *time.Time              `json:"started_at"`
	FinishedAt       *time.Time              `json:"finished_at"`
}

type armoryRunList struct {
	Items []armoryRun `json:"items"`
}

type armoryRunMutation struct {
	TemplateID    *uuid.UUID
	RawTemplate   *string
	AttackType    domain.ArmoryAttackType
	Wordlists     []string
	UseHTTPS      bool
	MaxConcurrent int
}

var errInvalidArmoryRequest = errors.New("invalid armory request")

func addArmoryRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	var runMutationMu sync.Mutex
	mux.HandleFunc("GET /armory/template", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := armoryRepository(proxy)
		if !ok {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		templates, err := repo.GetArmoryTemplates()
		if err != nil {
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		items := make([]armoryTemplateSummary, 0, len(templates))
		for _, template := range templates {
			items = append(items, armoryTemplateSummary{
				ID: template.ID, Name: template.Name, Description: template.Description,
			})
		}
		writeJSON(w, r, http.StatusOK, armoryTemplateList{Items: items})
	})

	mux.HandleFunc("POST /armory/template", func(w http.ResponseWriter, r *http.Request) {
		mutation, err := decodeArmoryTemplateMutation(r, true)
		if err != nil {
			writeArmoryError(w, r, http.StatusBadRequest, "invalid_armory_request")
			return
		}
		repo, ok := armoryRepository(proxy)
		if !ok {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		template := &domain.ArmoryTemplate{ID: id, Name: *mutation.Name}
		if mutation.Description != nil {
			template.Description = *mutation.Description
		}
		if mutation.RawTemplate != nil {
			template.RawTemplate = *mutation.RawTemplate
		}
		if err := repo.CreateArmoryTemplate(template); err != nil {
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response := armoryTemplateFromDomain(template)
		events.publish("armory.template.created", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("GET /armory/template/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		repo, ok := armoryRepository(proxy)
		if !ok {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		template, err := repo.GetArmoryTemplate(id)
		if err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		if template == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, r, http.StatusOK, armoryTemplateFromDomain(template))
	})

	mux.HandleFunc("POST /armory/template/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		mutation, err := decodeArmoryTemplateMutation(r, false)
		if err != nil {
			writeArmoryError(w, r, http.StatusBadRequest, "invalid_armory_request")
			return
		}
		repo, ok := armoryRepository(proxy)
		if !ok {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		template, err := repo.GetArmoryTemplate(id)
		if err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		if template == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if mutation.Name != nil {
			template.Name = *mutation.Name
		}
		if mutation.Description != nil {
			template.Description = *mutation.Description
		}
		if mutation.RawTemplate != nil {
			template.RawTemplate = *mutation.RawTemplate
		}
		if err := repo.UpdateArmoryTemplate(template); err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		response := armoryTemplateFromDomain(template)
		events.publish("armory.template.updated", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /armory/template/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		runMutationMu.Lock()
		defer runMutationMu.Unlock()
		repo, ok := armoryRepository(proxy)
		if !ok {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		template, err := repo.GetArmoryTemplate(id)
		if err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		if template == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		for _, runID := range proxy.Armory.ActiveRunIDs() {
			run, err := repo.GetArmoryRun(runID)
			if err != nil {
				writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
				return
			}
			if run != nil && run.TemplateID == id {
				writeArmoryError(w, r, http.StatusConflict, "template_has_active_run")
				return
			}
		}
		if err := repo.DeleteArmoryTemplate(id); err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		response := armoryID{ID: id}
		events.publish("armory.template.deleted", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("GET /armory/template/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		repo, ok := armoryRepository(proxy)
		if !ok {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetArmoryTemplate(id); err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		runs, err := repo.GetArmoryRuns(id)
		if err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		items := make([]armoryRun, 0, len(runs))
		for _, run := range runs {
			items = append(items, armoryRunFromDomain(run))
		}
		writeJSON(w, r, http.StatusOK, armoryRunList{Items: items})
	})

	mux.HandleFunc("POST /armory/run", func(w http.ResponseWriter, r *http.Request) {
		mutation, err := decodeArmoryRunMutation(r, true)
		if err != nil {
			writeArmoryError(w, r, http.StatusBadRequest, "invalid_armory_request")
			return
		}
		armoryService, ok := armoryService(proxy)
		if !ok {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		repo := armoryService.Repo()
		if repo == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		template, err := repo.GetArmoryTemplate(*mutation.TemplateID)
		if err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		run := mutation.armoryRun(id, template.RawTemplate)
		if err := armoryService.ValidateRun(run); err != nil {
			writeArmoryError(w, r, http.StatusBadRequest, "invalid_armory_request")
			return
		}
		if err := repo.CreateArmoryRun(run); err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		response := armoryRunFromDomain(run)
		events.publish("armory.run.created", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /armory/run/validate", func(w http.ResponseWriter, r *http.Request) {
		mutation, err := decodeArmoryRunMutation(r, false)
		if err != nil {
			writeArmoryError(w, r, http.StatusBadRequest, "invalid_armory_request")
			return
		}
		armoryService, ok := armoryService(proxy)
		if !ok || armoryService.Repo() == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		run := mutation.armoryRun(uuid.Nil, *mutation.RawTemplate)
		if err := armoryService.ValidateRun(run); err != nil {
			writeArmoryError(w, r, http.StatusBadRequest, "invalid_armory_request")
			return
		}
		writeJSON(w, r, http.StatusOK, struct {
			Status string `json:"status"`
		}{Status: "ok"})
	})

	mux.HandleFunc("GET /armory/run/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		run, ok := getArmoryRun(w, r, proxy, id)
		if !ok {
			return
		}
		writeJSON(w, r, http.StatusOK, armoryRunFromDomain(run))
	})

	mux.HandleFunc("GET /armory/run/{id}/traffic", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		limit, cursor, ok := parseArmoryRunTrafficQuery(r)
		if !ok {
			writeArmoryError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		if _, ok := getArmoryRun(w, r, proxy, id); !ok {
			return
		}
		repo, _ := armoryRepository(proxy)
		items, nextCursor, err := repo.ListArmoryRunTraffic(id, cursor, limit)
		if err != nil {
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		writeJSON(w, r, http.StatusOK, trafficListFromSummaries(items, nextCursor))
	})

	mux.HandleFunc("POST /armory/run/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		runMutationMu.Lock()
		defer runMutationMu.Unlock()
		armoryService, ok := armoryService(proxy)
		if !ok || armoryService.Repo() == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		run, ok := getArmoryRun(w, r, proxy, id)
		if !ok {
			return
		}
		if armoryRunIsActive(armoryService, id) {
			writeArmoryError(w, r, http.StatusConflict, "run_active")
			return
		}
		if run.Status != domain.ArmoryRunDraft {
			writeArmoryError(w, r, http.StatusConflict, "not_a_draft")
			return
		}
		if err := armoryService.StartRun(id); err != nil {
			if armoryRunIsActive(armoryService, id) {
				writeArmoryError(w, r, http.StatusConflict, "run_active")
				return
			}
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		run, ok = getArmoryRun(w, r, proxy, id)
		if !ok {
			return
		}
		run.Status = domain.ArmoryRunInProgress
		run.FinishedAt = nil
		writeJSON(w, r, http.StatusOK, armoryRunFromDomain(run))
	})

	mux.HandleFunc("POST /armory/run/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		runMutationMu.Lock()
		defer runMutationMu.Unlock()
		armoryService, ok := armoryService(proxy)
		if !ok || armoryService.Repo() == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, ok := getArmoryRun(w, r, proxy, id); !ok {
			return
		}
		if !armoryRunIsActive(armoryService, id) {
			writeArmoryError(w, r, http.StatusConflict, "run_not_active")
			return
		}
		if err := armoryService.CancelRun(id); err != nil {
			if !armoryRunIsActive(armoryService, id) {
				writeArmoryError(w, r, http.StatusConflict, "run_not_active")
				return
			}
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		run, ok := getArmoryRun(w, r, proxy, id)
		if !ok {
			return
		}
		writeJSON(w, r, http.StatusOK, armoryRunFromDomain(run))
	})

	mux.HandleFunc("DELETE /armory/run/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArmoryID(w, r)
		if !ok {
			return
		}
		runMutationMu.Lock()
		defer runMutationMu.Unlock()
		armoryService, ok := armoryService(proxy)
		if !ok || armoryService.Repo() == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, ok := getArmoryRun(w, r, proxy, id); !ok {
			return
		}
		if armoryRunIsActive(armoryService, id) {
			writeArmoryError(w, r, http.StatusConflict, "run_active")
			return
		}
		if err := armoryService.Repo().DeleteArmoryRun(id); err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		response := armoryID{ID: id}
		events.publish("armory.run.deleted", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}

func armoryService(proxy *marasi.Proxy) (marasi.ArmoryService, bool) {
	if proxy == nil {
		return nil, false
	}
	service, err := proxy.GetArmory()
	return service, err == nil && service != nil
}

func armoryRepository(proxy *marasi.Proxy) (domain.ArmoryRepository, bool) {
	armory, ok := armoryService(proxy)
	if !ok {
		return nil, false
	}
	repo := armory.Repo()
	return repo, repo != nil
}

func decodeArmoryRunMutation(r *http.Request, create bool) (armoryRunMutation, error) {
	if r.Body == nil {
		return armoryRunMutation{}, errInvalidArmoryRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return armoryRunMutation{}, errInvalidArmoryRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return armoryRunMutation{}, errInvalidArmoryRequest
	}
	mutation := armoryRunMutation{UseHTTPS: true, MaxConcurrent: 10}
	var attackTypeSet, wordlistsSet bool
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return armoryRunMutation{}, errInvalidArmoryRequest
		}
		switch name {
		case "template_id":
			var value uuid.UUID
			if !create || json.Unmarshal(raw, &value) != nil || value == uuid.Nil {
				return armoryRunMutation{}, errInvalidArmoryRequest
			}
			mutation.TemplateID = &value
		case "raw_template":
			var value string
			if create || json.Unmarshal(raw, &value) != nil {
				return armoryRunMutation{}, errInvalidArmoryRequest
			}
			mutation.RawTemplate = &value
		case "attack_type":
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return armoryRunMutation{}, errInvalidArmoryRequest
			}
			attackType, ok := domain.ParseArmoryAttackType(value)
			if !ok {
				return armoryRunMutation{}, errInvalidArmoryRequest
			}
			mutation.AttackType = attackType
			attackTypeSet = true
		case "wordlists":
			if json.Unmarshal(raw, &mutation.Wordlists) != nil || mutation.Wordlists == nil {
				return armoryRunMutation{}, errInvalidArmoryRequest
			}
			wordlistsSet = true
		case "use_https":
			if json.Unmarshal(raw, &mutation.UseHTTPS) != nil {
				return armoryRunMutation{}, errInvalidArmoryRequest
			}
		case "max_concurrent":
			if json.Unmarshal(raw, &mutation.MaxConcurrent) != nil || mutation.MaxConcurrent < 1 || mutation.MaxConcurrent > 100 {
				return armoryRunMutation{}, errInvalidArmoryRequest
			}
		default:
			return armoryRunMutation{}, errInvalidArmoryRequest
		}
	}
	if create && mutation.TemplateID == nil || !create && mutation.RawTemplate == nil || !attackTypeSet || !wordlistsSet {
		return armoryRunMutation{}, errInvalidArmoryRequest
	}
	return mutation, nil
}

func (mutation armoryRunMutation) armoryRun(id uuid.UUID, snapshot string) *domain.ArmoryRun {
	return &domain.ArmoryRun{
		ID:               id,
		TemplateID:       valueOrZero(mutation.TemplateID),
		TemplateSnapshot: snapshot,
		UseHTTPS:         mutation.UseHTTPS,
		Wordlists:        mutation.Wordlists,
		Status:           domain.ArmoryRunDraft,
		AttackType:       mutation.AttackType,
		MaxConcurrent:    mutation.MaxConcurrent,
		CreatedAt:        time.Now(),
	}
}

func valueOrZero(value *uuid.UUID) uuid.UUID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}

func getArmoryRun(w http.ResponseWriter, r *http.Request, proxy *marasi.Proxy, id uuid.UUID) (*domain.ArmoryRun, bool) {
	repo, ok := armoryRepository(proxy)
	if !ok {
		writeArmoryError(w, r, http.StatusNotFound, "not_found")
		return nil, false
	}
	run, err := repo.GetArmoryRun(id)
	if err != nil {
		writeArmoryRepositoryError(w, r, err)
		return nil, false
	}
	if run == nil {
		writeArmoryError(w, r, http.StatusNotFound, "not_found")
		return nil, false
	}
	return run, true
}

func armoryRunIsActive(service marasi.ArmoryService, id uuid.UUID) bool {
	for _, activeID := range service.ActiveRunIDs() {
		if activeID == id {
			return true
		}
	}
	return false
}

func decodeArmoryTemplateMutation(r *http.Request, requireName bool) (armoryTemplateMutation, error) {
	if r.Body == nil {
		return armoryTemplateMutation{}, errInvalidArmoryRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return armoryTemplateMutation{}, errInvalidArmoryRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return armoryTemplateMutation{}, errInvalidArmoryRequest
	}
	var mutation armoryTemplateMutation
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return armoryTemplateMutation{}, errInvalidArmoryRequest
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return armoryTemplateMutation{}, errInvalidArmoryRequest
		}
		switch name {
		case "name":
			mutation.Name = &value
		case "description":
			mutation.Description = &value
		case "raw_template":
			mutation.RawTemplate = &value
		default:
			return armoryTemplateMutation{}, errInvalidArmoryRequest
		}
	}
	if requireName && mutation.Name == nil || mutation.Name != nil && strings.TrimSpace(*mutation.Name) == "" {
		return armoryTemplateMutation{}, errInvalidArmoryRequest
	}
	return mutation, nil
}

func armoryTemplateFromDomain(template *domain.ArmoryTemplate) armoryTemplate {
	return armoryTemplate{
		ID:          template.ID,
		Name:        template.Name,
		Description: template.Description,
		RawTemplate: template.RawTemplate,
	}
}

func armoryRunFromDomain(run *domain.ArmoryRun) armoryRun {
	wordlists := append([]string(nil), run.Wordlists...)
	if wordlists == nil {
		wordlists = make([]string, 0)
	}
	return armoryRun{
		ID:               run.ID,
		TemplateID:       run.TemplateID,
		TemplateSnapshot: run.TemplateSnapshot,
		UseHTTPS:         run.UseHTTPS,
		Wordlists:        wordlists,
		Status:           run.Status,
		AttackType:       run.AttackType,
		MaxConcurrent:    run.MaxConcurrent,
		CreatedAt:        run.CreatedAt,
		StartedAt:        run.StartedAt,
		FinishedAt:       run.FinishedAt,
	}
}

func parseArmoryID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeArmoryError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func parseArmoryRunTrafficQuery(r *http.Request) (int, *uuid.UUID, bool) {
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			return 0, nil, false
		}
		limit = parsed
	}
	var cursor *uuid.UUID
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return 0, nil, false
		}
		cursor = &parsed
	}
	return limit, cursor, true
}

func writeArmoryError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeArmoryRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrArmoryTemplateNotFound) || errors.Is(err, domain.ErrArmoryRunNotFound) {
		writeArmoryError(w, r, http.StatusNotFound, "not_found")
		return
	}
	writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
}
