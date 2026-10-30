package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/domain"
)

type erroringReader struct{}

func (er *erroringReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("forced error")
}

func (er *erroringReader) Close() error {
	return nil
}

type mockProxyService struct {
	GetConfigDirFunc     func() (string, error)
	GetScopeFunc         func() (*compass.Scope, error)
	GetClientFunc        func() (*http.Client, error)
	WriteLogFunc         func(level string, message string, options ...func(log *domain.Log) error) error
	GetExtensionRepoFunc func() (domain.ExtensionRepository, error)
	GetTrafficRepoFunc   func() (domain.TrafficRepository, error)
}

func (m *mockProxyService) GetConfigDir() (string, error) {
	if m.GetConfigDirFunc != nil {
		return m.GetConfigDirFunc()
	}
	return "/tmp/marasi-test", nil
}

func (m *mockProxyService) GetScope() (*compass.Scope, error) {
	if m.GetScopeFunc != nil {
		return m.GetScopeFunc()
	}
	return compass.NewScope(true), nil
}

func (m *mockProxyService) GetClient() (*http.Client, error) {
	if m.GetClientFunc != nil {
		return m.GetClientFunc()
	}
	return http.DefaultClient, nil
}

func (m *mockProxyService) WriteLog(level string, message string, options ...func(log *domain.Log) error) error {
	if m.WriteLogFunc != nil {
		return m.WriteLogFunc(level, message, options...)
	}
	return nil
}

func (m *mockProxyService) GetExtensionRepo() (domain.ExtensionRepository, error) {
	if m.GetExtensionRepoFunc != nil {
		return m.GetExtensionRepoFunc()
	}
	return nil, nil
}

func (m *mockProxyService) GetTrafficRepo() (domain.TrafficRepository, error) {
	if m.GetTrafficRepoFunc != nil {
		return m.GetTrafficRepoFunc()
	}
	return nil, nil
}

type mockExtensionRepo struct {
	settingsStore map[uuid.UUID]map[string]any
	forceSetError bool
}

func (m *mockExtensionRepo) GetExtensions() ([]*domain.Extension, error) { return nil, nil }
func (m *mockExtensionRepo) GetExtensionByName(name string) (*domain.Extension, error) {
	return nil, nil
}
func (m *mockExtensionRepo) GetExtensionByUUID(id uuid.UUID) (*domain.Extension, error) {
	return nil, nil
}
func (m *mockExtensionRepo) GetExtensionLuaCodeByName(name string) (string, error)       { return "", nil }
func (m *mockExtensionRepo) UpdateExtensionLuaCodeByName(name string, code string) error { return nil }
func (m *mockExtensionRepo) UpdateExtensionLuaCodeByUUID(id uuid.UUID, code string) error {
	return nil
}

func (m *mockExtensionRepo) GetExtensionSettingsByUUID(id uuid.UUID) (map[string]any, error) {
	if settings, ok := m.settingsStore[id]; ok {
		return settings, nil
	}
	return make(map[string]any), nil
}

func (m *mockExtensionRepo) SetExtensionEnabledByUUID(id uuid.UUID, enabled bool) error {
	return nil
}

func (m *mockExtensionRepo) SetExtensionSettingsByUUID(id uuid.UUID, settings map[string]any) error {
	if m.forceSetError {
		return errors.New("forced set error")
	}
	if m.settingsStore == nil {
		m.settingsStore = make(map[uuid.UUID]map[string]any)
	}
	m.settingsStore[id] = settings
	return nil
}

type mockTrafficRepo struct {
	summaryData   []*domain.RequestResponseSummary
	rowData       map[uuid.UUID]*domain.RequestResponseRow
	metadataStore map[uuid.UUID]map[string]any
	noteStore     map[uuid.UUID]string
	forceError    bool
}

func (m *mockTrafficRepo) NextPairNumber() int64                        { return 0 }
func (m *mockTrafficRepo) InsertRequest(req *domain.ProxyRequest) error { return nil }
func (m *mockTrafficRepo) InsertResponse(res *domain.ProxyResponse) error {
	return nil
}
func (m *mockTrafficRepo) GetResponse(id uuid.UUID) (*domain.ProxyResponse, error) {
	return nil, nil
}

func (m *mockTrafficRepo) GetRequestResponseSummary() ([]*domain.RequestResponseSummary, error) {
	if m.forceError {
		return nil, errors.New("forced repo error")
	}
	if m.summaryData != nil {
		return m.summaryData, nil
	}
	return []*domain.RequestResponseSummary{}, nil
}

func (m *mockTrafficRepo) ListTraffic(q domain.TrafficQuery) (domain.TrafficPage, error) {
	if m.forceError {
		return domain.TrafficPage{}, errors.New("forced repo error")
	}
	complete := map[domain.JobName]bool{domain.JobTrafficIndex: true}
	if m.summaryData != nil {
		return domain.TrafficPage{Items: m.summaryData, Complete: complete}, nil
	}
	return domain.TrafficPage{Items: []*domain.RequestResponseSummary{}, Complete: complete}, nil
}

func (m *mockTrafficRepo) ListTrafficContext(_ context.Context, q domain.TrafficQuery) (domain.TrafficPage, error) {
	return m.ListTraffic(q)
}

func (m *mockTrafficRepo) CountTrafficContext(_ context.Context, q domain.TrafficCountQuery) (domain.TrafficCount, error) {
	return m.CountTraffic(q)
}

func (m *mockTrafficRepo) CountContentTypesContext(context.Context) (domain.ContentTypeCounts, error) {
	return m.CountContentTypes()
}

