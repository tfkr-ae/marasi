package extensions

import (
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

type mockExtensionRepo struct {
	settingsStore map[uuid.UUID]map[string]any
	forceSetError bool
}

func (m *mockExtensionRepo) GetExtensions() ([]*domain.Extension, error) { return nil, nil }
func (m *mockExtensionRepo) GetExtensionByName(name string) (*domain.Extension, error) {
	return nil, nil
}
func (m *mockExtensionRepo) GetExtensionLuaCodeByName(name string) (string, error)       { return "", nil }
func (m *mockExtensionRepo) UpdateExtensionLuaCodeByName(name string, code string) error { return nil }

func (m *mockExtensionRepo) GetExtensionSettingsByUUID(id uuid.UUID) (map[string]any, error) {
	if settings, ok := m.settingsStore[id]; ok {
		return settings, nil
	}
	return make(map[string]any), nil
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
