package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
)

type stubLogRepository struct {
	domain.LogRepository
	items      []*domain.Log
	nextCursor *uuid.UUID
	cursor     *uuid.UUID
	limit      int
	listErr    error
}

func (repo *stubLogRepository) ListLogs(cursor *uuid.UUID, limit int) ([]*domain.Log, *uuid.UUID, error) {
	repo.cursor = cursor
	repo.limit = limit
	return repo.items, repo.nextCursor, repo.listErr
}

func TestLogList(t *testing.T) {
	t.Run("should return proxy log fields and an empty context object", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		requestID := uuid.MustParse("01938030-0000-7000-8000-000000000001")
		extensionID := uuid.MustParse("01938031-0000-7000-8000-000000000001")
		nextCursor := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubLogRepository{
			items: []*domain.Log{
				{
					ID: id, Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
					Level: "FATAL", Message: "proxy failure", Context: nil,
				},
				{
					ID: nextCursor, Timestamp: time.Date(2026, 1, 2, 3, 4, 4, 0, time.UTC),
					Level: "DEBUG", Message: "request log", Context: map[string]any{"source": "proxy"},
					RequestID: &requestID, ExtensionID: &extensionID,
				},
			},
			nextCursor: &nextCursor,
		}
		server := newTestServer(&marasi.Proxy{LogRepo: repo}, func() {})
		response := requestLogs(server, http.MethodGet, "/logs")

		want := "{\"items\":[{\"id\":\"01938032-1b17-7243-b035-e6a9f4645904\",\"timestamp\":\"2026-01-02T03:04:05Z\",\"level\":\"FATAL\",\"message\":\"proxy failure\",\"context\":{},\"request_id\":null,\"extension_id\":null},{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"timestamp\":\"2026-01-02T03:04:04Z\",\"level\":\"DEBUG\",\"message\":\"request log\",\"context\":{\"source\":\"proxy\"},\"request_id\":\"01938030-0000-7000-8000-000000000001\",\"extension_id\":\"01938031-0000-7000-8000-000000000001\"}],\"next_cursor\":\"0193802f-f0e7-73d9-a764-06d21e367809\"}\n"
		assertLogResponse(t, response, http.StatusOK, want)
	})

	t.Run("should return a stable empty page", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{LogRepo: &stubLogRepository{}}, func() {})
		response := requestLogs(server, http.MethodGet, "/logs")
		assertLogResponse(t, response, http.StatusOK, "{\"items\":[],\"next_cursor\":null}\n")
	})

	t.Run("should forward limit and cursor and ignore unknown query parameters", func(t *testing.T) {
		cursor := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubLogRepository{}
		server := newTestServer(&marasi.Proxy{LogRepo: repo}, func() {})
		response := requestLogs(server, http.MethodGet, "/logs?limit=7&cursor="+cursor.String()+"&future=ignored")
		assertLogResponse(t, response, http.StatusOK, "{\"items\":[],\"next_cursor\":null}\n")
		if repo.limit != 7 || repo.cursor == nil || *repo.cursor != cursor {
			t.Fatalf("wanted limit 7 and cursor %s, got limit %d and cursor %v", cursor, repo.limit, repo.cursor)
		}
	})

	t.Run("should default to 200 and accept the maximum page size", func(t *testing.T) {
		repo := &stubLogRepository{}
		server := newTestServer(&marasi.Proxy{LogRepo: repo}, func() {})
		assertLogResponse(t, requestLogs(server, http.MethodGet, "/logs"), http.StatusOK, "{\"items\":[],\"next_cursor\":null}\n")
		if repo.limit != 200 {
			t.Fatalf("wanted default limit 200, got %d", repo.limit)
		}
		assertLogResponse(t, requestLogs(server, http.MethodGet, "/logs?limit=500"), http.StatusOK, "{\"items\":[],\"next_cursor\":null}\n")
		if repo.limit != 500 {
			t.Fatalf("wanted maximum limit 500, got %d", repo.limit)
		}
	})

	t.Run("should return the default and maximum page sizes from persisted logs", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		project := canonicalProjectPath(t, filepath.Join(dir, "paged.marasi"))
		if err := lifecycle.Open(context.Background(), project); err != nil {
			t.Fatalf("opening project: %v", err)
		}
		logID := func(index int) uuid.UUID {
			return uuid.MustParse(fmt.Sprintf("01938032-1b17-7%03x-8000-000000000001", index))
		}
		for index := 0; index <= 500; index++ {
			if err := proxy.LogRepo.InsertLog(&domain.Log{
				ID: logID(index), Timestamp: time.Unix(int64(500-index), 0).UTC(),
				Level: "INFO", Message: fmt.Sprintf("log %d", index),
			}); err != nil {
				t.Fatalf("inserting log %d: %v", index, err)
			}
		}
		server := newTestServer(proxy, func() {})
		type page struct {
			Items []struct {
				ID uuid.UUID `json:"id"`
			} `json:"items"`
			NextCursor *uuid.UUID `json:"next_cursor"`
		}
		decode := func(response *httptest.ResponseRecorder) page {
			t.Helper()
			var decoded page
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decoding log page: %v", err)
			}
			return decoded
		}

		defaultPage := decode(requestLogs(server, http.MethodGet, "/logs"))
		if len(defaultPage.Items) != 200 || defaultPage.Items[0].ID != logID(500) || defaultPage.Items[199].ID != logID(301) || defaultPage.NextCursor == nil || *defaultPage.NextCursor != logID(301) {
			t.Fatalf("default page did not contain the newest 200 UUIDs: count %d, first %v, last %v, cursor %v", len(defaultPage.Items), defaultPage.Items[0].ID, defaultPage.Items[len(defaultPage.Items)-1].ID, defaultPage.NextCursor)
		}

		maxPage := decode(requestLogs(server, http.MethodGet, "/logs?limit=500"))
		if len(maxPage.Items) != 500 || maxPage.Items[0].ID != logID(500) || maxPage.Items[499].ID != logID(1) || maxPage.NextCursor == nil || *maxPage.NextCursor != logID(1) {
			t.Fatalf("maximum page did not contain the newest 500 UUIDs: count %d, first %v, last %v, cursor %v", len(maxPage.Items), maxPage.Items[0].ID, maxPage.Items[len(maxPage.Items)-1].ID, maxPage.NextCursor)
		}

		lastPage := decode(requestLogs(server, http.MethodGet, "/logs?limit=500&cursor="+logID(1).String()))
		if len(lastPage.Items) != 1 || lastPage.Items[0].ID != logID(0) || lastPage.NextCursor != nil {
			t.Fatalf("wanted final UUID %s with no cursor, got %v and %v", logID(0), lastPage.Items, lastPage.NextCursor)
		}
	})

	t.Run("should reject invalid limit and cursor values", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{LogRepo: &stubLogRepository{}}, func() {})
		for _, query := range []string{"limit=0", "limit=501", "limit=abc", "limit=1.5", "cursor=not-a-uuid"} {
			response := requestLogs(server, http.MethodGet, "/logs?"+query)
			assertLogResponse(t, response, http.StatusBadRequest, "{\"error\":\"bad_request\"}\n")
		}
	})

	t.Run("should page by UUID, read the newly open project, and exclude other logs", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		firstProject := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		secondProject := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		if err := lifecycle.Open(context.Background(), firstProject); err != nil {
			t.Fatalf("opening first project: %v", err)
		}
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		middle := uuid.MustParse("01938030-0000-7000-8000-000000000001")
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		for _, entry := range []*domain.Log{
			{ID: older, Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Level: "DEBUG", Message: "older"},
			{ID: middle, Timestamp: time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC), Level: "INFO", Message: "middle"},
			{ID: newer, Timestamp: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), Level: "FATAL", Message: "newer"},
		} {
			if err := proxy.LogRepo.InsertLog(entry); err != nil {
				t.Fatalf("inserting first project log: %v", err)
			}
		}
		server := newTestServer(proxy, func() {})
		first := requestLogs(server, http.MethodGet, "/logs?limit=2")
		assertLogResponse(t, first, http.StatusOK, "{\"items\":[{\"id\":\"01938032-1b17-7243-b035-e6a9f4645904\",\"timestamp\":\"2026-01-02T03:04:06Z\",\"level\":\"FATAL\",\"message\":\"newer\",\"context\":{},\"request_id\":null,\"extension_id\":null},{\"id\":\"01938030-0000-7000-8000-000000000001\",\"timestamp\":\"2026-01-02T03:04:07Z\",\"level\":\"INFO\",\"message\":\"middle\",\"context\":{},\"request_id\":null,\"extension_id\":null}],\"next_cursor\":\"01938030-0000-7000-8000-000000000001\"}\n")
		olderPage := requestLogs(server, http.MethodGet, "/logs?limit=2&cursor="+middle.String())
		assertLogResponse(t, olderPage, http.StatusOK, "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"timestamp\":\"2026-01-02T03:04:05Z\",\"level\":\"DEBUG\",\"message\":\"older\",\"context\":{},\"request_id\":null,\"extension_id\":null}],\"next_cursor\":null}\n")

		if err := lifecycle.Open(context.Background(), secondProject); err != nil {
			t.Fatalf("opening second project: %v", err)
		}
		secondID := uuid.MustParse("01938033-1b17-7243-b035-e6a9f4645904")
		if err := proxy.LogRepo.InsertLog(&domain.Log{
			ID: secondID, Timestamp: time.Date(2026, 1, 2, 3, 4, 8, 0, time.UTC), Level: "WARN", Message: "second project",
		}); err != nil {
			t.Fatalf("inserting second project log: %v", err)
		}
		instanceLogPath := filepath.Join(dir, "instance.log")
		instanceLog, err := os.Create(instanceLogPath)
		if err != nil {
			t.Fatalf("creating instance log: %v", err)
		}
		proxy.Logger = slog.New(slog.NewTextHandler(instanceLog, nil))
		proxy.Logger.Info("instance operational line")
		if err := instanceLog.Close(); err != nil {
			t.Fatalf("closing instance log: %v", err)
		}
		proxy.Extensions = []*extensions.Runtime{{Logs: []extensions.ExtensionLog{{
			Time: time.Date(2026, 1, 2, 3, 4, 9, 0, time.UTC), Text: "extension print",
		}}}}
		second := requestLogs(server, http.MethodGet, "/logs")
		assertLogResponse(t, second, http.StatusOK, "{\"items\":[{\"id\":\"01938033-1b17-7243-b035-e6a9f4645904\",\"timestamp\":\"2026-01-02T03:04:08Z\",\"level\":\"WARN\",\"message\":\"second project\",\"context\":{},\"request_id\":null,\"extension_id\":null}],\"next_cursor\":null}\n")
	})

	t.Run("should report repository failures", func(t *testing.T) {
		repo := &stubLogRepository{listErr: errors.New("database unavailable")}
		server := newTestServer(&marasi.Proxy{LogRepo: repo}, func() {})
		response := requestLogs(server, http.MethodGet, "/logs")
		assertLogResponse(t, response, http.StatusInternalServerError, "{\"error\":\"internal_server_error\"}\n")
	})

	t.Run("should return method not allowed for non-GET requests", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{LogRepo: &stubLogRepository{}}, func() {})
		for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodOptions} {
			response := requestLogs(server, method, "/logs")
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("wanted %s status %d, got %d", method, http.StatusMethodNotAllowed, response.Code)
			}
		}
	})
}

func requestLogs(server *Server, method, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func assertLogResponse(t *testing.T, response *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if response.Code != status || response.Body.String() != body {
		t.Fatalf("wanted status %d and body %q, got status %d and body %q", status, body, response.Code, response.Body.String())
	}
}