func (m *mockTrafficRepo) CountTraffic(q domain.TrafficCountQuery) (domain.TrafficCount, error) {
	return domain.TrafficCount{Count: len(m.summaryData), Complete: map[domain.JobName]bool{domain.JobTrafficIndex: true}}, nil
}

func (m *mockTrafficRepo) CountTrafficByFieldContext(_ context.Context, q domain.TrafficFieldCountQuery) (domain.TrafficFieldCounts, error) {
	return m.CountTrafficByField(q)
}

func (m *mockTrafficRepo) CountTrafficByField(domain.TrafficFieldCountQuery) (domain.TrafficFieldCounts, error) {
	return domain.TrafficFieldCounts{Counts: []*domain.TrafficFieldCount{}}, nil
}

func (m *mockTrafficRepo) ListSitemapChildren(context.Context, domain.SitemapQuery) (domain.SitemapPage, error) {
	return domain.SitemapPage{}, errors.New("not implemented")
}

func (m *mockTrafficRepo) ListSitemapLevels(context.Context, domain.SitemapLevelsQuery) (domain.SitemapLevels, error) {
	return domain.SitemapLevels{}, errors.New("not implemented")
}

func (m *mockTrafficRepo) GetRequestResponseRow(id uuid.UUID) (*domain.RequestResponseRow, error) {
	if m.forceError {
		return nil, errors.New("forced repo error")
	}
	if row, ok := m.rowData[id]; ok {
		return row, nil
	}
	return nil, errors.New("row not found")
}

func (m *mockTrafficRepo) GetRequestResponseRows(ids []uuid.UUID) ([]*domain.RequestResponseRow, error) {
	if m.forceError {
		return nil, errors.New("forced repo error")
	}

	reqRows := make([]*domain.RequestResponseRow, 0, len(ids))
	for _, id := range ids {
		if row, ok := m.rowData[id]; ok {
			reqRows = append(reqRows, row)
		}
	}

	return reqRows, nil
}

func (m *mockTrafficRepo) GetMetadata(id uuid.UUID) (map[string]any, error) {
	if m.forceError {
		return nil, errors.New("forced repo error")
	}
	if meta, ok := m.metadataStore[id]; ok {
		return meta, nil
	}
	return make(map[string]any), nil
}

func (m *mockTrafficRepo) UpdateMetadata(metadata map[string]any, ids ...uuid.UUID) error {
	if m.forceError {
		return errors.New("forced repo error")
	}
	if m.metadataStore == nil {
		m.metadataStore = make(map[uuid.UUID]map[string]any)
	}
	for _, id := range ids {
		m.metadataStore[id] = metadata
	}
	return nil
}

func (m *mockTrafficRepo) GetNote(id uuid.UUID) (string, error) {
	if m.forceError {
		return "", errors.New("forced repo error")
	}
	if note, ok := m.noteStore[id]; ok {
		return note, nil
	}
	return "", nil
}

func (m *mockTrafficRepo) UpdateNote(id uuid.UUID, note string) error {
	if m.forceError {
		return errors.New("forced repo error")
	}
	if m.noteStore == nil {
		m.noteStore = make(map[uuid.UUID]string)
	}
	m.noteStore[id] = note
	return nil
}

func (m *mockTrafficRepo) DeleteNote(id uuid.UUID) error {
	if m.forceError {
		return errors.New("forced repo error")
	}
	if _, ok := m.noteStore[id]; !ok {
		return errors.New("note not found")
	}
	delete(m.noteStore, id)
	return nil
}

func (m *mockTrafficRepo) ListNotes(cursor *uuid.UUID, limit int) ([]*domain.RequestResponseSummary, *uuid.UUID, error) {
	if m.forceError {
		return nil, nil, errors.New("forced repo error")
	}
	return []*domain.RequestResponseSummary{}, nil, nil
}

func (m *mockTrafficRepo) SearchByMetadata(path string, value any) ([]*domain.RequestResponseSummary, error) {
	if m.forceError {
		return nil, errors.New("forced repo error")
	}
	if m.summaryData != nil {
		return m.summaryData, nil
	}
	return []*domain.RequestResponseSummary{}, nil
}

func (m *mockTrafficRepo) CountContentTypes() (domain.ContentTypeCounts, error) {
	if m.forceError {
		return domain.ContentTypeCounts{}, errors.New("forced repo error")
	}
	return domain.ContentTypeCounts{Counts: []*domain.ContentTypeCount{}}, nil
}

func (m *mockTrafficRepo) LinkTraffic(domain.Selection, domain.LinkTarget) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) UnlinkTraffic(domain.Selection, domain.LinkTarget) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) SetHighlight(domain.Selection, int) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) ClearHighlight(domain.Selection) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) SetMetadataKey(domain.Selection, string, json.RawMessage) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) ClearMetadataKey(domain.Selection, string) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) AppendNote(domain.Selection, string) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) ClearNotes(domain.Selection) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) DeleteTraffic(domain.Selection) (int, error) {
	return 0, errors.New("not implemented")
}

func (m *mockTrafficRepo) SelectionIncludes(domain.Selection, []uuid.UUID) ([]uuid.UUID, error) {
	return nil, errors.New("not implemented")
}

func setupTestExtension(t *testing.T, luaCode string, options ...func(*Runtime) error) (*Runtime, *mockProxyService) {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generating uuid : %v", err)
	}
	ext := &domain.Extension{
		ID:         id,
		Name:       "test-extension",
		LuaContent: luaCode,
	}
	runtime := &Runtime{Data: ext}

	mockProxy := &mockProxyService{}

	err = runtime.PrepareState(mockProxy, options)
	if err != nil {
		t.Fatalf("preparing state: %v", err)
	}

	return runtime, mockProxy
}
