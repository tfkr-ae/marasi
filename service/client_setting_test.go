package service

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
)

func newClientSettingServer(t *testing.T) *Server {
	t.Helper()
	conn, err := db.New(t.TempDir()+"/settings.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	repo := db.NewProxyRepo(conn)
	t.Cleanup(func() { repo.Close() })
	return newTestServer(&marasi.Proxy{ConfigRepo: repo}, func() {})
}

type clientSettingResponse struct {
	Client    string          `json:"client"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt string          `json:"updated_at"`
}

func decodeClientSetting(t *testing.T, body string) clientSettingResponse {
	t.Helper()
	var setting clientSettingResponse
	if err := json.Unmarshal([]byte(body), &setting); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return setting
}

func TestClientSettingControlAPI(t *testing.T) {
	t.Run("should put and get a setting under a key with slashes", func(t *testing.T) {
		server := newClientSettingServer(t)

		put := requestControlAPI(server, http.MethodPut, "/client-settings/marasi-app/ledger/columns", `{"value":{"order":["host","path"]}}`)
		if put.Code != http.StatusOK {
			t.Fatalf("wanted: 200\ngot: %d %s", put.Code, put.Body)
		}
		stored := decodeClientSetting(t, put.Body.String())
		if stored.Client != "marasi-app" || stored.Key != "ledger/columns" || string(stored.Value) != `{"order":["host","path"]}` || stored.UpdatedAt == "" {
			t.Fatalf("wanted: stored setting\ngot: %s", put.Body)
		}

		get := requestControlAPI(server, http.MethodGet, "/client-settings/marasi-app/ledger/columns", "")
		if get.Code != http.StatusOK || get.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("wanted: 200 application/json\ngot: %d %s", get.Code, get.Header().Get("Content-Type"))
		}
		if got := decodeClientSetting(t, get.Body.String()); string(got.Value) != `{"order":["host","path"]}` {
			t.Fatalf("\nwanted:\n{\"order\":[\"host\",\"path\"]}\ngot:\n%s", got.Value)
		}
	})

	t.Run("should list a client's settings with a prefix sorted by key", func(t *testing.T) {
		server := newClientSettingServer(t)
		for _, put := range []struct{ key, value string }{
			{"ledger.b", `2`}, {"theme", `"dark"`}, {"ledger.a", `1`},
		} {
			requestControlAPI(server, http.MethodPut, "/client-settings/marasi-app/"+put.key, `{"value":`+put.value+`}`)
		}

		response := requestControlAPI(server, http.MethodGet, "/client-settings/marasi-app?prefix=ledger.", "")
		if response.Code != http.StatusOK {
			t.Fatalf("wanted: 200\ngot: %d %s", response.Code, response.Body)
		}
		var list struct {
			Items []clientSettingResponse `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
			t.Fatalf("decoding %s: %v", response.Body, err)
		}
		var got []string
		for _, item := range list.Items {
			got = append(got, item.Key+"="+string(item.Value))
		}
		if strings.Join(got, ",") != "ledger.a=1,ledger.b=2" {
			t.Fatalf("\nwanted:\nledger.a=1,ledger.b=2\ngot:\n%v", got)
		}

		empty := requestControlAPI(server, http.MethodGet, "/client-settings/nobody", "")
		assertControlAPIResponse(t, empty, http.StatusOK, "{\"items\":[]}\n")
	})

	t.Run("should delete a setting and accept deleting a missing one", func(t *testing.T) {
		server := newClientSettingServer(t)
		requestControlAPI(server, http.MethodPut, "/client-settings/marasi-app/theme", `{"value":"dark"}`)

		for range 2 {
			response := requestControlAPI(server, http.MethodDelete, "/client-settings/marasi-app/theme", "")
			assertControlAPIResponse(t, response, http.StatusOK, "{\"client\":\"marasi-app\",\"key\":\"theme\"}\n")
		}
		get := requestControlAPI(server, http.MethodGet, "/client-settings/marasi-app/theme", "")
		assertControlAPIResponse(t, get, http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	})

	t.Run("should refuse writes to the reserved client with a 400", func(t *testing.T) {
		server := newClientSettingServer(t)

		put := requestControlAPI(server, http.MethodPut, "/client-settings/marasi/legacy.content_type_exclusion", `{"value":[]}`)
		assertControlAPIResponse(t, put, http.StatusBadRequest, "{\"error\":\"reserved_client\"}\n")

		get := requestControlAPI(server, http.MethodGet, "/client-settings/marasi/legacy.content_type_exclusion", "")
		assertControlAPIResponse(t, get, http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		list := requestControlAPI(server, http.MethodGet, "/client-settings/marasi", "")
		assertControlAPIResponse(t, list, http.StatusOK, "{\"items\":[]}\n")
		del := requestControlAPI(server, http.MethodDelete, "/client-settings/marasi/legacy.content_type_exclusion", "")
		assertControlAPIResponse(t, del, http.StatusOK, "{\"client\":\"marasi\",\"key\":\"legacy.content_type_exclusion\"}\n")
	})

	t.Run("should reject invalid clients keys and values with a 400", func(t *testing.T) {
		server := newClientSettingServer(t)
		bigValue := `{"value":"` + strings.Repeat("x", 1<<20) + `"}`
		for _, tc := range []struct{ name, method, path, body string }{
			{"uppercase client on put", http.MethodPut, "/client-settings/Marasi-App/k", `{"value":1}`},
			{"uppercase client on get", http.MethodGet, "/client-settings/Marasi-App/k", ``},
			{"uppercase client on list", http.MethodGet, "/client-settings/Marasi-App", ``},
			{"uppercase client on delete", http.MethodDelete, "/client-settings/Marasi-App/k", ``},
			{"key with control character", http.MethodPut, "/client-settings/app/a%01b", `{"value":1}`},
			{"key over 256 bytes", http.MethodPut, "/client-settings/app/" + strings.Repeat("k", 257), `{"value":1}`},
			{"value over 1 MiB", http.MethodPut, "/client-settings/app/k", bigValue},
			{"missing value", http.MethodPut, "/client-settings/app/k", `{}`},
			{"unknown field", http.MethodPut, "/client-settings/app/k", `{"value":1,"extra":true}`},
			{"invalid JSON body", http.MethodPut, "/client-settings/app/k", `{"value":`},
			{"trailing body", http.MethodPut, "/client-settings/app/k", `{"value":1} {}`},
			{"empty body", http.MethodPut, "/client-settings/app/k", ``},
		} {
			response := requestControlAPI(server, tc.method, tc.path, tc.body)
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_client_setting\"}\n" {
				t.Errorf("%s: wanted: 400 invalid_client_setting\ngot: %d %s", tc.name, response.Code, response.Body)
			}
		}
	})

	t.Run("should accept null as a value", func(t *testing.T) {
		server := newClientSettingServer(t)
		put := requestControlAPI(server, http.MethodPut, "/client-settings/app/k", `{"value":null}`)
		if put.Code != http.StatusOK {
			t.Fatalf("wanted: 200\ngot: %d %s", put.Code, put.Body)
		}
		if got := decodeClientSetting(t, put.Body.String()); string(got.Value) != `null` {
			t.Fatalf("wanted: null\ngot: %s", got.Value)
		}
	})
}

func TestClientSettingsWithoutProject(t *testing.T) {
	server := newTestServer(&marasi.Proxy{}, func() {})
	requests := []struct{ method, path, body string }{
		{http.MethodGet, "/client-settings/app", ""},
		{http.MethodGet, "/client-settings/app/k", ""},
		{http.MethodPut, "/client-settings/app/k", `{"value":1}`},
		{http.MethodDelete, "/client-settings/app/k", ""},
	}
	for _, request := range requests {
		t.Run("should return 404 not_found for "+request.method+" "+request.path+" when no project is open", func(t *testing.T) {
			response := requestControlAPI(server, request.method, request.path, request.body)
			if want := "{\"error\":\"not_found\"}\n"; response.Code != http.StatusNotFound || response.Body.String() != want {
				t.Fatalf("\nwanted:\n404 %s\ngot:\n%d %s", want, response.Code, response.Body)
			}
		})
	}
}
