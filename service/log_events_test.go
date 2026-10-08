package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
)

func TestStoredProxyLogEvent(t *testing.T) {
	lifecycle, proxy, dir := newTestProjectLifecycle(t)
	project := canonicalProjectPath(t, filepath.Join(dir, "events.marasi"))
	if err := lifecycle.Open(t.Context(), project); err != nil {
		t.Fatalf("opening project: %v", err)
	}

	callbackLogs := make(chan domain.Log, 1)
	proxy.OnLog = func(entry domain.Log) error {
		callbackLogs <- entry
		return nil
	}
	server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "test-version", "test-instance", project)
	reader := connectServiceEvents(t, server)

	startProxyDBWriter(t, proxy)

	if err := proxy.WriteLog("ERROR", "stored proxy log"); err != nil {
		t.Fatalf("queueing proxy log: %v", err)
	}
	event := nextHTTPServiceEvent(t, reader)
	if event.name != "log.added" {
		t.Fatalf("wanted log.added event, got %q", event.name)
	}
	eventData := event.data

	var callbackLog domain.Log
	select {
	case callbackLog = <-callbackLogs:
	case <-time.After(time.Second):
		t.Fatal("proxy's existing log callback did not run")
	}
	if callbackLog.Message != "stored proxy log" {
		t.Fatalf("wanted callback message %q, got %q", "stored proxy log", callbackLog.Message)
	}

	page := oneProxyLogPage(t, server)
	if page.Items[0].ID != callbackLog.ID {
		t.Fatalf("wanted stored callback log %s in page, got %+v", callbackLog.ID, page.Items)
	}
	assertProxyLogEventMatchesListItem(t, eventData, page.Items[0])
}

func TestProxyLogEventsFollowOpenProject(t *testing.T) {
	lifecycle, proxy, dir := newTestProjectLifecycle(t)
	firstProject := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
	secondProject := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
	if err := lifecycle.Open(t.Context(), firstProject); err != nil {
		t.Fatalf("opening first project: %v", err)
	}
	oldLogID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	if err := proxy.LogRepo.InsertLog(&domain.Log{
		ID: oldLogID, Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Level: "INFO", Message: "old project log",
	}); err != nil {
		t.Fatalf("inserting old project log: %v", err)
	}

	server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "test-version", "test-instance", firstProject)
	reader := connectServiceEvents(t, server)
	if err := lifecycle.Open(t.Context(), secondProject); err != nil {
		t.Fatalf("opening second project: %v", err)
	}
	if event := nextHTTPServiceEvent(t, reader); event.name != "project.opened" {
		t.Fatalf("wanted project.opened after switch, got %q", event.name)
	}

	if response := requestLogs(server, http.MethodGet, "/logs"); response.Body.String() != "{\"items\":[],\"next_cursor\":null}\n" {
		t.Fatalf("wanted empty logs for new project, got %s", response.Body.String())
	}

	var instanceLog bytes.Buffer
	proxy.Logger = slog.New(slog.NewTextHandler(&instanceLog, nil))
	proxy.Logger.Info("instance operational line")
	extensionID := workshopExtensionID(t, proxy)
	runtime := &extensions.Runtime{Data: &domain.Extension{
		ID: extensionID, Name: "workshop",
		LuaContent: `function print_only() print("extension print") end
function store_proxy_log() marasi:log("stored from Lua", "WARN") end`,
	}}
	extensionService := stagedExtensionService{
		configDir:  dir,
		client:     proxy.Client,
		repository: lifecycle.open.resources.Repository,
		scope:      lifecycle.open.resources.Scope,
	}
	if err := runtime.PrepareState(extensionService, nil); err != nil {
		t.Fatalf("preparing extension: %v", err)
	}
	if err := runtime.CallFunction("print_only"); err != nil {
		t.Fatalf("printing extension log: %v", err)
	}
	if len(runtime.Logs) != 1 || runtime.Logs[0].Text != "extension print" {
		t.Fatalf("extension print was not captured: %+v", runtime.Logs)
	}
	if !strings.Contains(instanceLog.String(), "instance operational line") {
		t.Fatalf("instance log line was not written: %q", instanceLog.String())
	}
	if err := runtime.CallFunction("store_proxy_log"); err != nil {
		t.Fatalf("storing proxy log from extension: %v", err)
	}
	event := nextHTTPServiceEvent(t, reader)
	if event.name != "log.added" {
		t.Fatalf("wanted log.added after Lua log, got %q", event.name)
	}
	var added proxyLogResponse
	if err := json.Unmarshal(event.data, &added); err != nil {
		t.Fatalf("decoding log.added: %v", err)
	}
	if added.Message != "stored from Lua" || added.Level != "WARN" || added.ExtensionID == nil || *added.ExtensionID != extensionID {
		t.Fatalf("unexpected Lua log event: %+v", added)
	}
	page := oneProxyLogPage(t, server)
	if page.Items[0].ID != added.ID || page.Items[0].ExtensionID == nil || *page.Items[0].ExtensionID != extensionID {
		t.Fatalf("new project's stored Lua log missing or old log replayed: %+v", page.Items)
	}
	assertProxyLogEventMatchesListItem(t, event.data, page.Items[0])
}

