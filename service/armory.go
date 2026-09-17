package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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

var errInvalidArmoryRequest = errors.New("invalid armory request")

func addArmoryRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
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
}

func armoryRepository(proxy *marasi.Proxy) (domain.ArmoryRepository, bool) {
	if proxy == nil {
		return nil, false
	}
	armory, err := proxy.GetArmory()
	if err != nil {
		return nil, false
	}
	repo := armory.Repo()
	return repo, repo != nil
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

func parseArmoryID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeArmoryError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func writeArmoryError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeArmoryRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrArmoryTemplateNotFound) {
		writeArmoryError(w, r, http.StatusNotFound, "not_found")
		return
	}
	writeArmoryError(w, r, http.StatusInternalServerError, "internal_server_error")
}
