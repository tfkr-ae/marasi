package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
	t.Run("should list wordlists by name and return an empty list", func(t *testing.T) {
		provider := &stubWordlistProvider{infos: []wordlist.Info{
			{Name: "users.txt", Size: 5},
			{Name: "passwords.txt", Size: 15},
		}}
		server := newTestServer(&marasi.Proxy{WordlistManager: provider}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/wordlist", "")
		want := "{\"items\":[{\"name\":\"passwords.txt\",\"size\":15},{\"name\":\"users.txt\",\"size\":5}]}\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)

		empty := newTestServer(&marasi.Proxy{WordlistManager: &stubWordlistProvider{}}, func() {})
		assertControlAPIResponse(t, requestControlAPI(empty, http.MethodGet, "/wordlist", ""), http.StatusOK, "{\"items\":[]}\n")
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
