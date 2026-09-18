package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
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

func addWordlistRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
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
		response, err := listWordlists(provider)
		if err != nil {
			writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /wordlist", func(w http.ResponseWriter, r *http.Request) {
		path, ok := parseWordlistAddRequest(r)
		if !ok {
			writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
			return
		}
		provider, ok := wordlistProvider(proxy)
		if !ok {
			writeWordlistError(w, r, http.StatusNotFound, "not_found")
			return
		}
		manager, ok := provider.(interface{ Add(string) error })
		if !ok {
			writeWordlistError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := manager.Add(path); err != nil {
			switch {
			case errors.Is(err, wordlist.ErrAlreadyExists):
				writeWordlistError(w, r, http.StatusConflict, "wordlist_already_exists")
			case errors.Is(err, wordlist.ErrInvalidSource):
				writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
			case errors.Is(err, os.ErrNotExist):
				writeWordlistError(w, r, http.StatusNotFound, "not_found")
			default:
				writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			}
			return
		}
		response, err := listWordlists(provider)
		if err != nil {
			writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		events.publish("wordlist.added", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /wordlist/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		query, ok := parseWordlistQuery(r)
		if !ok || len(query) != 0 || !wordlist.ValidName(name) {
			writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			writeWordlistError(w, r, http.StatusBadRequest, "invalid_wordlist_request")
			return
		}
		provider, ok := wordlistProvider(proxy)
		if !ok {
			writeWordlistError(w, r, http.StatusNotFound, "not_found")
			return
		}
		manager, ok := provider.(interface{ Remove(string) error })
		if !ok {
			writeWordlistError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err = manager.Remove(name); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeWordlistError(w, r, http.StatusNotFound, "not_found")
			} else {
				writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			}
			return
		}
		response, err := listWordlists(provider)
		if err != nil {
			writeWordlistError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		events.publish("wordlist.removed", response)
		writeJSON(w, r, http.StatusOK, response)
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

func listWordlists(provider wordlist.Provider) (wordlistList, error) {
	infos, err := provider.List()
	if err != nil {
		return wordlistList{}, err
	}
	items := make([]wordlistSummary, 0, len(infos))
	for _, info := range infos {
		items = append(items, wordlistSummary{Name: info.Name, Size: info.Size})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return wordlistList{Items: items}, nil
}

func parseWordlistAddRequest(r *http.Request) (string, bool) {
	query, ok := parseWordlistQuery(r)
	if !ok || len(query) != 0 {
		return "", false
	}
	var request struct {
		Path string `json:"path"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Path == "" {
		return "", false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", false
	}
	return request.Path, true
}

func wordlistProvider(proxy *marasi.Proxy) (wordlist.Provider, bool) {
	if proxy == nil {
		return nil, false
	}
	provider, err := proxy.GetWordlistManager()
	return provider, err == nil
}

func parseWordlistPreviewRequest(r *http.Request, name string) (int, bool) {
	if !wordlist.ValidName(name) {
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
	return !wordlist.ValidName(strings.TrimPrefix(path, "/wordlist/"))
}

func writeWordlistError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
