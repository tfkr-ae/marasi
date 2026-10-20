package service

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type clientSettingList struct {
	Items []domain.ClientSetting `json:"items"`
}

type clientSettingAddress struct {
	Client string `json:"client"`
	Key    string `json:"key"`
}

var errInvalidClientSettingBody = errors.New("invalid client setting body")

// addClientSettingRoutes registers the client setting store (docs/adr/2026-10-14-client-settings-are-opaque.md).
// Values are opaque JSON; Marasi validates only their shape.
func addClientSettingRoutes(mux routeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /client-settings/{client}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := clientSettingRepo(w, r, proxy)
		if !ok {
			return
		}
		settings, err := repo.ListClientSettings(r.PathValue("client"), r.URL.Query().Get("prefix"))
		if err != nil {
			writeClientSettingError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, clientSettingList{Items: settings})
	})
	mux.HandleFunc("GET /client-settings/{client}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := clientSettingRepo(w, r, proxy)
		if !ok {
			return
		}
		setting, err := repo.GetClientSetting(r.PathValue("client"), r.PathValue("key"))
		if err != nil {
			writeClientSettingError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, setting)
	})
	mux.HandleFunc("PUT /client-settings/{client}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := clientSettingRepo(w, r, proxy)
		if !ok {
			return
		}
		value, err := decodeClientSettingBody(w, r)
		if err != nil {
			writeClientSettingError(w, r, domain.ErrInvalidClientSetting)
			return
		}
		setting, err := repo.SetClientSetting(r.PathValue("client"), r.PathValue("key"), value)
		if err != nil {
			writeClientSettingError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, setting)
	})
	mux.HandleFunc("DELETE /client-settings/{client}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := clientSettingRepo(w, r, proxy)
		if !ok {
			return
		}
		address := clientSettingAddress{Client: r.PathValue("client"), Key: r.PathValue("key")}
		if err := repo.DeleteClientSetting(address.Client, address.Key); err != nil {
			writeClientSettingError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, address)
	})
}

// clientSettingRepo returns the open project's store, or writes 404
// not_found when no project is open.
func clientSettingRepo(w http.ResponseWriter, r *http.Request, proxy *marasi.Proxy) (domain.ConfigRepository, bool) {
	repo, err := proxy.GetConfigRepo()
	if err != nil {
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
		return nil, false
	}
	return repo, true
}

// decodeClientSettingBody reads {"value": <JSON>}. The value may be null.
func decodeClientSettingBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	if r.Body == nil {
		return nil, errInvalidClientSettingBody
	}
	// Leave room for the envelope; the store enforces the exact value limit.
	body := http.MaxBytesReader(w, r.Body, domain.MaxClientSettingValueBytes+1024)
	var fields map[string]json.RawMessage
	if !decodeStrictJSON(body, &fields) {
		return nil, errInvalidClientSettingBody
	}
	value, present := fields["value"]
	if !present || len(fields) != 1 {
		return nil, errInvalidClientSettingBody
	}
	return value, nil
}

func writeClientSettingError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, "internal_server_error"
	switch {
	case errors.Is(err, domain.ErrInvalidClientSetting):
		status, code = http.StatusBadRequest, "invalid_client_setting"
	case errors.Is(err, domain.ErrReservedClient):
		status, code = http.StatusBadRequest, "reserved_client"
	case errors.Is(err, domain.ErrClientSettingNotFound):
		status, code = http.StatusNotFound, "not_found"
	}
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
