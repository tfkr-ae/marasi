package extensions

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestSettingsLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		setupRepo     func() *mockExtensionRepo
		validatorFunc func(t *testing.T, extensionID uuid.UUID, repo *mockExtensionRepo, got any)
	}{
		{
			name: "settings:get should return empty table by default",
			luaCode: `
				return marasi.settings:get()
			`,
			setupRepo: func() *mockExtensionRepo {
				return &mockExtensionRepo{settingsStore: make(map[uuid.UUID]map[string]any)}
			},
			validatorFunc: func(t *testing.T, extensionID uuid.UUID, repo *mockExtensionRepo, got any) {
				m := asMap(got)
				if m == nil {
					t.Fatalf("\nwanted:\nmap[string]any\ngot:\n%T", got)
				}
				if len(m) != 0 {
					t.Errorf("\nwanted:\nempty map\ngot:\n%v", m)
				}
			},
		},
		{
			name: "settings:set should update repository",
			luaCode: `
				local ok = marasi.settings:set({enabled = true, count = 123, list = {1,2,3}, sub = {marasi = true}})
				return ok
			`,
			setupRepo: func() *mockExtensionRepo {
				return &mockExtensionRepo{settingsStore: make(map[uuid.UUID]map[string]any)}
			},
			validatorFunc: func(t *testing.T, extensionID uuid.UUID, repo *mockExtensionRepo, got any) {
				ok, isBool := got.(bool)
				if !isBool || !ok {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", got)
				}

				if len(repo.settingsStore[extensionID]) == 0 {
					t.Fatal("\nwanted:\nrepository update\ngot:\nno update")
				}

				want := map[string]any{
					"enabled": true,
					"count":   123.0,
					"list":    []any{1.0, 2.0, 3.0},
					"sub":     map[string]any{"marasi": true},
				}
				if !reflect.DeepEqual(want, repo.settingsStore[extensionID]) {
					t.Errorf("\nwanted:\n%#v\ngot:\n%#v", want, repo.settingsStore[extensionID])
				}
			},
		},
		{
			name: "settings:set and settings:get roundtrip should return the correct settings under the extensionID",
			luaCode: `
				marasi.settings:set({enabled = true, count = 123, list = {1,2,3}, sub = {marasi = true}})
				return marasi.settings:get()
			`,
			setupRepo: func() *mockExtensionRepo {
				return &mockExtensionRepo{settingsStore: make(map[uuid.UUID]map[string]any)}
			},
			validatorFunc: func(t *testing.T, extensionID uuid.UUID, repo *mockExtensionRepo, got any) {
				m := asMap(got)

				if m == nil {
					t.Fatalf("\nwanted:\nmap[string]any\ngot:\n%T", got)
				}

				want := map[uuid.UUID]map[string]any{
					extensionID: {
						"enabled": true,
						"count":   123.0,
						"list":    []any{1.0, 2.0, 3.0},
						"sub":     map[string]any{"marasi": true},
					},
				}
				if !reflect.DeepEqual(want, repo.settingsStore) {
					t.Errorf("\nwanted:\n%#v\ngot:\n%#v", want, repo.settingsStore)
				}
			},
		},
		{
			name: "settings:set should error on invalid input types",
			luaCode: `
				local ok, res = pcall(marasi.settings.set, marasi.settings, "not a table")
				if ok then
					return "expected error"
				end
				return res
			`,
			setupRepo: func() *mockExtensionRepo {
				return &mockExtensionRepo{settingsStore: make(map[uuid.UUID]map[string]any)}
			},
			validatorFunc: func(t *testing.T, extensionID uuid.UUID, repo *mockExtensionRepo, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "getting table(map) got") {
					t.Errorf("\nwanted:\nerror containing 'getting table(map)'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "settings:get should error if repo fails",
			luaCode: `
				local ok, res = pcall(marasi.settings.get, marasi.settings)
				if ok then
					return "expected error"
				end
				return res
			`,
			setupRepo: nil,
			validatorFunc: func(t *testing.T, extensionID uuid.UUID, repo *mockExtensionRepo, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "forced error") {
					t.Errorf("\nwanted:\nerror containing 'forced error'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "settings:set should error if repo write fails",
			luaCode: `
				local ok, res = pcall(marasi.settings.set, marasi.settings, {enabled = true})
				if ok then
					return "expected error"
				end
				return res
			`,
			setupRepo: func() *mockExtensionRepo {
				return &mockExtensionRepo{
					settingsStore: make(map[uuid.UUID]map[string]any),
					forceSetError: true,
				}
			},
			validatorFunc: func(t *testing.T, extensionID uuid.UUID, repo *mockExtensionRepo, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "forced set error") {
					t.Errorf("\nwanted:\nerror containing 'forced set error'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, mockProxy := setupTestExtension(t, "")

			var repo *mockExtensionRepo
			if tt.setupRepo != nil {
				repo = tt.setupRepo()
				mockProxy.GetExtensionRepoFunc = func() (domain.ExtensionRepository, error) {
					return repo, nil
				}
			} else {
				mockProxy.GetExtensionRepoFunc = func() (domain.ExtensionRepository, error) {
					return nil, errors.New("forced error")
				}
			}

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := goValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension.Data.ID, repo, got)
			}
		})
	}
}
