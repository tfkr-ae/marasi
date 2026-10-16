package db

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// reservedMetadataKeys are the top-level metadata keys only Marasi writes, so
// a bulk set or clear refuses them.
var reservedMetadataKeys = map[string]bool{
	"prettified-request":  true,
	"prettified-response": true,
	"has_note":            true,
}

// metadataKeyPath converts a dotted key path, as written in a query, to a
// SQLite JSON path: a.b becomes $."a"."b". A path with an empty segment, a
// segment with a quote or backslash, or a reserved top-level key wraps
// domain.ErrInvalidMetadataKey.
func metadataKeyPath(path string) (string, error) {
	keys := strings.Split(path, ".")
	if reservedMetadataKeys[keys[0]] {
		return "", fmt.Errorf("%w: %q is written only by Marasi", domain.ErrInvalidMetadataKey, keys[0])
	}
	var b strings.Builder
	b.WriteString("$")
	for _, key := range keys {
		if key == "" {
			return "", fmt.Errorf("%w: %q has an empty key", domain.ErrInvalidMetadataKey, path)
		}
		if strings.ContainsAny(key, `"\`) {
			return "", fmt.Errorf("%w: %q cannot contain a quote or backslash", domain.ErrInvalidMetadataKey, key)
		}
		b.WriteString(`."`)
		b.WriteString(key)
		b.WriteString(`"`)
	}
	return b.String(), nil
}

// SetMetadataKey sets the key at path to value on every pair in sel and
// returns the number of pairs whose metadata changed.
func (repo *Repository) SetMetadataKey(sel domain.Selection, path string, value json.RawMessage) (int, error) {
	jsonPath, err := metadataKeyPath(path)
	if err != nil {
		return 0, err
	}
	if !json.Valid(value) {
		return 0, fmt.Errorf("setting metadata key %q: value is not JSON", path)
	}
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		// Only rows whose document changes are written, so the count is
		// exact and unchanged rows are not re-indexed.
		query := `UPDATE request SET metadata = json_set(coalesce(metadata, '{}'), ?, json(?))
			WHERE id IN (` + selected + `)
			AND json_set(coalesce(metadata, '{}'), ?, json(?)) IS NOT json(coalesce(metadata, '{}'))
			RETURNING id`
		queryArgs := append([]any{jsonPath, string(value)}, args...)
		queryArgs = append(queryArgs, jsonPath, string(value))
		return updateAndIndex(tx, path, fmt.Sprintf("setting metadata key %q", path), query, queryArgs...)
	})
}

// ClearMetadataKey removes the key at path from every pair in sel and
// returns the number of pairs that had it.
func (repo *Repository) ClearMetadataKey(sel domain.Selection, path string) (int, error) {
	jsonPath, err := metadataKeyPath(path)
	if err != nil {
		return 0, err
	}
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		query := `UPDATE request SET metadata = json_remove(metadata, ?)
			WHERE id IN (` + selected + `) AND json_type(metadata, ?) IS NOT NULL
			RETURNING id`
		queryArgs := append([]any{jsonPath}, args...)
		queryArgs = append(queryArgs, jsonPath)
		return updateAndIndex(tx, path, fmt.Sprintf("clearing metadata key %q", path), query, queryArgs...)
	})
}

// updateAndIndex runs an UPDATE ... RETURNING id of the metadata key at
// path in tx, re-indexes the metadata of every pair it returns in the
// annotation index, recounts them when the key is the intercepted key, and
// returns how many there were.
func updateAndIndex(tx *sqlx.Tx, path, what, query string, args ...any) (int, error) {
	var ids []uuid.UUID
	if err := tx.Select(&ids, query, args...); err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	for _, id := range ids {
		if err := indexAnnotations(tx, id); err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
	}
	if strings.Split(path, ".")[0] == interceptedKey {
		if err := syncIntercepted(tx, ids...); err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
	}
	return len(ids), nil
}
