package service

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/wordlist"
)

type wordlistSummary struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type wordlistList struct {
	Items []wordlistSummary `json:"items"`
}

type wordlistPreview struct {
	Name  string   `json:"name"`
	Items []string `json:"items"`
}

func addWordlistRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /wordlist", func(w http.ResponseWriter, r *http.Request) {
		query, ok := parseWordlistQuery(r)
		if !ok || len(query) != 0 {
			writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
			return
		}
		provider, ok := wordlistProvider(proxy)
		if !ok {
			writeWordlistError(w, r, http.StatusNotFound, "not_found")
			return
		}
		infos, err := provider.List()
		if err != nil {
			writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		items := make([]wordlistSummary, 0, len(infos))
		for _, info := range infos {
			items = append(items, wordlistSummary{Name: info.Name, Size: info.Size})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		writeJSON(w, r, http.StatusOK, wordlistList{Items: items})
	})

	mux.HandleFunc("GET /wordlist/", func(w http.ResponseWriter, r *http.Request) {
		writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
	})

	mux.HandleFunc("GET /wordlist/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		limit, ok := parseWordlistPreviewRequest(r, name)
		if !ok {
			writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
			return
		}
		provider, ok := wordlistProvider(proxy)
		if !ok {
			writeWordlistError(w, r, http.StatusNotFound, "not_found")
			return
		}
		iterator, err := provider.Open(name)
		if err != nil {
			if os.IsNotExist(err) {
				writeWordlistError(w, r, http.StatusNotFound, "not_found")
				return
			}
			writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		items := make([]string, 0, limit)
		for len(items) < limit && iterator.Scan() {
			items = append(items, iterator.Text())
		}
		scanErr := iterator.Err()
		closeErr := iterator.Close()
		if scanErr != nil || closeErr != nil {
			writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		writeJSON(w, r, http.StatusOK, wordlistPreview{Name: name, Items: items})
	})
}

func wordlistProvider(proxy *marasi.Proxy) (wordlist.Provider, bool) {
	if proxy == nil {
		return nil, false
	}
	provider, err := proxy.GetWordlistManager()
	return provider, err == nil
}

func parseWordlistPreviewRequest(r *http.Request, name string) (int, bool) {
	if !validWordlistName(name) {
		return 0, false
	}
	query, ok := parseWordlistQuery(r)
	if !ok {
		return 0, false
	}
	if len(query) == 0 {
		return 20, true
	}
	values, ok := query["limit"]
	if !ok || len(query) != 1 || len(values) != 1 || values[0] == "" {
		return 0, false
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || limit < 1 || limit > 100 {
		return 0, false
	}
	return limit, true
}

func parseWordlistQuery(r *http.Request) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	return query, err == nil
}

func invalidWordlistPath(path string) bool {
	if !strings.HasPrefix(path, "/wordlist/") {
		return false
	}
	return !validWordlistName(strings.TrimPrefix(path, "/wordlist/"))
}

func validWordlistName(name string) bool {
	return name != "" && filepath.IsLocal(name) && filepath.Base(name) == name
}

func writeWordlistError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
