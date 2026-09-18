package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/wordlist"
)

type stubWordlistProvider struct {
	infos    []wordlist.Info
	entries  map[string][]string
	openErr  error
	iterator *stubWordlistIterator
}

func (provider *stubWordlistProvider) List() ([]wordlist.Info, error) {
	return provider.infos, nil
}

func (provider *stubWordlistProvider) Open(name string) (wordlist.Iterator, error) {
	if provider.openErr != nil {
		return nil, provider.openErr
	}
	provider.iterator = &stubWordlistIterator{entries: provider.entries[name]}
	return provider.iterator, nil
}

type stubWordlistIterator struct {
	entries   []string
	index     int
	scanCalls int
}

func (iterator *stubWordlistIterator) Scan() bool {
	iterator.scanCalls++
	return iterator.index < len(iterator.entries)
}

func (iterator *stubWordlistIterator) Text() string {
	item := iterator.entries[iterator.index]
	iterator.index++
	return item
}

func (iterator *stubWordlistIterator) Err() error   { return nil }
func (iterator *stubWordlistIterator) Close() error { return nil }

func TestWordlistControlAPI(t *testing.T) {
	t.Run("should remove a wordlist and publish the resulting list", func(t *testing.T) {
		parentDir := t.TempDir()
		manager, err := wordlist.NewManager(parentDir)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		removedPath := filepath.Join(parentDir, "wordlists", "passwords.txt")
		if err = os.WriteFile(removedPath, []byte("password\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err = os.WriteFile(filepath.Join(parentDir, "wordlists", "users.txt"), []byte("admin\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := newTestServer(&marasi.Proxy{WordlistManager: manager}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodDelete, "/wordlist/passwords.txt", "")
		want := `{"items":[{"name":"users.txt","size":6}]}`
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		if _, err = os.Stat(removedPath); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nwordlist removed\ngot:\n%v", err)
		}
		select {
		case event := <-subscriber.events:
			if event.name != "wordlist.removed" || string(event.data) != want {
				t.Fatalf("\nwanted:\nwordlist.removed %s\ngot:\n%s %s", want, event.name, event.data)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nwordlist.removed event\ngot:\nno event")
		}
	})

	t.Run("should reject missing names and invalid remove requests", func(t *testing.T) {
		manager, err := wordlist.NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := newTestServer(&marasi.Proxy{WordlistManager: manager}, func() {})

		assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/wordlist/missing.txt", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		for _, request := range []struct {
			path string
			body string
		}{
			{path: "/wordlist/.."},
			{path: "/wordlist/foo/../bar"},
			{path: "/wordlist/%2E%2E"},
			{path: "/wordlist/passwords.txt?extra=true"},
			{path: "/wordlist/passwords.txt", body: "not empty"},
		} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, request.path, request.body), http.StatusBadRequest, "{\"error\":\"invalid_wordlist_request\"}\n")
		}
	})

	t.Run("should add a wordlist and publish the resulting list", func(t *testing.T) {
		parentDir := t.TempDir()
		manager, err := wordlist.NewManager(parentDir)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err = os.WriteFile(filepath.Join(parentDir, "wordlists", "z.txt"), []byte("z\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		source := filepath.Join(t.TempDir(), "a.txt")
		if err = os.WriteFile(source, []byte("alpha\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := newTestServer(&marasi.Proxy{WordlistManager: manager}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		body, _ := json.Marshal(map[string]string{"path": source})

		response := requestControlAPI(server, http.MethodPost, "/wordlist", string(body))
		want := `{"items":[{"name":"a.txt","size":6},{"name":"z.txt","size":2}]}`
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		if _, err = os.Stat(source); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nsource removed\ngot:\n%v", err)
		}
		select {
		case event := <-subscriber.events:
			if event.name != "wordlist.added" || string(event.data) != want {
				t.Fatalf("\nwanted:\nwordlist.added %s\ngot:\n%s %s", want, event.name, event.data)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nwordlist.added event\ngot:\nno event")
		}
	})

	t.Run("should reject a conflicting name without moving the source or publishing", func(t *testing.T) {
		parentDir := t.TempDir()
		manager, err := wordlist.NewManager(parentDir)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err = os.WriteFile(filepath.Join(parentDir, "wordlists", "words.txt"), []byte("old"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		source := filepath.Join(t.TempDir(), "words.txt")
		if err = os.WriteFile(source, []byte("new"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := newTestServer(&marasi.Proxy{WordlistManager: manager}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		body, _ := json.Marshal(map[string]string{"path": source})

		response := requestControlAPI(server, http.MethodPost, "/wordlist", string(body))
		assertControlAPIResponse(t, response, http.StatusConflict, "{\"error\":\"wordlist_already_exists\"}\n")
		if content, readErr := os.ReadFile(source); readErr != nil || string(content) != "new" {
			t.Fatalf("\nwanted:\nsource left in place\ngot:\n%q, %v", content, readErr)
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		case <-time.After(10 * time.Millisecond):
		}
	})

	t.Run("should reject invalid add requests and missing sources", func(t *testing.T) {
		manager, err := wordlist.NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := newTestServer(&marasi.Proxy{WordlistManager: manager}, func() {})
		for _, body := range []string{"", "null", `{}`, `{"path":"relative.txt"}`, `{"path":"/tmp/file","extra":true}`, `{"path":"/tmp/file"} {}`, `{"path":"/tmp/bad\u0000name"}`} {
			response := requestControlAPI(server, http.MethodPost, "/wordlist", body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, "{\"error\":\"invalid_wordlist_request\"}\n")
		}
		missing := filepath.Join(t.TempDir(), "missing.txt")
		body, _ := json.Marshal(map[string]string{"path": missing})
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/wordlist", string(body)), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	})

	t.Run("should list wordlists by name and return an empty list", func(t *testing.T) {
		provider := &stubWordlistProvider{infos: []wordlist.Info{
			{Name: "users.txt", Size: 5},
			{Name: "passwords.txt", Size: 15},
		}}
		server := newTestServer(&marasi.Proxy{WordlistManager: provider}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodGet, "/wordlist", "")
		want := "{\"items\":[{\"name\":\"passwords.txt\",\"size\":15},{\"name\":\"users.txt\",\"size\":5}]}\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)

		empty := newTestServer(&marasi.Proxy{WordlistManager: &stubWordlistProvider{}}, func() {})
		assertControlAPIResponse(t, requestControlAPI(empty, http.MethodGet, "/wordlist", ""), http.StatusOK, "{\"items\":[]}\n")
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		case <-time.After(10 * time.Millisecond):
		}
	})

	t.Run("should preview only the requested number of wordlist entries", func(t *testing.T) {
		provider := &stubWordlistProvider{entries: map[string][]string{
			"passwords.txt": {"one", "two", "three"},
		}}
		server := newTestServer(&marasi.Proxy{WordlistManager: provider}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/wordlist/passwords.txt?limit=2", "")
		assertControlAPIResponse(t, response, http.StatusOK, "{\"name\":\"passwords.txt\",\"items\":[\"one\",\"two\"]}\n")
		if provider.iterator.scanCalls != 2 {
			t.Fatalf("\nwanted:\n2 scanned entries\ngot:\n%d", provider.iterator.scanCalls)
		}
	})

	t.Run("should default the preview limit to twenty", func(t *testing.T) {
		entries := make([]string, 21)
		for index := range entries {
			entries[index] = "item"
		}
		provider := &stubWordlistProvider{entries: map[string][]string{"words.txt": entries}}
		server := newTestServer(&marasi.Proxy{WordlistManager: provider}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/wordlist/words.txt", "")
		if response.Code != http.StatusOK || provider.iterator == nil || provider.iterator.scanCalls != 20 {
			t.Fatalf("\nwanted:\n200 with 20 scanned entries\ngot:\n%d with iterator %+v", response.Code, provider.iterator)
		}
	})

	t.Run("should reject invalid names and limits", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{WordlistManager: &stubWordlistProvider{}}, func() {})
		for _, path := range []string{
			"/wordlist/",
			"/wordlist/..",
			"/wordlist/foo/../bar",
			"/wordlist/%2E%2E",
			"/wordlist/words.txt?limit=0",
			"/wordlist/words.txt?limit=101",
			"/wordlist/words.txt?limit=nope",
			"/wordlist/words.txt?limit=",
			"/wordlist/words.txt?extra=true",
			"/wordlist/words.txt?limit=2;extra=true",
		} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, path, ""), http.StatusBadRequest, "{\"error\":\"invalid_wordlist_request\"}\n")
		}

		request := httptest.NewRequest(http.MethodGet, "/wordlist", nil)
		request.URL.RawQuery = "bad=%ZZ"
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		assertControlAPIResponse(t, response, http.StatusBadRequest, "{\"error\":\"invalid_wordlist_request\"}\n")
	})

	t.Run("should return not found for a missing wordlist or provider", func(t *testing.T) {
		missing := newTestServer(&marasi.Proxy{WordlistManager: &stubWordlistProvider{openErr: os.ErrNotExist}}, func() {})
		assertControlAPIResponse(t, requestControlAPI(missing, http.MethodGet, "/wordlist/missing.txt", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")

		withoutProvider := newTestServer(&marasi.Proxy{}, func() {})
		assertControlAPIResponse(t, requestControlAPI(withoutProvider, http.MethodGet, "/wordlist", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		assertControlAPIResponse(t, requestControlAPI(withoutProvider, http.MethodGet, "/wordlist/words.txt", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	})
}
