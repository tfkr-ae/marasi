package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type stubWaypointRepository struct {
	items     map[string]string
	getCalls  int
	createErr error
	getErrAt  int
}

func (repo *stubWaypointRepository) GetWaypoints() ([]*domain.Waypoint, error) {
	repo.getCalls++
	if repo.getErrAt == repo.getCalls {
		return nil, errors.New("get failed")
	}
	hostnames := make([]string, 0, len(repo.items))
	for hostname := range repo.items {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)
	items := make([]*domain.Waypoint, 0, len(hostnames))
	for _, hostname := range hostnames {
		items = append(items, &domain.Waypoint{Hostname: hostname, Override: repo.items[hostname]})
	}
	return items, nil
}

func (repo *stubWaypointRepository) CreateWaypoint(hostname, override string) error {
	if repo.createErr != nil {
		return repo.createErr
	}
	if _, exists := repo.items[hostname]; exists {
		return domain.ErrWaypointAlreadyExists
	}
	if repo.items == nil {
		repo.items = make(map[string]string)
	}
	repo.items[hostname] = override
	return nil
}

func (repo *stubWaypointRepository) CreateOrUpdateWaypoint(hostname, override string) error {
	if repo.items == nil {
		repo.items = make(map[string]string)
	}
	repo.items[hostname] = override
	return nil
}

func (repo *stubWaypointRepository) DeleteWaypoint(string) error { return nil }

func TestWaypointControlRoutes(t *testing.T) {
	t.Run("should list waypoints in hostname order without publishing an event", func(t *testing.T) {
		repo := &stubWaypointRepository{items: map[string]string{
			"z.example:443": "127.0.0.1:9000",
			"a.example:80":  "127.0.0.1:8080",
		}}
		server, _ := newWaypointServer(t, repo)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestWaypoint(server, http.MethodGet, "")
		assertWaypointResponse(t, response, http.StatusOK, `{"items":[{"hostname":"a.example:80","override":"127.0.0.1:8080"},{"hostname":"z.example:443","override":"127.0.0.1:9000"}]}`+"\n")
		assertNoWaypointEvent(t, subscriber)
	})

	t.Run("should return an empty list", func(t *testing.T) {
		server, _ := newWaypointServer(t, &stubWaypointRepository{})
		assertWaypointResponse(t, requestWaypoint(server, http.MethodGet, ""), http.StatusOK, `{"items":[]}`+"\n")
	})

	t.Run("should trim add sync the live map and publish the resulting list", func(t *testing.T) {
		repo := &stubWaypointRepository{items: map[string]string{"z.example:443": "127.0.0.1:9000"}}
		server, proxy := newWaypointServer(t, repo)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestWaypoint(server, http.MethodPost, `{"hostname":"  a.example:80  ","override":"  [::1]:8080  "}`)
		want := `{"items":[{"hostname":"a.example:80","override":"[::1]:8080"},{"hostname":"z.example:443","override":"127.0.0.1:9000"}]}`
		assertWaypointResponse(t, response, http.StatusOK, want+"\n")
		if proxy.Waypoints["a.example:80"] != "[::1]:8080" || repo.getCalls != 2 {
			t.Fatalf("\nwanted:\nsynced live waypoint after persist\ngot:\nmap %v, get calls %d", proxy.Waypoints, repo.getCalls)
		}
		assertWaypointEvent(t, subscriber, "waypoint.added", want)
	})

	t.Run("should reject a duplicate after trimming without publishing an event", func(t *testing.T) {
		repo := &stubWaypointRepository{items: map[string]string{"example.com:443": "127.0.0.1:8080"}}
		server, _ := newWaypointServer(t, repo)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestWaypoint(server, http.MethodPost, `{"hostname":" example.com:443 ","override":"127.0.0.1:9000"}`)
		assertWaypointError(t, response, http.StatusConflict, "waypoint_already_exists")
		if repo.items["example.com:443"] != "127.0.0.1:8080" {
			t.Fatal("duplicate add replaced the existing waypoint")
		}
		assertNoWaypointEvent(t, subscriber)
	})

	t.Run("should reject invalid requests without publishing an event", func(t *testing.T) {
		invalid := []string{
			``, `null`, `{}`, `{"hostname":"example.com:443"}`, `{"override":"127.0.0.1:8080"}`,
			`{"hostname":null,"override":"127.0.0.1:8080"}`,
			`{"hostname":"example.com:443","override":null}`,
			`{"hostname":" ","override":"127.0.0.1:8080"}`,
			`{"hostname":"example.com","override":"127.0.0.1:8080"}`,
			`{"hostname":"example.com:443","override":"127.0.0.1"}`,
			`{"hostname":"example.com:443","override":"127.0.0.1:8080","extra":true}`,
			`{"hostname":"example.com:443","override":"127.0.0.1:8080"} {}`,
		}
		for _, body := range invalid {
			server, _ := newWaypointServer(t, &stubWaypointRepository{})
			subscriber := server.events.subscribe()
			response := requestWaypoint(server, http.MethodPost, body)
			assertWaypointError(t, response, http.StatusBadRequest, "invalid_waypoint_request")
			assertNoWaypointEvent(t, subscriber)
			server.events.unsubscribe(subscriber)
		}
	})

	t.Run("should report a missing repository", func(t *testing.T) {
		proxy, err := marasi.New()
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		server := newTestServer(proxy, func() {})
		assertWaypointError(t, requestWaypoint(server, http.MethodGet, ""), http.StatusNotFound, "not_found")
		assertWaypointError(t, requestWaypoint(server, http.MethodPost, `{"hostname":"example.com:443","override":"127.0.0.1:8080"}`), http.StatusNotFound, "not_found")
	})

	t.Run("should return an internal error and no event when sync fails after persist", func(t *testing.T) {
		repo := &stubWaypointRepository{getErrAt: 1}
		server, proxy := newWaypointServer(t, repo)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestWaypoint(server, http.MethodPost, `{"hostname":"example.com:443","override":"127.0.0.1:8080"}`)
		assertWaypointError(t, response, http.StatusInternalServerError, "internal_server_error")
		if repo.items["example.com:443"] != "127.0.0.1:8080" || len(proxy.Waypoints) != 0 {
			t.Fatalf("\nwanted:\npersisted waypoint and unchanged live map\ngot:\nrepo %v, map %v", repo.items, proxy.Waypoints)
		}
		assertNoWaypointEvent(t, subscriber)
	})
}

func newWaypointServer(t *testing.T, repo *stubWaypointRepository) (*Server, *marasi.Proxy) {
	t.Helper()
	proxy, err := marasi.New()
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	proxy.WaypointRepo = repo
	return newTestServer(proxy, func() {}), proxy
}

func requestWaypoint(server *Server, method, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/waypoint", strings.NewReader(body))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertWaypointResponse(t *testing.T, response *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if response.Code != status || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != body {
		t.Fatalf("\nwanted:\n%d application/json %s\ngot:\n%d %s %s", status, body, response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func assertWaypointError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertWaypointResponse(t, response, status, `{"error":"`+code+`"}`+"\n")
}

func assertWaypointEvent(t *testing.T, subscriber *eventSubscriber, name, data string) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		if event.name != name || string(event.data) != data {
			t.Fatalf("\nwanted:\n%s %s\ngot:\n%s %s", name, data, event.name, event.data)
		}
	default:
		t.Fatalf("\nwanted:\n%s event\ngot:\nno event", name)
	}
}

func assertNoWaypointEvent(t *testing.T, subscriber *eventSubscriber) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		t.Fatalf("\nwanted:\nno event\ngot:\n%s %s", event.name, event.data)
	default:
	}
}
