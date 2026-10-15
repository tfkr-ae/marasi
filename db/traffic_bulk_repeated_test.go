package db

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestTrafficRepo_BulkOpsCountARepeatedIDOnce(t *testing.T) {
	tests := []struct {
		name string
		// prepare puts the pair in the state the op changes.
		prepare func(t *testing.T, repo *Repository, id uuid.UUID)
		run     func(repo *Repository, sel domain.Selection) (int, error)
		check   func(t *testing.T, repo *Repository, id uuid.UUID)
	}{
		{
			name: "link",
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				launchpadID, err := repo.CreateLaunchpad("repeat", "")
				if err != nil {
					return 0, err
				}
				return repo.LinkTraffic(sel, domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: launchpadID})
			},
		},
		{
			name: "unlink",
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				launchpadID, err := repo.CreateLaunchpad("repeat", "")
				if err != nil {
					return 0, err
				}
				target := domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: launchpadID}
				if _, err := repo.LinkTraffic(sel, target); err != nil {
					return 0, err
				}
				return repo.UnlinkTraffic(sel, target)
			},
		},
		{
			name: "set highlight",
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				return repo.SetHighlight(sel, 2)
			},
		},
		{
			name: "clear highlight",
			prepare: func(t *testing.T, repo *Repository, id uuid.UUID) {
				if _, err := repo.SetHighlight(idSelection(id), 2); err != nil {
					t.Fatalf("SetHighlight() error = %v", err)
				}
			},
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				return repo.ClearHighlight(sel)
			},
		},
		{
			name: "append note",
			prepare: func(t *testing.T, repo *Repository, id uuid.UUID) {
				if err := repo.UpdateNote(id, "seen"); err != nil {
					t.Fatalf("UpdateNote() error = %v", err)
				}
			},
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				return repo.AppendNote(sel, "triaged")
			},
			check: func(t *testing.T, repo *Repository, id uuid.UUID) {
				got, err := repo.GetNote(id)
				if err != nil {
					t.Fatalf("GetNote() error = %v", err)
				}
				if want := "seen\n\ntriaged"; got != want {
					t.Errorf("GetNote() = %q, want %q", got, want)
				}
			},
		},
		{
			name: "clear notes",
			prepare: func(t *testing.T, repo *Repository, id uuid.UUID) {
				if err := repo.UpdateNote(id, "seen"); err != nil {
					t.Fatalf("UpdateNote() error = %v", err)
				}
			},
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				return repo.ClearNotes(sel)
			},
		},
		{
			name: "set metadata key",
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				return repo.SetMetadataKey(sel, "triage", json.RawMessage(`"done"`))
			},
		},
		{
			name: "clear metadata key",
			prepare: func(t *testing.T, repo *Repository, id uuid.UUID) {
				if _, err := repo.SetMetadataKey(idSelection(id), "triage", json.RawMessage(`"done"`)); err != nil {
					t.Fatalf("SetMetadataKey() error = %v", err)
				}
			},
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				return repo.ClearMetadataKey(sel, "triage")
			},
		},
		{
			name: "delete",
			run: func(repo *Repository, sel domain.Selection) (int, error) {
				return repo.DeleteTraffic(sel)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, teardown := setupTestDB(t)
			defer teardown()

			id := insertTraffic(t, repo, "GET", "a.example.com", "/repeat")
			if tt.prepare != nil {
				tt.prepare(t, repo, id)
			}
			count, err := tt.run(repo, idSelection(id, id, id))
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if count != 1 {
				t.Errorf("count = %d, want 1", count)
			}
			if tt.check != nil {
				tt.check(t, repo, id)
			}
		})
	}
}
