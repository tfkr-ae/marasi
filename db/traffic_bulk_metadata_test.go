package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// metadataPair inserts a captured pair with metadata and returns its id.
func metadataPair(t *testing.T, repo *Repository, metadata map[string]any) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: "https", Method: "GET", Host: "a.example.com", Path: "/",
		Raw: []byte("GET / HTTP/1.1\r\n\r\n"), Metadata: metadata, RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	return id
}

func metadataOf(t *testing.T, repo *Repository, id uuid.UUID) map[string]any {
	t.Helper()
	metadata, err := repo.GetMetadata(id)
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	return metadata
}

// decodeJSON decodes a JSON literal for comparison with read metadata.
func decodeJSON(t *testing.T, literal string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(literal), &value); err != nil {
		t.Fatalf("decoding %s: %v", literal, err)
	}
	return value
}

func TestTrafficRepo_SetMetadataKey_LeavesOtherKeys(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	const pairs = 10_000
	ids := make([]uuid.UUID, pairs)
	for i := range ids {
		ids[i] = metadataPair(t, repo, map[string]any{
			"n": i, "extension": "workshop", "a": map[string]any{"keep": i, "b": "old"},
		})
	}
	count, err := repo.SetMetadataKey(domain.Selection{IDs: ids}, "a.b", json.RawMessage(`{"new":[1,true,null]}`))
	if err != nil {
		t.Fatalf("SetMetadataKey() error = %v", err)
	}
	if count != pairs {
		t.Errorf("SetMetadataKey() count = %d, want %d", count, pairs)
	}
	for i, id := range ids {
		want := decodeJSON(t, fmt.Sprintf(`{"n":%d,"extension":"workshop","a":{"keep":%d,"b":{"new":[1,true,null]}}}`, i, i))
		if got := metadataOf(t, repo, id); !reflect.DeepEqual(got, want) {
			t.Fatalf("pair %d metadata:\nwanted:\n%v\ngot:\n%v", i, want, got)
		}
	}
}

func TestTrafficRepo_SetMetadataKey_CountsOnlyChangedPairs(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	already := metadataPair(t, repo, map[string]any{"tag": "x"})
	other := metadataPair(t, repo, map[string]any{"tag": "y"})
	empty := metadataPair(t, repo, map[string]any{})
	sel := domain.Selection{IDs: []uuid.UUID{already, other, empty}}

	count, err := repo.SetMetadataKey(sel, "tag", json.RawMessage(`"x"`))
	if err != nil {
		t.Fatalf("SetMetadataKey() error = %v", err)
	}
	if count != 2 {
		t.Errorf("SetMetadataKey() count = %d, want 2", count)
	}
	count, err = repo.SetMetadataKey(sel, "tag", json.RawMessage(`"x"`))
	if err != nil {
		t.Fatalf("second SetMetadataKey() error = %v", err)
	}
	if count != 0 {
		t.Errorf("second SetMetadataKey() count = %d, want 0", count)
	}
	for _, id := range sel.IDs {
		if got, want := metadataOf(t, repo, id), map[string]any{"tag": "x"}; !reflect.DeepEqual(got, want) {
			t.Errorf("metadata = %v, want %v", got, want)
		}
	}
}

func TestTrafficRepo_SetMetadataKey_NestedParents(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	missing := metadataPair(t, repo, map[string]any{})
	scalar := metadataPair(t, repo, map[string]any{"a": "text"})

	count, err := repo.SetMetadataKey(domain.Selection{IDs: []uuid.UUID{missing, scalar}}, "a.b", json.RawMessage(`1`))
	if err != nil {
		t.Fatalf("SetMetadataKey() error = %v", err)
	}
	// A missing parent is created; a parent that is not an object is left
	// alone, so the pair is not counted.
	if count != 1 {
		t.Errorf("SetMetadataKey() count = %d, want 1", count)
	}
	if got, want := metadataOf(t, repo, missing), decodeJSON(t, `{"a":{"b":1}}`); !reflect.DeepEqual(got, want) {
		t.Errorf("missing parent: metadata = %v, want %v", got, want)
	}
	if got, want := metadataOf(t, repo, scalar), decodeJSON(t, `{"a":"text"}`); !reflect.DeepEqual(got, want) {
		t.Errorf("scalar parent: metadata = %v, want %v", got, want)
	}
}

