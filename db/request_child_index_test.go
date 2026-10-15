package db

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// ADR-0034: deleting a pair cascades to every table that references it, so
// each of those tables needs an index that leads with the referencing
// column, or every deleted pair scans the table.
func TestEveryRequestChildIsIndexedByRequest(t *testing.T) {
	conn, err := New(filepath.Join(t.TempDir(), "p.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	var tables []string
	if err := conn.Select(&tables, `SELECT name FROM sqlite_schema WHERE type = 'table' AND sql NOT LIKE 'CREATE VIRTUAL%'`); err != nil {
		t.Fatal(err)
	}
	children := 0
	for _, table := range tables {
		var columns []string
		if err := conn.Select(&columns, `SELECT "from" FROM pragma_foreign_key_list(?) WHERE "table" = 'request'`, table); err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			children++
			var leading int
			if err := conn.Get(&leading, `SELECT count(*) FROM pragma_index_list(?) AS l
				JOIN pragma_index_info(l.name) AS i ON i.seqno = 0
				WHERE i.name = ?`, table, column); err != nil {
				t.Fatal(err)
			}
			if leading == 0 {
				t.Errorf("%s.%s references request but no index leads with it", table, column)
			}
		}
	}
	if children < 4 {
		t.Fatalf("found %d references to request; the schema query is broken", children)
	}
}
