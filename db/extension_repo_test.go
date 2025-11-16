package db

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	compassID    = uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
	checkpointID = uuid.MustParse("01937d13-9632-75b1-9e73-c5129b06fa8c")
	workshopID   = uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43")
)

func TestExtensionRepo_GetExtensions(t *testing.T) {
	t.Run("should return the default extensions", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		extensions, err := repo.GetExtensions()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(extensions) != 3 {
			t.Fatalf("\nwanted:\n3\ngot:\n%d", len(extensions))
		}

		wantNames := map[uuid.UUID]string{
			compassID:    "compass",
			checkpointID: "checkpoint",
			workshopID:   "workshop",
		}

		for _, ext := range extensions {
			if name, ok := wantNames[ext.ID]; !ok || name != ext.Name {
				t.Errorf("unexpected extension: ID %v, Name %s", ext.ID, ext.Name)
			}
		}
	})
}

func TestExtensionRepo_GetExtensionByName(t *testing.T) {
	t.Run("should return a specific extension by name", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantName := "compass"
		wantID := compassID

		ext, err := repo.GetExtensionByName(wantName)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if ext.Name != wantName {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantName, ext.Name)
		}
		if ext.ID != wantID {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantID, ext.ID)
		}
	})

	t.Run("should return an error for a non-existent name", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		_, err := repo.GetExtensionByName("non-existent-ext")
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("\nwanted:\nerror containing 'no rows'\ngot:\n%v", err)
		}
	})
}

func TestExtensionRepo_GetExtensionLuaCodeByName(t *testing.T) {
	t.Run("should return lua code for a specific extension", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		code, err := repo.GetExtensionLuaCodeByName("compass")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !strings.Contains(code, "marasi:scope()") {
			t.Fatalf("\nwanted:\ncode containing 'marasi:scope()'\ngot:\n%s", code)
		}
	})

	t.Run("should return an error for a non-existent name", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		_, err := repo.GetExtensionLuaCodeByName("non-existent-ext")
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("\nwanted:\nerror containing 'no rows'\ngot:\n%v", err)
		}
	})
}

func TestExtensionRepo_UpdateExtensionLuaCodeByName(t *testing.T) {
	t.Run("should update lua code for an existing extension", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantCode := "function processRequest(req) end"
		extName := "compass"

		err := repo.UpdateExtensionLuaCodeByName(extName, wantCode)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		gotCode, err := repo.GetExtensionLuaCodeByName(extName)
		if err != nil {
			t.Fatalf("getting updated code: %v", err)
		}

		if gotCode != wantCode {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantCode, gotCode)
		}
	})

	t.Run("should not fail for a non-existent name", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		err := repo.UpdateExtensionLuaCodeByName("non-existent-ext", "code")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}

func TestExtensionRepo_GetExtensionSettingsByUUID(t *testing.T) {
	t.Run("should get default settings for an extension", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantSettings := make(map[string]any)

		gotSettings, err := repo.GetExtensionSettingsByUUID(compassID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(wantSettings, gotSettings) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantSettings, gotSettings)
		}
	})

	t.Run("should return an error for a non-existent uuid", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

		_, err := repo.GetExtensionSettingsByUUID(nonExistentID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("\nwanted:\nerror containing 'no rows'\ngot:\n%v", err)
		}
	})
}

func TestExtensionRepo_SetExtensionSettingsByUUID(t *testing.T) {
	t.Run("should set settings for an existing extension", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantSettings := map[string]any{
			"key":     "value",
			"enabled": true,
			"num":     float64(123),
		}

		err := repo.SetExtensionSettingsByUUID(compassID, wantSettings)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		gotSettings, err := repo.GetExtensionSettingsByUUID(compassID)
		if err != nil {
			t.Fatalf("getting updated settings: %v", err)
		}

		if !reflect.DeepEqual(wantSettings, gotSettings) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantSettings, gotSettings)
		}
	})

	t.Run("should overwrite existing settings", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		initialSettings := map[string]any{"key": "old_value"}

		err := repo.SetExtensionSettingsByUUID(compassID, initialSettings)
		if err != nil {
			t.Fatalf("setting initial settings: %v", err)
		}

		wantSettings := map[string]any{"key": "new_value", "new_key": true}

		err = repo.SetExtensionSettingsByUUID(compassID, wantSettings)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		gotSettings, err := repo.GetExtensionSettingsByUUID(compassID)
		if err != nil {
			t.Fatalf("getting updated settings: %v", err)
		}

		if !reflect.DeepEqual(wantSettings, gotSettings) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantSettings, gotSettings)
		}
	})

	t.Run("should not fail for a non-existent uuid", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
		settings := map[string]any{"key": "value"}

		err := repo.SetExtensionSettingsByUUID(nonExistentID, settings)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}

func TestExtensionRepo_GetExtensionsOrdering(t *testing.T) {
	t.Run("should return extensions ordered by ID ascending", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		extensions, err := repo.GetExtensions()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(extensions) != 3 {
			t.Fatalf("\nwanted:\n3\ngot:\n%d", len(extensions))
		}

		if extensions[0].ID != compassID || extensions[0].Name != "compass" {
			t.Fatalf("\nwanted:\nindex 0 to be compass\ngot:\n%v", extensions[0].Name)
		}
		if extensions[1].ID != checkpointID || extensions[1].Name != "checkpoint" {
			t.Fatalf("\nwanted:\nindex 0 to be checkpoint\ngot:\n%v", extensions[1].Name)
		}
		if extensions[2].ID != workshopID || extensions[2].Name != "workshop" {
			t.Fatalf("\nwanted:\nindex 0 to be workshop\ngot:\n%v", extensions[2].Name)
		}
	})

	t.Run("should handle inserted extensions and maintain order", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		newExt := dbExtension{
			ID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			Name:        "Marasi Test Extension",
			SourceURL:   "test",
			Author:      "test",
			LuaContent:  "test",
			UpdatedAt:   time.Now(),
			Enabled:     false,
			Description: "test",
			Settings:    Metadata{},
		}
		query := `INSERT INTO extensions (id, name, source_url, author, lua_content, update_at, enabled, description, settings)
				  VALUES (:id, :name, :source_url, :author, :lua_content, :update_at, :enabled, :description, :settings)`
		_, err := repo.dbConn.NamedExec(query, newExt)
		if err != nil {
			t.Fatalf("inserting new extension: %v", err)
		}

		extensions, err := repo.GetExtensions()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(extensions) != 4 {
			t.Fatalf("\nwanted:\n4\ngot:\n%d", len(extensions))
		}

		if extensions[0].ID != newExt.ID || extensions[0].Name != newExt.Name {
			t.Fatalf("\nwanted:\nindex 0 to be Marasi Test Extension\ngot:\n%v", extensions[0].Name)
		}
		if extensions[1].ID != compassID || extensions[1].Name != "compass" {
			t.Fatalf("\nwanted:\nindex 1 to be compass\ngot:\n%v", extensions[1].Name)
		}
	})
}
