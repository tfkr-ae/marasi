package db

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

func TestClientSettings(t *testing.T) {
	t.Parallel()
	t.Run("should get a value that was set", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		if _, err := repo.SetClientSetting("marasi-app", "ledger.exclusion", []byte(`{"types":["image/png"]}`)); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		got, err := repo.GetClientSetting("marasi-app", "ledger.exclusion")
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if got.Client != "marasi-app" || got.Key != "ledger.exclusion" || string(got.Value) != `{"types":["image/png"]}` {
			t.Fatalf("\nwanted:\nmarasi-app ledger.exclusion {\"types\":[\"image/png\"]}\ngot:\n%s %s %s", got.Client, got.Key, got.Value)
		}
		if got.UpdatedAt.IsZero() {
			t.Fatalf("wanted: updated_at set\ngot: zero")
		}
	})

	t.Run("should keep both keys when two writers interleave", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		const rounds = 50
		var wg sync.WaitGroup
		errs := make(chan error, 2*rounds)
		for _, key := range []string{"ledger.columns", "ledger.exclusion"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range rounds {
					if _, err := repo.SetClientSetting("marasi-app", key, []byte(strconv.Itoa(i))); err != nil {
						errs <- err
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		for _, key := range []string{"ledger.columns", "ledger.exclusion"} {
			got, err := repo.GetClientSetting("marasi-app", key)
			if err != nil {
				t.Fatalf("wanted: nil\ngot: %v", err)
			}
			if string(got.Value) != "49" {
				t.Fatalf("\nwanted:\n%s = 49\ngot:\n%s", key, got.Value)
			}
		}
	})

	t.Run("should replace the value of an existing key", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		mustSetClientSetting(t, repo, "marasi-app", "theme", `"dark"`)
		mustSetClientSetting(t, repo, "marasi-app", "theme", `"light"`)

		got, err := repo.GetClientSetting("marasi-app", "theme")
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if string(got.Value) != `"light"` {
			t.Fatalf("\nwanted:\n\"light\"\ngot:\n%s", got.Value)
		}
	})

	t.Run("should report a missing key as not found", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		_, err := repo.GetClientSetting("marasi-app", "missing")
		if !errors.Is(err, domain.ErrClientSettingNotFound) {
			t.Fatalf("wanted: %v\ngot: %v", domain.ErrClientSettingNotFound, err)
		}
	})

	t.Run("should list one client's keys with a prefix sorted by key", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		mustSetClientSetting(t, repo, "marasi-app", "ledger.b", `2`)
		mustSetClientSetting(t, repo, "marasi-app", "theme", `"dark"`)
		mustSetClientSetting(t, repo, "marasi-app", "ledger.a", `1`)
		mustSetClientSetting(t, repo, "marasi-app", "ledger_c", `3`)
		mustSetClientSetting(t, repo, "other", "ledger.z", `26`)

		got, err := repo.ListClientSettings("marasi-app", "ledger.")
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		var keys []string
		for _, setting := range got {
			keys = append(keys, setting.Key+"="+string(setting.Value))
		}
		want := []string{"ledger.a=1", "ledger.b=2"}
		if !slices.Equal(keys, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, keys)
		}

		all, err := repo.ListClientSettings("marasi-app", "")
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if len(all) != 4 {
			t.Fatalf("wanted: 4 settings\ngot: %d", len(all))
		}
	})

	t.Run("should list nothing as an empty slice", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		got, err := repo.ListClientSettings("marasi-app", "")
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("wanted: empty non-nil slice\ngot: %#v", got)
		}
	})

	t.Run("should delete a key and accept deleting a missing key", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		mustSetClientSetting(t, repo, "marasi-app", "theme", `"dark"`)
		if err := repo.DeleteClientSetting("marasi-app", "theme"); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if _, err := repo.GetClientSetting("marasi-app", "theme"); !errors.Is(err, domain.ErrClientSettingNotFound) {
			t.Fatalf("wanted: %v\ngot: %v", domain.ErrClientSettingNotFound, err)
		}
		if err := repo.DeleteClientSetting("marasi-app", "theme"); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
	})
}

