package db

import (
	"testing"
)

func TestConfigRepo_SPKI(t *testing.T) {
	t.Run("should update SPKI", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := "test-spki-hash-value"
		err := repo.UpdateSPKI(want)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		var got string

		err = repo.dbConn.Get(&got, "SELECT spki FROM app LIMIT 1")
		if err != nil {
			t.Fatalf("getting spki from DB : %v", err)
		}

		if want != got {
			t.Fatalf("wanted: %q\ngot: %q", want, got)
		}
	})
}