func TestProjectStartupLogsAreAnnouncedAfterOpen(t *testing.T) {
	lifecycle, proxy, dir := newTestProjectLifecycle(t)
	firstProject := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
	secondProject := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
	if err := lifecycle.Open(t.Context(), firstProject); err != nil {
		t.Fatalf("opening first project: %v", err)
	}
	server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "test-version", "test-instance", firstProject)
	reader := connectServiceEvents(t, server)
	if err := lifecycle.Open(t.Context(), secondProject); err != nil {
		t.Fatalf("opening second project: %v", err)
	}
	if event := nextHTTPServiceEvent(t, reader); event.name != "project.opened" {
		t.Fatalf("wanted project.opened after initial switch, got %q", event.name)
	}

	workshopID := workshopExtensionID(t, proxy)
	startupLua := `function startup() marasi:log("startup proxy log", "INFO") end`
	if err := proxy.ExtensionRepo.UpdateExtensionLuaCodeByUUID(workshopID, startupLua); err != nil {
		t.Fatalf("setting second project startup log: %v", err)
	}
	if err := lifecycle.Open(t.Context(), firstProject); err != nil {
		t.Fatalf("switching back to first project: %v", err)
	}
	if event := nextHTTPServiceEvent(t, reader); event.name != "project.opened" {
		t.Fatalf("wanted project.opened when switching back, got %q", event.name)
	}
	if err := lifecycle.Open(t.Context(), secondProject); err != nil {
		t.Fatalf("reopening second project: %v", err)
	}
	if event := nextHTTPServiceEvent(t, reader); event.name != "project.opened" {
		t.Fatalf("wanted project.opened before startup log, got %q", event.name)
	}
	added := nextHTTPServiceEvent(t, reader)
	if added.name != "log.added" {
		t.Fatalf("wanted log.added after project.opened, got %q", added.name)
	}
	var response proxyLogResponse
	if err := json.Unmarshal(added.data, &response); err != nil {
		t.Fatalf("decoding startup log event: %v", err)
	}
	if response.Message != "startup proxy log" {
		t.Fatalf("wanted startup proxy log, got %+v", response)
	}
	page := oneProxyLogPage(t, server)
	if page.Items[0].ID != response.ID {
		t.Fatalf("startup event was not visible in the newly open project: %+v", page.Items)
	}
	assertProxyLogEventMatchesListItem(t, added.data, page.Items[0])
}

