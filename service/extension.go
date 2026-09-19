package service

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/extensions"
)

type extensionSummary struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Enabled     bool      `json:"enabled"`
	Author      string    `json:"author"`
	Description string    `json:"description"`
	SourceURL   string    `json:"source_url"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type extensionList struct {
	Items []extensionSummary `json:"items"`
}

type extensionDetail struct {
	extensionSummary
	LuaContent string         `json:"lua_content"`
	Settings   map[string]any `json:"settings"`
}

type extensionLogItem struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

type extensionLogs struct {
	Items []extensionLogItem `json:"items"`
}

type extensionUpdateRequest struct {
	LuaContent *string `json:"lua_content"`
}

var errInvalidExtensionRequest = errors.New("invalid extension request")

func addExtensionRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /extension", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusOK, extensionList{Items: listExtensionSummaries(proxy)})
	})

	mux.HandleFunc("GET /extension/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseExtensionID(w, r)
		if !ok {
			return
		}
		runtime := findExtensionRuntime(proxy, id)
		if runtime == nil {
			writeExtensionError(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, r, http.StatusOK, extensionDetailFromRuntime(runtime))
	})

	mux.HandleFunc("GET /extension/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseExtensionID(w, r)
		if !ok {
			return
		}
		runtime := findExtensionRuntime(proxy, id)
		if runtime == nil {
			writeExtensionError(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, r, http.StatusOK, extensionLogsFromRuntime(runtime))
	})

	mux.HandleFunc("POST /extension/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseExtensionID(w, r)
		if !ok {
			return
		}
		runtime := findExtensionRuntime(proxy, id)
		if runtime == nil {
			writeExtensionError(w, r, http.StatusNotFound, "not_found")
			return
		}
		lua, err := decodeExtensionUpdate(r)
		if err != nil {
			writeExtensionError(w, r, http.StatusBadRequest, "invalid_extension_request")
			return
		}
		repo, err := proxy.GetExtensionRepo()
		if err != nil {
			writeExtensionError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		if err := repo.UpdateExtensionLuaCodeByUUID(id, lua); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeExtensionError(w, r, http.StatusNotFound, "not_found")
				return
			}
			writeExtensionError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		stored, err := repo.GetExtensionByUUID(id)
		if err != nil {
			writeExtensionError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		runtime.Data.LuaContent = stored.LuaContent
		runtime.Data.Enabled = stored.Enabled
		runtime.Data.UpdatedAt = stored.UpdatedAt
		events.publish("extension.updated", extensionSummaryFromRuntime(runtime))
		if err := runtime.ExecuteLua(lua); err != nil {
			writeExtensionError(w, r, http.StatusBadRequest, "lua_error")
			return
		}
		writeJSON(w, r, http.StatusOK, extensionDetailFromRuntime(runtime))
	})
}

func listExtensionSummaries(proxy *marasi.Proxy) []extensionSummary {
	items := make([]extensionSummary, 0, len(proxy.Extensions))
	for _, runtime := range proxy.Extensions {
		items = append(items, extensionSummaryFromRuntime(runtime))
	}
	slices.SortFunc(items, func(a, b extensionSummary) int {
		return cmp.Compare(a.ID.String(), b.ID.String())
	})
	return items
}

func extensionSummaryFromRuntime(runtime *extensions.Runtime) extensionSummary {
	return extensionSummary{
		ID:          runtime.Data.ID,
		Name:        runtime.Data.Name,
		Enabled:     runtime.Data.Enabled,
		Author:      runtime.Data.Author,
		Description: runtime.Data.Description,
		SourceURL:   runtime.Data.SourceURL,
		UpdatedAt:   runtime.Data.UpdatedAt,
	}
}

func extensionDetailFromRuntime(runtime *extensions.Runtime) extensionDetail {
	settings := runtime.Data.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	return extensionDetail{
		extensionSummary: extensionSummaryFromRuntime(runtime),
		LuaContent:       runtime.Data.LuaContent,
		Settings:         settings,
	}
}

func extensionLogsFromRuntime(runtime *extensions.Runtime) extensionLogs {
	items := make([]extensionLogItem, 0, len(runtime.Logs))
	for _, entry := range runtime.Logs {
		items = append(items, extensionLogItem{Time: entry.Time, Text: entry.Text})
	}
	return extensionLogs{Items: items}
}

func findExtensionRuntime(proxy *marasi.Proxy, id uuid.UUID) *extensions.Runtime {
	for _, runtime := range proxy.Extensions {
		if runtime.Data.ID == id {
			return runtime
		}
	}
	return nil
}

func decodeExtensionUpdate(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", errInvalidExtensionRequest
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request extensionUpdateRequest
	if err := decoder.Decode(&request); err != nil || request.LuaContent == nil {
		return "", errInvalidExtensionRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errInvalidExtensionRequest
	}
	return *request.LuaContent, nil
}

func parseExtensionID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeExtensionError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func writeExtensionError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
