package db

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestTrafficRepo_AppendNote_KeepsExistingNotesAsPrefix(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	const pairs = 2_000
	ids := make([]uuid.UUID, pairs)
	existing := map[uuid.UUID]string{}
	for i := range ids {
		ids[i] = insertTraffic(t, repo, "GET", "a.example.com", fmt.Sprintf("/%d", i))
		if i%3 == 0 {
			existing[ids[i]] = fmt.Sprintf("seen %d", i)
			if err := repo.UpdateNote(ids[i], existing[ids[i]]); err != nil {
				t.Fatalf("UpdateNote() error = %v", err)
			}
		}
	}

	sel := domain.Selection{Query: &domain.SelectionQuery{Text: `method = "GET"`, AsOf: ids[pairs-1]}}
	count, err := repo.AppendNote(sel, "triaged")
	if err != nil {
		t.Fatalf("AppendNote() error = %v", err)
	}
	if count != pairs {
		t.Errorf("AppendNote() count = %d, want %d", count, pairs)
	}
	for _, id := range ids {
		want := "triaged"
		if old, ok := existing[id]; ok {
			want = old + "\n\ntriaged"
		}
		got, err := repo.GetNote(id)
		if err != nil {
			t.Fatalf("GetNote(%s) error = %v", id, err)
		}
		if got != want {
			t.Fatalf("GetNote(%s) = %q, want %q", id, got, want)
		}
	}
}

// hasNote reports whether the pair's metadata carries has_note.
func hasNote(t *testing.T, repo *Repository, id uuid.UUID) bool {
	t.Helper()
	metadata, err := repo.GetMetadata(id)
	if err != nil {
		t.Fatalf("GetMetadata(%s) error = %v", id, err)
	}
	_, ok := metadata["has_note"]
	return ok
}

func TestTrafficRepo_AppendNote_UpdatesHasNoteAndSearch(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{name: "noted", request: "GET /a HTTP/1.1\r\n\r\n"},
		{name: "bare", request: "GET /b HTTP/1.1\r\n\r\n"},
		{name: "other", request: "GET /c HTTP/1.1\r\n\r\n"},
	})
	if err := repo.UpdateNote(ids["noted"], "idor candidate"); err != nil {
		t.Fatalf("UpdateNote() error = %v", err)
	}

	count, err := repo.AppendNote(domain.Selection{IDs: []uuid.UUID{ids["noted"], ids["bare"]}}, "confirmed")
	if err != nil {
		t.Fatalf("AppendNote() error = %v", err)
	}
	if count != 2 {
		t.Errorf("AppendNote() count = %d, want 2", count)
	}
	if !hasNote(t, repo, ids["bare"]) {
		t.Errorf("bare pair has_note = false, want true")
	}
	assertListNames(t, repo, ids, "after append", `note:"confirmed"`, "bare,noted")
	assertListNames(t, repo, ids, "after append", `note:"idor candidate"`, "noted")
}

func TestTrafficRepo_AppendNote_RejectsEmptyText(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	id := insertTraffic(t, repo, "GET", "a.example.com", "/")

	_, err := repo.AppendNote(domain.Selection{IDs: []uuid.UUID{id}}, "")
	if !errors.Is(err, domain.ErrEmptyNote) {
		t.Fatalf("AppendNote() error = %v, want ErrEmptyNote", err)
	}
	if hasNote(t, repo, id) {
		t.Errorf("has_note = true after rejected append")
	}
}

func TestTrafficRepo_ClearNotes_CountsOnlyNotedPairs(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{name: "noted", request: "GET /a HTTP/1.1\r\n\r\n"},
		{name: "bare", request: "GET /b HTTP/1.1\r\n\r\n"},
		{name: "kept", request: "GET /c HTTP/1.1\r\n\r\n"},
	})
	for _, name := range []string{"noted", "kept"} {
		if err := repo.UpdateNote(ids[name], "triaged "+name); err != nil {
			t.Fatalf("UpdateNote() error = %v", err)
		}
	}

	sel := domain.Selection{IDs: []uuid.UUID{ids["noted"], ids["bare"]}}
	count, err := repo.ClearNotes(sel)
	if err != nil {
		t.Fatalf("ClearNotes() error = %v", err)
	}
	if count != 1 {
		t.Errorf("ClearNotes() count = %d, want 1", count)
	}
	if _, err := repo.GetNote(ids["noted"]); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetNote(noted) error = %v, want sql.ErrNoRows", err)
	}
	if hasNote(t, repo, ids["noted"]) {
		t.Errorf("noted pair has_note = true after clear")
	}
	if !hasNote(t, repo, ids["kept"]) {
		t.Errorf("kept pair has_note = false, want true")
	}
	assertListNames(t, repo, ids, "after clear", `note:"triaged"`, "kept")

	count, err = repo.ClearNotes(sel)
	if err != nil {
		t.Fatalf("second ClearNotes() error = %v", err)
	}
	if count != 0 {
		t.Errorf("second ClearNotes() count = %d, want 0", count)
	}
}