func TestFailedProxyLogInsertDoesNotPublish(t *testing.T) {
	lifecycle, proxy, dir := newTestProjectLifecycle(t)
	project := canonicalProjectPath(t, filepath.Join(dir, "failed.marasi"))
	if err := lifecycle.Open(t.Context(), project); err != nil {
		t.Fatalf("opening project: %v", err)
	}
	server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "test-version", "test-instance", project)
	reader := connectServiceEvents(t, server)
	callbackLogs := make(chan domain.Log, 1)
	proxy.OnLog = func(entry domain.Log) error {
		callbackLogs <- entry
		return nil
	}
	logRepository := &eventLogRepository{
		RepositoryProvider: failedLogInsertRepository{RepositoryProvider: lifecycle.open.resources.Repository},
		onStored:           lifecycle.publishLogAdded,
	}
	logRepository.startPublishing()
	proxy.LogRepo = logRepository
	startProxyDBWriter(t, proxy)
	if err := proxy.WriteLog("ERROR", "not stored"); err != nil {
		t.Fatalf("queueing proxy log: %v", err)
	}
	select {
	case <-callbackLogs:
	case <-time.After(time.Second):
		t.Fatal("proxy's existing log callback did not run")
	}
	proxy.LogRepo = lifecycle.open.resources.Repository
	if err := proxy.WriteLog("INFO", "stored after failure"); err != nil {
		t.Fatalf("queueing successful proxy log: %v", err)
	}
	event := nextHTTPServiceEvent(t, reader)
	if event.name != "log.added" {
		t.Fatalf("wanted successful log.added, got %s %s", event.name, event.data)
	}
	var added proxyLogResponse
	if err := json.Unmarshal(event.data, &added); err != nil {
		t.Fatalf("decoding successful proxy log event: %v", err)
	}
	if added.Message != "stored after failure" {
		t.Fatalf("failed insert was announced: %+v", added)
	}
	page := oneProxyLogPage(t, server)
	if page.Items[0].ID != added.ID {
		t.Fatalf("wanted only successful log %s in page, got %+v", added.ID, page.Items)
	}
}

type failedLogInsertRepository struct {
	marasi.RepositoryProvider
}

func (repository failedLogInsertRepository) InsertLog(*domain.Log) error {
	return errors.New("database unavailable")
}

func connectServiceEvents(t *testing.T, server *Server) *bufio.Reader {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	stream, reader := connectEventStream(t, httpServer.URL)
	t.Cleanup(func() { _ = stream.Body.Close() })
	return reader
}

func nextHTTPServiceEvent(t *testing.T, reader *bufio.Reader) serviceEvent {
	t.Helper()
	frame := readEventFrame(t, reader)
	nameLine, rest, ok := strings.Cut(frame, "\n")
	if !ok || !strings.HasPrefix(nameLine, "event: ") {
		t.Fatalf("invalid service event frame %q", frame)
	}
	dataLine, _, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("invalid service event frame %q", frame)
	}
	return serviceEvent{name: strings.TrimPrefix(nameLine, "event: "), data: []byte(strings.TrimPrefix(dataLine, "data: "))}
}

func startProxyDBWriter(t *testing.T, proxy *marasi.Proxy) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		proxy.WriteToDB()
		close(done)
	}()
	t.Cleanup(func() {
		close(proxy.DBWriteChannel)
		<-done
	})
}

func workshopExtensionID(t *testing.T, proxy *marasi.Proxy) uuid.UUID {
	t.Helper()
	for _, runtime := range proxy.Extensions {
		if runtime.Data.Name == "workshop" {
			return runtime.Data.ID
		}
	}
	t.Fatal("workshop extension was not loaded")
	return uuid.Nil
}

func oneProxyLogPage(t *testing.T, server *Server) proxyLogList {
	t.Helper()
	response := requestLogs(server, http.MethodGet, "/logs")
	if response.Code != http.StatusOK {
		t.Fatalf("listing proxy logs: %d %s", response.Code, response.Body.String())
	}
	var page proxyLogList
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decoding proxy log page: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("wanted one proxy log, got %+v", page.Items)
	}
	return page
}

func assertProxyLogEventMatchesListItem(t *testing.T, eventData []byte, item proxyLogResponse) {
	t.Helper()
	listItem, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("encoding proxy log list item: %v", err)
	}
	if !bytes.Equal(eventData, listItem) {
		t.Fatalf("event payload %s did not match list item %s", eventData, listItem)
	}
}
