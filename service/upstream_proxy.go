package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/internal/filelock"
)

// Upstream proxy events. Each carries the whole redacted list, and only the
// service instance that made the edit publishes it
// (docs/adr/2026-10-23-upstream-proxies-are-listed-per-machine-and-selected-per-project.md).
const (
	upstreamProxyAddedEvent   = "upstream_proxy.added"
	upstreamProxyUpdatedEvent = "upstream_proxy.updated"
	upstreamProxyRemovedEvent = "upstream_proxy.removed"
)

var errInvalidUpstreamProxyRequest = errors.New("invalid upstream proxy request")

// UpstreamProxy is an upstream proxy list entry as the control API shows it.
// It never holds credentials or the CA's PEM.
type UpstreamProxy struct {
	Name           string `json:"name"`
	Scheme         string `json:"scheme"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	HasCredentials bool   `json:"has_credentials"`
	HasCA          bool   `json:"has_ca"`
}

type upstreamProxyList struct {
	Items []UpstreamProxy `json:"items"`
}

// UpstreamProxies edits the machine's upstream proxy list in
// marasi_config.yaml for one service instance.
type UpstreamProxies struct {
	configDir string
	events    *eventBroadcaster
}

// NewUpstreamProxies creates the upstream proxy list module for configDir.
func NewUpstreamProxies(configDir string) *UpstreamProxies {
	return &UpstreamProxies{configDir: configDir}
}

func addUpstreamProxyRoutes(mux *http.ServeMux, proxies *UpstreamProxies) {
	mux.HandleFunc("GET /upstream-proxy", func(w http.ResponseWriter, r *http.Request) {
		items, err := proxies.List(r.Context())
		if err != nil {
			writeUpstreamProxyError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, upstreamProxyList{Items: items})
	})
	mux.HandleFunc("POST /upstream-proxy", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name *string `json:"name"`
			URL  *string `json:"url"`
			CA   *string `json:"ca"`
		}
		if r.Body == nil || !decodeStrictJSON(r.Body, &body) || body.Name == nil || body.URL == nil {
			writeUpstreamProxyError(w, r, errInvalidUpstreamProxyRequest)
			return
		}
		entry := marasi.UpstreamProxyConfig{Name: *body.Name, URL: *body.URL}
		if body.CA != nil {
			if *body.CA == "" {
				writeUpstreamProxyError(w, r, errInvalidUpstreamProxyRequest)
				return
			}
			entry.CA = *body.CA
		}
		items, err := proxies.Add(r.Context(), entry)
		if err != nil {
			writeUpstreamProxyError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, upstreamProxyList{Items: items})
	})
	mux.HandleFunc("POST /upstream-proxy/{name}", func(w http.ResponseWriter, r *http.Request) {
		update, err := decodeUpstreamProxyUpdate(r)
		if err != nil {
			writeUpstreamProxyError(w, r, err)
			return
		}
		items, err := proxies.Update(r.Context(), r.PathValue("name"), update)
		if err != nil {
			writeUpstreamProxyError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, upstreamProxyList{Items: items})
	})
	mux.HandleFunc("DELETE /upstream-proxy/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !emptyRequestBody(r) {
			writeUpstreamProxyError(w, r, errInvalidUpstreamProxyRequest)
			return
		}
		items, err := proxies.Remove(r.Context(), r.PathValue("name"))
		if err != nil {
			writeUpstreamProxyError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, upstreamProxyList{Items: items})
	})
}

// decodeUpstreamProxyUpdate reads {"url"?: string, "ca"?: string|null} with
// at least one field. A null CA removes it.
func decodeUpstreamProxyUpdate(r *http.Request) (marasi.UpstreamProxyUpdate, error) {
	var fields map[string]json.RawMessage
	if r.Body == nil || !decodeStrictJSON(r.Body, &fields) || len(fields) == 0 {
		return marasi.UpstreamProxyUpdate{}, errInvalidUpstreamProxyRequest
	}
	var update marasi.UpstreamProxyUpdate
	for name, raw := range fields {
		isNull := bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		var value string
		if !isNull && json.Unmarshal(raw, &value) != nil {
			return marasi.UpstreamProxyUpdate{}, errInvalidUpstreamProxyRequest
		}
		switch {
		case name == "url" && !isNull:
			update.URL = &value
		case name == "ca" && isNull:
			update.CA = &value
		case name == "ca" && value != "":
			update.CA = &value
		default:
			return marasi.UpstreamProxyUpdate{}, errInvalidUpstreamProxyRequest
		}
	}
	return update, nil
}

func writeUpstreamProxyError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "internal_server_error", ""
	switch {
	case errors.Is(err, errInvalidUpstreamProxyRequest):
		status, code = http.StatusBadRequest, "invalid_upstream_proxy"
	case errors.Is(err, marasi.ErrInvalidUpstreamProxy):
		// The message names the field at fault and never quotes the URL.
		status, code, message = http.StatusBadRequest, "invalid_upstream_proxy", err.Error()
	case errors.Is(err, marasi.ErrUpstreamProxyExists):
		status, code = http.StatusConflict, "upstream_proxy_already_exists"
	case errors.Is(err, marasi.ErrUpstreamProxyNotFound):
		status, code = http.StatusNotFound, "not_found"
	}
	writeJSON(w, r, status, struct {
		Error   string `json:"error"`
		Message string `json:"message,omitempty"`
	}{Error: code, Message: message})
}

// List returns the redacted upstream proxy list in YAML order.
func (u *UpstreamProxies) List(ctx context.Context) ([]UpstreamProxy, error) {
	var items []UpstreamProxy
	err := u.withConfig(ctx, func(cfg *marasi.Config) error {
		items = redactUpstreamProxies(cfg.UpstreamProxies)
		return nil
	})
	return items, err
}

// Add appends entry to the list and returns the redacted list.
func (u *UpstreamProxies) Add(ctx context.Context, entry marasi.UpstreamProxyConfig) ([]UpstreamProxy, error) {
	return u.edit(ctx, upstreamProxyAddedEvent, func(cfg *marasi.Config) error {
		return cfg.AddUpstreamProxy(entry)
	})
}

// Update changes the named entry and returns the redacted list.
func (u *UpstreamProxies) Update(ctx context.Context, name string, update marasi.UpstreamProxyUpdate) ([]UpstreamProxy, error) {
	return u.edit(ctx, upstreamProxyUpdatedEvent, func(cfg *marasi.Config) error {
		return cfg.UpdateUpstreamProxy(name, update)
	})
}

// Remove removes the named entry and returns the redacted list.
func (u *UpstreamProxies) Remove(ctx context.Context, name string) ([]UpstreamProxy, error) {
	return u.edit(ctx, upstreamProxyRemovedEvent, func(cfg *marasi.Config) error {
		return cfg.RemoveUpstreamProxy(name)
	})
}

// edit runs change under the config lock and publishes event with the new
// list while the lock is still held, so events leave in the order the edits
// were written.
func (u *UpstreamProxies) edit(ctx context.Context, event string, change func(*marasi.Config) error) ([]UpstreamProxy, error) {
	var items []UpstreamProxy
	err := u.withConfig(ctx, func(cfg *marasi.Config) error {
		if err := change(cfg); err != nil {
			return err
		}
		items = redactUpstreamProxies(cfg.UpstreamProxies)
		if u.events != nil {
			u.events.publish(event, upstreamProxyList{Items: items})
		}
		return nil
	})
	return items, err
}

// withConfig rereads marasi_config.yaml under the config lock (docs/adr/2026-10-06-chrome-config-lock-reread.md).
func (u *UpstreamProxies) withConfig(ctx context.Context, operation func(*marasi.Config) error) (resultErr error) {
	lock, err := acquireConfigLock(ctx, u.configDir)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, filelock.Unlock(lock), lock.Close())
	}()
	cfg, err := marasi.ReadConfig(u.configDir)
	if err != nil {
		return err
	}
	return operation(cfg)
}

// redactUpstreamProxies drops credentials and PEMs. An entry whose URL a
// hand edit broke is listed with only its name and CA flag.
func redactUpstreamProxies(entries []marasi.UpstreamProxyConfig) []UpstreamProxy {
	items := make([]UpstreamProxy, len(entries))
	for i, entry := range entries {
		items[i] = UpstreamProxy{Name: entry.Name, HasCA: entry.CA != ""}
		if _, address, err := marasi.ParseUpstreamProxyURL(entry.URL); err == nil {
			items[i].Scheme = string(address.Scheme)
			items[i].Host = address.Host
			items[i].Port = address.Port
			items[i].HasCredentials = address.HasCredentials
		}
	}
	return items
}
