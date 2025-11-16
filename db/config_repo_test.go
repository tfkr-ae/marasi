package db

import (
	"reflect"
	"slices"
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

func TestConfigRepo_Filters(t *testing.T) {
	t.Run("should have the default filters", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		got, err := repo.GetFilters()
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(got) == 0 {
			t.Fatalf("wanted filter list to not be empty\ngot: 0")
		}

		if !slices.Contains(got, "image/jpeg") {
			t.Fatalf("wanted default filters to contain image/jpeg")
		}
	})

	t.Run("should update filters", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := []string{"application/json", "text/plain"}

		err := repo.SetFilters(want)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		got, err := repo.GetFilters()
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should be able to set empty filter", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		err := repo.SetFilters([]string{})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFilters()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(got))
		}
	})
}