func TestTrafficRepo_SetMetadataKey_IsSearchable(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	id := metadataPair(t, repo, map[string]any{})
	if _, err := repo.SetMetadataKey(domain.Selection{IDs: []uuid.UUID{id}}, "triage", json.RawMessage(`"zebracorn"`)); err != nil {
		t.Fatalf("SetMetadataKey() error = %v", err)
	}
	for _, query := range []string{`metadata:"zebracorn"`, `metadata.triage = "zebracorn"`} {
		items, _, _, err := listPage(repo, nil, 10, query)
		if err != nil {
			t.Fatalf("%s: error = %v", query, err)
		}
		if got := idsOf(items); !reflect.DeepEqual(got, []uuid.UUID{id}) {
			t.Errorf("%s = %v, want [%s]", query, got, id)
		}
	}
}

func TestTrafficRepo_ClearMetadataKey(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	nested := metadataPair(t, repo, map[string]any{"a": map[string]any{"b": 1, "c": 2}, "d": 3})
	null := metadataPair(t, repo, map[string]any{"a": map[string]any{"b": nil}})
	without := metadataPair(t, repo, map[string]any{"a": map[string]any{"c": 2}})
	sel := domain.Selection{IDs: []uuid.UUID{nested, null, without}}

	count, err := repo.ClearMetadataKey(sel, "a.b")
	if err != nil {
		t.Fatalf("ClearMetadataKey() error = %v", err)
	}
	if count != 2 {
		t.Errorf("ClearMetadataKey() count = %d, want 2", count)
	}
	wants := map[uuid.UUID]map[string]any{
		nested:  decodeJSON(t, `{"a":{"c":2},"d":3}`),
		null:    decodeJSON(t, `{"a":{}}`),
		without: decodeJSON(t, `{"a":{"c":2}}`),
	}
	for id, want := range wants {
		if got := metadataOf(t, repo, id); !reflect.DeepEqual(got, want) {
			t.Errorf("metadata = %v, want %v", got, want)
		}
	}
	count, err = repo.ClearMetadataKey(sel, "a.b")
	if err != nil {
		t.Fatalf("second ClearMetadataKey() error = %v", err)
	}
	if count != 0 {
		t.Errorf("second ClearMetadataKey() count = %d, want 0", count)
	}
}

func TestTrafficRepo_ClearMetadataKey_HasKeySelection(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	var highlighted []uuid.UUID
	for _, value := range []any{"#EF4444", "#123456", 7, nil} {
		highlighted = append(highlighted, metadataPair(t, repo, map[string]any{"highlight": value, "keep": true}))
	}
	plain := metadataPair(t, repo, map[string]any{"keep": true})

	sel := domain.Selection{Query: &domain.SelectionQuery{Text: `metadata.highlight:*`, AsOf: plain}}
	count, err := repo.ClearMetadataKey(sel, "highlight")
	if err != nil {
		t.Fatalf("ClearMetadataKey() error = %v", err)
	}
	if count != len(highlighted) {
		t.Errorf("ClearMetadataKey() count = %d, want %d", count, len(highlighted))
	}
	for _, id := range append(highlighted, plain) {
		if got, want := metadataOf(t, repo, id), map[string]any{"keep": true}; !reflect.DeepEqual(got, want) {
			t.Errorf("metadata = %v, want %v", got, want)
		}
	}
}

func TestTrafficRepo_MetadataKey_RefusesKeys(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	id := metadataPair(t, repo, map[string]any{})
	sel := domain.Selection{IDs: []uuid.UUID{id}}
	for path, named := range map[string]string{
		"prettified-request":  `"prettified-request"`,
		"prettified-response": `"prettified-response"`,
		"has_note":            `"has_note"`,
		"has_note.x":          `"has_note"`,
		"":                    `""`,
		"a..b":                `"a..b"`,
		"a.":                  `"a."`,
		`a"b`:                 `"a\"b"`,
		`a\b`:                 `"a\\b"`,
	} {
		t.Run(path, func(t *testing.T) {
			for name, run := range map[string]func() (int, error){
				"set":   func() (int, error) { return repo.SetMetadataKey(sel, path, json.RawMessage(`1`)) },
				"clear": func() (int, error) { return repo.ClearMetadataKey(sel, path) },
			} {
				_, err := run()
				if !errors.Is(err, domain.ErrInvalidMetadataKey) {
					t.Fatalf("%s: error = %v, want ErrInvalidMetadataKey", name, err)
				}
				if !strings.Contains(err.Error(), named) {
					t.Errorf("%s: error %q does not name %s", name, err, named)
				}
			}
		})
	}
	if got := metadataOf(t, repo, id); len(got) != 0 {
		t.Errorf("metadata = %v, want it unchanged", got)
	}
}
