package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type noteBody struct {
	ID   uuid.UUID `json:"id"`
	Note string    `json:"note"`
}

type noteList struct {
	Items      []noteListItem `json:"items"`
	NextCursor *uuid.UUID     `json:"next_cursor"`
}

type noteListItem struct {
	trafficSummary
	Note string `json:"note"`
}

var errInvalidNoteRequest = errors.New("invalid note request")

func addNoteRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /notes", func(w http.ResponseWriter, r *http.Request) {
		limit, cursor, ok := parseNotesListQuery(r)
		if !ok {
			writeNoteError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeNoteError(w, r, http.StatusNotFound, "not_found")
			return
		}
		items, nextCursor, err := repo.ListNotes(cursor, limit)
		if err != nil {
			writeNoteError(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, r, http.StatusOK, noteListFromSummaries(items, nextCursor))
	})
	mux.HandleFunc("PUT /notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseNoteID(w, r)
		if !ok {
			return
		}
		note, err := decodeNoteBody(r)
		if err != nil {
			writeNoteError(w, r, http.StatusBadRequest, "invalid_note_request")
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeNoteError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetRequestResponseRow(id); err != nil {
			writeNoteError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := repo.UpdateNote(id, note); err != nil {
			writeNoteError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response := noteBody{ID: id, Note: note}
		events.publish("note.updated", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseNoteID(w, r)
		if !ok {
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeNoteError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetRequestResponseRow(id); err != nil {
			writeNoteError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetNote(id); err != nil {
			writeNoteError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := repo.DeleteNote(id); err != nil {
			writeNoteError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response := struct {
			ID uuid.UUID `json:"id"`
		}{ID: id}
		events.publish("note.deleted", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}

func parseNotesListQuery(r *http.Request) (int, *uuid.UUID, bool) {
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

func noteListFromSummaries(items []*domain.RequestResponseSummary, nextCursor *uuid.UUID) noteList {
	list := make([]noteListItem, 0, len(items))
	for _, item := range items {
		list = append(list, noteListItem{
			trafficSummary: trafficSummaryFromDomain(item),
			Note:           item.Note,
		})
	}
	return noteList{Items: list, NextCursor: nextCursor}
}

func parseNoteID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeNoteError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func decodeNoteBody(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", errInvalidNoteRequest
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		Note *string `json:"note"`
	}
	if err := decoder.Decode(&body); err != nil {
		return "", errInvalidNoteRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errInvalidNoteRequest
	}
	if body.Note == nil || *body.Note == "" {
		return "", errInvalidNoteRequest
	}
	return *body.Note, nil
}

func writeNoteError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
