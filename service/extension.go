package service

import (
	"cmp"
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

func addExtensionRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
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

func findExtensionRuntime(proxy *marasi.Proxy, id uuid.UUID) *extensions.Runtime {
	for _, runtime := range proxy.Extensions {
		if runtime.Data.ID == id {
			return runtime
		}
	}
	return nil
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
