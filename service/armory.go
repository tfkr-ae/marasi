package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
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

func addArmoryRoutes(mux routeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
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

	mux.HandleFunc("GET /armory/run", func(w http.ResponseWriter, r *http.Request) {
		statuses, err := parseArmoryRunStatusFilter(r)
		if err != nil {
			writeRefusal(w, r, http.StatusBadRequest, "invalid_armory_request", err.Error())
			return
		}
		armoryService, ok := armoryService(proxy)
		if !ok || armoryService.Repo() == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		runs, err := listProjectArmoryRuns(armoryService)
		if err != nil {
			writeArmoryRepositoryError(w, r, err)
			return
		}
		items := make([]armoryRun, 0, len(runs))
		for _, run := range runs {
			if len(statuses) == 0 || statuses[run.Status] {
				items = append(items, armoryRunFromDomain(run))
			}
		}
		writeJSON(w, r, http.StatusOK, armoryRunList{Items: items})
	})

	mux.HandleFunc("POST /armory/run", func(w http.ResponseWriter, r *http.Request) {
		mutation, err := decodeArmoryRunMutation(r, true)
		if err != nil {
			writeRefusal(w, r, http.StatusBadRequest, "invalid_armory_request", err.Error())
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
			writeRefusal(w, r, http.StatusBadRequest, "invalid_armory_request", err.Error())
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
			writeRefusal(w, r, http.StatusBadRequest, "invalid_armory_request", err.Error())
			return
		}
		armoryService, ok := armoryService(proxy)
		if !ok || armoryService.Repo() == nil {
			writeArmoryError(w, r, http.StatusNotFound, "not_found")
			return
		}
		run := mutation.armoryRun(uuid.Nil, *mutation.RawTemplate)
		if err := armoryService.ValidateRun(run); err != nil {
			writeRefusal(w, r, http.StatusBadRequest, "invalid_armory_request", err.Error())
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
		items, nextCursor, indexComplete, err := repo.ListArmoryRunTraffic(id, cursor, limit)
		if err != nil {
			writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		list := trafficListFromSummaries(items, nextCursor)
		list.Index = &trafficIndex{Complete: indexComplete}
		writeJSON(w, r, http.StatusOK, list)
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

// parseArmoryRunStatusFilter reads every status query value. Each value may
// hold several comma-separated statuses. No status parameter means no filter.
func parseArmoryRunStatusFilter(r *http.Request) (map[domain.ArmoryRunStatus]bool, error) {
	values, present := r.URL.Query()["status"]
	if !present {
		return nil, nil
	}
	statuses := make(map[domain.ArmoryRunStatus]bool)
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			status := domain.ArmoryRunStatus(name)
			switch status {
			case domain.ArmoryRunDraft, domain.ArmoryRunInProgress, domain.ArmoryRunCompleted, domain.ArmoryRunFailed, domain.ArmoryRunCancelled:
				statuses[status] = true
			default:
				return nil, fmt.Errorf("status %q is not draft, in_progress, complete, failed or cancelled", name)
			}
		}
	}
	return statuses, nil
}

// listProjectArmoryRuns returns every run in the project, oldest first, with
// in_progress reported for exactly the runs in one ActiveRunIDs snapshot.
//
// The rows are read before the snapshot. A run starts by joining the active
// set before its in_progress write, and finishes by storing its final status
// before it leaves the set. So a run read as in_progress but missing from the
// snapshot has finished, and is read again for its final status. A run in the
// snapshot that the rows missed was created after they were read, and is read
// on its own.
func listProjectArmoryRuns(service marasi.ArmoryService) ([]*domain.ArmoryRun, error) {
	repo := service.Repo()
	templates, err := repo.GetArmoryTemplates()
	if err != nil {
		return nil, err
	}
	var runs []*domain.ArmoryRun
	for _, template := range templates {
		templateRuns, err := repo.GetArmoryRuns(template.ID)
		if err != nil {
			return nil, err
		}
		runs = append(runs, templateRuns...)
	}

	active := make(map[uuid.UUID]bool)
	for _, id := range service.ActiveRunIDs() {
		active[id] = true
	}

	result := make([]*domain.ArmoryRun, 0, len(runs)+len(active))
	seen := make(map[uuid.UUID]bool, len(runs))
	for _, run := range runs {
		seen[run.ID] = true
		if !active[run.ID] && run.Status == domain.ArmoryRunInProgress {
			current, err := repo.GetArmoryRun(run.ID)
			if errors.Is(err, domain.ErrArmoryRunNotFound) || (err == nil && current == nil) {
				continue
			}
			if err != nil {
				return nil, err
			}
			run = current
		}
		result = append(result, run)
	}
	for id := range active {
		if seen[id] {
			continue
		}
		run, err := repo.GetArmoryRun(id)
		if errors.Is(err, domain.ErrArmoryRunNotFound) || (err == nil && run == nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	for _, run := range result {
		if active[run.ID] {
			run.Status = domain.ArmoryRunInProgress
		}
	}
	slices.SortFunc(result, func(a, b *domain.ArmoryRun) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	return result, nil
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

// decodeArmoryRunMutation reads a run draft. Its errors are the refusal
// message, naming the first problem in field-name order so the answer does
// not depend on map order.
func decodeArmoryRunMutation(r *http.Request, create bool) (armoryRunMutation, error) {
	errNotObject := errors.New("the body must be one JSON object")
	if r.Body == nil {
		return armoryRunMutation{}, errNotObject
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return armoryRunMutation{}, errNotObject
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return armoryRunMutation{}, errNotObject
	}
	mutation := armoryRunMutation{UseHTTPS: true, MaxConcurrent: 10}
	var attackTypeSet, wordlistsSet bool
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		raw := fields[name]
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return armoryRunMutation{}, fmt.Errorf("%s must not be null", name)
		}
		switch name {
		case "template_id":
			if !create {
				return armoryRunMutation{}, errors.New("template_id is not accepted here; send raw_template")
			}
			var value uuid.UUID
			if json.Unmarshal(raw, &value) != nil || value == uuid.Nil {
				return armoryRunMutation{}, errors.New("template_id must be an Armory template UUID")
			}
			mutation.TemplateID = &value
		case "raw_template":
			if create {
				return armoryRunMutation{}, errors.New("raw_template is not accepted here; send template_id")
			}
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return armoryRunMutation{}, errors.New("raw_template must be a string")
			}
			mutation.RawTemplate = &value
		case "attack_type":
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return armoryRunMutation{}, errors.New("attack_type must be a string")
			}
			attackType, ok := domain.ParseArmoryAttackType(value)
			if !ok {
				return armoryRunMutation{}, fmt.Errorf("attack_type %q is not harpoon, broadside, tandem or maelstrom", value)
			}
			mutation.AttackType = attackType
			attackTypeSet = true
		case "wordlists":
			if json.Unmarshal(raw, &mutation.Wordlists) != nil || mutation.Wordlists == nil {
				return armoryRunMutation{}, errors.New("wordlists must be an array of wordlist names")
			}
			wordlistsSet = true
		case "use_https":
			if json.Unmarshal(raw, &mutation.UseHTTPS) != nil {
				return armoryRunMutation{}, errors.New("use_https must be true or false")
			}
		case "max_concurrent":
			if json.Unmarshal(raw, &mutation.MaxConcurrent) != nil || mutation.MaxConcurrent < 1 || mutation.MaxConcurrent > 100 {
				return armoryRunMutation{}, errors.New("max_concurrent must be a whole number from 1 to 100")
			}
		default:
			return armoryRunMutation{}, fmt.Errorf("unknown field %q", name)
		}
	}
	switch {
	case create && mutation.TemplateID == nil:
		return armoryRunMutation{}, errors.New("template_id is required")
	case !create && mutation.RawTemplate == nil:
		return armoryRunMutation{}, errors.New("raw_template is required")
	case !attackTypeSet:
		return armoryRunMutation{}, errors.New("attack_type is required")
	case !wordlistsSet:
		return armoryRunMutation{}, errors.New("wordlists is required")
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
