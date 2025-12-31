package extensions

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func TestRepoLibrary(t *testing.T) {
	testUUID := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")

	tests := []struct {
		name          string
		luaCode       string
		setupRepo     func() *mockTrafficRepo
		validatorFunc func(t *testing.T, repo *mockTrafficRepo, got any)
	}{
		{
			name:    "repo:get_summary should return summaries on success",
			luaCode: `return marasi.repo:get_summary()`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{
					summaryData: []*domain.RequestResponseSummary{
						{
							ID:          testUUID,
							Scheme:      "https",
							Method:      "GET",
							Host:        "example.com",
							Path:        "/test",
							Status:      "200 OK",
							StatusCode:  200,
							ContentType: "application/json",
							Length:      "100",
							Metadata:    map[string]any{"key": "value"},
							RequestedAt: time.Now(),
							RespondedAt: time.Now(),
						},
					},
				}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				arr := asSlice(got)
				if arr == nil || len(arr) != 1 {
					t.Fatalf("\nwanted:\nslice with 1 item\ngot:\n%T", got)
				}
				m := asMap(arr[0])
				if m == nil || m["scheme"] != "https" {
					t.Errorf("\nwanted:\nsummary with scheme=https\ngot:\n%v", m)
				}
			},
		},
		{
			name: "repo:get_summary should error if getting repo fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_summary, marasi.repo)
				if ok then return "expected error" end
				return res
			`,
			setupRepo: nil,
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced error") {
					t.Errorf("\nwanted:\nerror containing 'forced error'\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:get_summary should error if summary retrieval fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_summary, marasi.repo)
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{forceError: true}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced repo error") {
					t.Errorf("\nwanted:\nerror containing 'forced repo error'\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "repo:get_details should return details on valid ID",
			luaCode: `return marasi.repo:get_details("` + testUUID.String() + `")`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{
					rowData: map[uuid.UUID]*domain.RequestResponseRow{
						testUUID: {
							Request:  domain.ProxyRequest{ID: testUUID, Raw: []byte("raw req")},
							Response: domain.ProxyResponse{ID: testUUID, Raw: []byte("raw res")},
							Metadata: map[string]any{"meta": "data"},
							Note:     "test note",
						},
					},
				}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				m := asMap(got)
				if m == nil || m["note"] != "test note" {
					t.Errorf("\nwanted:\ndetails with note='test note'\ngot:\n%v", m)
				}
			},
		},
		{
			name: "repo:get_details should error on invalid UUID",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_details, marasi.repo, "invalid-uuid")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo { return &mockTrafficRepo{} },
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "invalid UUID") {
					t.Errorf("\nwanted:\nerror containing 'invalid UUID'\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:get_details should error if row retrieval fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_details, marasi.repo, "` + testUUID.String() + `")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{forceError: true}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced repo error") {
					t.Errorf("\nwanted:\nerror containing 'forced repo error'\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "repo:get_metadata should return metadata on valid ID",
			luaCode: `return marasi.repo:get_metadata("` + testUUID.String() + `")`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{
					metadataStore: map[uuid.UUID]map[string]any{
						testUUID: {"key": "value"},
					},
				}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				m := asMap(got)
				if !reflect.DeepEqual(m, map[string]any{"key": "value"}) {
					t.Errorf("\nwanted:\nmetadata map\ngot:\n%v", m)
				}
			},
		},
		{
			name: "repo:get_metadata should error on invalid UUID",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_metadata, marasi.repo, "invalid-uuid")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo { return &mockTrafficRepo{} },
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "invalid UUID") {
					t.Errorf("\nwanted:\nerror containing 'invalid UUID'\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:get_metadata should error if metadata retrieval fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_metadata, marasi.repo, "` + testUUID.String() + `")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{forceError: true}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced repo error") {
					t.Errorf("\nwanted:\nerror containing 'forced repo error'\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "repo:set_metadata should succeed on valid inputs",
			luaCode: `marasi.repo:set_metadata("` + testUUID.String() + `", {key = "value"})`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				if repo.metadataStore == nil || repo.metadataStore[testUUID] == nil {
					t.Errorf("\nwanted:\nmetadata stored\ngot:\nno metadata")
				}
				if !reflect.DeepEqual(repo.metadataStore[testUUID], map[string]any{"key": "value"}) {
					t.Errorf("\nwanted:\nmetadata updated\ngot:\n%v", repo.metadataStore[testUUID])
				}
			},
		},
		{
			name: "repo:set_metadata should error on invalid UUID",
			luaCode: `
				local ok, res = pcall(marasi.repo.set_metadata, marasi.repo, "invalid-uuid", {key = "value"})
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo { return &mockTrafficRepo{} },
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "invalid UUID") {
					t.Errorf("\nwanted:\nerror containing 'invalid UUID'\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:set_metadata should error on non-table metadata",
			luaCode: `
				local ok, res = pcall(marasi.repo.set_metadata, marasi.repo, "` + testUUID.String() + `", "not a table")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo { return &mockTrafficRepo{} },
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "metadata must be a key-value table") {
					t.Errorf("\nwanted:\nerror containing 'metadata must be a key-value table'\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:set_metadata should error if update fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.set_metadata, marasi.repo, "` + testUUID.String() + `", {key = "value"})
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{forceError: true}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced repo error") {
					t.Errorf("\nwanted:\nerror containing 'forced repo error'\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "repo:get_note should return note on valid ID",
			luaCode: `return marasi.repo:get_note("` + testUUID.String() + `")`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{
					noteStore: map[uuid.UUID]string{
						testUUID: "test note",
					},
				}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				note, ok := got.(string)
				if !ok || note != "test note" {
					t.Errorf("\nwanted:\n\"test note\"\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:get_note should error on invalid UUID",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_note, marasi.repo, "invalid-uuid")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo { return &mockTrafficRepo{} },
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "invalid UUID") {
					t.Errorf("\nwanted:\nerror containing 'invalid UUID'\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:get_note should error if note retrieval fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.get_note, marasi.repo, "` + testUUID.String() + `")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{forceError: true}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced repo error") {
					t.Errorf("\nwanted:\nerror containing 'forced repo error'\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "repo:set_note should succeed on valid inputs",
			luaCode: `marasi.repo:set_note("` + testUUID.String() + `", "test note")`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				if repo.noteStore == nil || repo.noteStore[testUUID] != "test note" {
					t.Errorf("\nwanted:\nnote stored\ngot:\n%v", repo.noteStore)
				}
			},
		},
		{
			name: "repo:set_note should error on invalid UUID",
			luaCode: `
				local ok, res = pcall(marasi.repo.set_note, marasi.repo, "invalid-uuid", "note")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo { return &mockTrafficRepo{} },
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "invalid UUID") {
					t.Errorf("\nwanted:\nerror containing 'invalid UUID'\ngot:\n%v", got)
				}
			},
		},
		{
			name: "repo:set_note should error if update fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.set_note, marasi.repo, "` + testUUID.String() + `", "note")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{forceError: true}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced repo error") {
					t.Errorf("\nwanted:\nerror containing 'forced repo error'\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "repo:search_by_metadata should return matching summaries",
			luaCode: `return marasi.repo:search_by_metadata("$.key", "value")`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{
					summaryData: []*domain.RequestResponseSummary{
						{
							ID:          testUUID,
							Scheme:      "https",
							Method:      "GET",
							Host:        "example.com",
							Path:        "/search",
							Status:      "200 OK",
							StatusCode:  200,
							ContentType: "application/json",
							Length:      "100",
							Metadata:    map[string]any{"key": "value"},
							RequestedAt: time.Now(),
							RespondedAt: time.Now(),
						},
					},
				}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				arr := asSlice(got)
				if arr == nil || len(arr) != 1 {
					t.Fatalf("\nwanted:\nslice with 1 item\ngot:\n%T", got)
				}
				m := asMap(arr[0])
				if m == nil || m["path"] != "/search" {
					t.Errorf("\nwanted:\nsummary with path=/search\ngot:\n%v", m)
				}
			},
		},
		{
			name: "repo:search_by_metadata should error if repo fails",
			luaCode: `
				local ok, res = pcall(marasi.repo.search_by_metadata, marasi.repo, "$.key", "value")
				if ok then return "expected error" end
				return res
			`,
			setupRepo: func() *mockTrafficRepo {
				return &mockTrafficRepo{forceError: true}
			},
			validatorFunc: func(t *testing.T, repo *mockTrafficRepo, got any) {
				errStr, ok := got.(string)
				if !ok || !strings.Contains(errStr, "forced repo error") {
					t.Errorf("\nwanted:\nerror containing 'forced repo error'\ngot:\n%v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, mockProxy := setupTestExtension(t, "")

			var repo *mockTrafficRepo
			if tt.setupRepo != nil {
				repo = tt.setupRepo()
				mockProxy.GetTrafficRepoFunc = func() (domain.TrafficRepository, error) {
					return repo, nil
				}
			} else {
				mockProxy.GetTrafficRepoFunc = func() (domain.TrafficRepository, error) {
					return nil, errors.New("forced error")
				}
			}

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, repo, got)
			}
		})
	}
}