func TestClientSettingValidation(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()

	t.Run("should accept the edges of every rule", func(t *testing.T) {
		for _, tc := range []struct{ name, client, key, value string }{
			{"single character client", "a", "k", `1`},
			{"64 character client", "a" + strings.Repeat("b", 63), "k", `1`},
			{"client with dot underscore dash and digits", "0app.v2_x-y", "k", `1`},
			{"256 byte key", "app", strings.Repeat("k", 256), `1`},
			{"key with slash and spaces", "app", "ledger/columns two", `1`},
			{"non-ASCII printable key", "app", "réglage", `1`},
			{"1 MiB value", "app", "big", `"` + strings.Repeat("x", 1<<20-2) + `"`},
			{"null value", "app", "null", `null`},
		} {
			if _, err := repo.SetClientSetting(tc.client, tc.key, []byte(tc.value)); err != nil {
				t.Errorf("%s: wanted: nil\ngot: %v", tc.name, err)
			}
		}
	})

	t.Run("should reject invalid settings on write", func(t *testing.T) {
		for _, tc := range []struct{ name, client, key, value string }{
			{"empty client", "", "k", `1`},
			{"uppercase client", "Marasi-App", "k", `1`},
			{"client starting with dash", "-app", "k", `1`},
			{"client starting with dot", ".app", "k", `1`},
			{"65 character client", "a" + strings.Repeat("b", 64), "k", `1`},
			{"client with slash", "app/x", "k", `1`},
			{"empty key", "app", "", `1`},
			{"257 byte key", "app", strings.Repeat("k", 257), `1`},
			{"key with NUL", "app", "a\x00b", `1`},
			{"key with newline", "app", "a\nb", `1`},
			{"key with invalid UTF-8", "app", "a\xffb", `1`},
			{"empty value", "app", "k", ``},
			{"invalid JSON value", "app", "k", `{"a":`},
			{"value over 1 MiB", "app", "k", `"` + strings.Repeat("x", 1<<20-1) + `"`},
		} {
			_, err := repo.SetClientSetting(tc.client, tc.key, []byte(tc.value))
			if !errors.Is(err, domain.ErrInvalidClientSetting) {
				t.Errorf("%s: wanted: %v\ngot: %v", tc.name, domain.ErrInvalidClientSetting, err)
			}
		}
	})

	t.Run("should reject an invalid client or key on read and delete", func(t *testing.T) {
		if _, err := repo.GetClientSetting("Bad", "k"); !errors.Is(err, domain.ErrInvalidClientSetting) {
			t.Errorf("get: wanted: %v\ngot: %v", domain.ErrInvalidClientSetting, err)
		}
		if _, err := repo.GetClientSetting("app", ""); !errors.Is(err, domain.ErrInvalidClientSetting) {
			t.Errorf("get key: wanted: %v\ngot: %v", domain.ErrInvalidClientSetting, err)
		}
		if _, err := repo.ListClientSettings("Bad", ""); !errors.Is(err, domain.ErrInvalidClientSetting) {
			t.Errorf("list: wanted: %v\ngot: %v", domain.ErrInvalidClientSetting, err)
		}
		if err := repo.DeleteClientSetting("app", "a\x00"); !errors.Is(err, domain.ErrInvalidClientSetting) {
			t.Errorf("delete: wanted: %v\ngot: %v", domain.ErrInvalidClientSetting, err)
		}
	})

	t.Run("should refuse writes to the reserved client but allow reads and deletes", func(t *testing.T) {
		if _, err := repo.SetClientSetting(domain.ReservedClient, "legacy.content_type_exclusion", []byte(`[]`)); !errors.Is(err, domain.ErrReservedClient) {
			t.Fatalf("wanted: %v\ngot: %v", domain.ErrReservedClient, err)
		}
		if _, err := repo.GetClientSetting(domain.ReservedClient, "legacy.content_type_exclusion"); !errors.Is(err, domain.ErrClientSettingNotFound) {
			t.Fatalf("wanted: %v\ngot: %v", domain.ErrClientSettingNotFound, err)
		}
		if _, err := repo.ListClientSettings(domain.ReservedClient, ""); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if err := repo.DeleteClientSetting(domain.ReservedClient, "legacy.content_type_exclusion"); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
	})
}

func mustSetClientSetting(t *testing.T, repo *Repository, client, key, value string) {
	t.Helper()
	if _, err := repo.SetClientSetting(client, key, []byte(value)); err != nil {
		t.Fatalf("setting %s/%s: %v", client, key, err)
	}
}
