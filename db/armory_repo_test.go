package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func armoryTestUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}
	return id
}

func armoryTestTemplate(t *testing.T, repo *Repository, name string) *domain.ArmoryTemplate {
	t.Helper()
	template := &domain.ArmoryTemplate{
		ID:          armoryTestUUID(t),
		Name:        name,
		Description: "Test description",
		RawTemplate: "GET /?value={{.value}} HTTP/1.1\r\nHost: marasi.app\r\n\r\n",
	}
	if err := repo.CreateArmoryTemplate(template); err != nil {
		t.Fatalf("creating armory template: %v", err)
	}
	return template
}

func armoryTestRun(t *testing.T, repo *Repository, template *domain.ArmoryTemplate) *domain.ArmoryRun {
	t.Helper()
	run := &domain.ArmoryRun{
		ID:               armoryTestUUID(t),
		TemplateID:       template.ID,
		TemplateSnapshot: template.RawTemplate,
		UseHTTPS:         true,
		Wordlists:        []string{"values.txt"},
		Status:           domain.ArmoryRunDraft,
		AttackType:       domain.ArmoryAttackHarpoon,
		MaxConcurrent:    5,
		CreatedAt:        time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := repo.CreateArmoryRun(run); err != nil {
		t.Fatalf("creating armory run: %v", err)
	}
	return run
}

func TestArmoryRepo_Templates(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	first := armoryTestTemplate(t, repo, "First")
	second := armoryTestTemplate(t, repo, "Second")
	secondRun := armoryTestRun(t, repo, second)
	requestID := testRequest(t, repo, nil)
	if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: secondRun.ID, RequestID: requestID}); err != nil {
		t.Fatalf("creating armory entry: %v", err)
	}

	got, err := repo.GetArmoryTemplate(first.ID)
	if err != nil {
		t.Fatalf("getting armory template: %v", err)
	}
	if !reflect.DeepEqual(first, got) {
		t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", first, got)
	}

	templates, err := repo.GetArmoryTemplates()
	if err != nil {
		t.Fatalf("getting armory templates: %v", err)
	}
	if len(templates) != 2 {
		t.Fatalf("\nwanted:\n2 templates\ngot:\n%d", len(templates))
	}
	if templates[0].ID != first.ID || templates[1].ID != second.ID {
		t.Fatalf("\nwanted:\n%s, %s\ngot:\n%s, %s", first.ID, second.ID, templates[0].ID, templates[1].ID)
	}

	first.Name = "Updated"
	first.Description = "Updated description"
	first.RawTemplate = "POST / HTTP/1.1\r\nHost: marasi.app\r\n\r\n{{.body}}"
	err = repo.UpdateArmoryTemplate(first)
	if err != nil {
		t.Fatalf("updating armory template: %v", err)
	}
	got, err = repo.GetArmoryTemplate(first.ID)
	if err != nil {
		t.Fatalf("getting updated armory template: %v", err)
	}
	if !reflect.DeepEqual(first, got) {
		t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", first, got)
	}

	if err := repo.DeleteArmoryTemplate(second.ID); err != nil {
		t.Fatalf("deleting armory template: %v", err)
	}
	if _, err := repo.GetArmoryTemplate(second.ID); err == nil {
		t.Fatal("expected error getting deleted armory template")
	}
	if _, err := repo.GetArmoryRun(secondRun.ID); err == nil {
		t.Fatal("expected template delete to cascade to its run")
	}
	entries, err := repo.GetArmoryEntries(secondRun.ID)
	if err != nil {
		t.Fatalf("getting entries after template delete: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected template delete to cascade to entries, got %+v", entries)
	}
}

func TestArmoryRepo_TemplateNotFound(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	template := &domain.ArmoryTemplate{ID: armoryTestUUID(t), Name: "Missing"}
	if _, err := repo.GetArmoryTemplate(template.ID); !errors.Is(err, domain.ErrArmoryTemplateNotFound) {
		t.Fatalf("expected ErrArmoryTemplateNotFound, got %v", err)
	}
	if err := repo.UpdateArmoryTemplate(template); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found update error, got %v", err)
	}
	if err := repo.DeleteArmoryTemplate(template.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found delete error, got %v", err)
	}
}

func TestArmoryRepo_Runs(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	template := armoryTestTemplate(t, repo, "Template")
	run := armoryTestRun(t, repo, template)
	otherTemplate := armoryTestTemplate(t, repo, "Other")
	_ = armoryTestRun(t, repo, otherTemplate)

	got, err := repo.GetArmoryRun(run.ID)
	if err != nil {
		t.Fatalf("getting armory run: %v", err)
	}
	if !reflect.DeepEqual(run, got) {
		t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", run, got)
	}

	runs, err := repo.GetArmoryRuns(template.ID)
	if err != nil {
		t.Fatalf("getting armory runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("expected only run %s, got %+v", run.ID, runs)
	}

	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	run.Status = domain.ArmoryRunInProgress
	run.StartedAt = &startedAt
	err = repo.UpdateArmoryRun(run)
	if err != nil {
		t.Fatalf("updating armory run: %v", err)
	}
	got, err = repo.GetArmoryRun(run.ID)
	if err != nil {
		t.Fatalf("getting updated armory run: %v", err)
	}
	if !reflect.DeepEqual(run, got) {
		t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", run, got)
	}

	if err := repo.DeleteArmoryRun(run.ID); err != nil {
		t.Fatalf("deleting armory run: %v", err)
	}
	if _, err := repo.GetArmoryRun(run.ID); err == nil {
		t.Fatal("expected error getting deleted armory run")
	}
}

func TestArmoryRepo_RunForeignKey(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	run := &domain.ArmoryRun{
		ID:               armoryTestUUID(t),
		TemplateID:       armoryTestUUID(t),
		TemplateSnapshot: "GET / HTTP/1.1\r\nHost: marasi.app\r\n\r\n",
		Wordlists:        []string{},
		Status:           domain.ArmoryRunDraft,
		AttackType:       domain.ArmoryAttackHarpoon,
		MaxConcurrent:    1,
	}
	err := repo.CreateArmoryRun(run)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected foreign key error, got %v", err)
	}
}

func TestArmoryRepo_RunNotFound(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	run := &domain.ArmoryRun{ID: armoryTestUUID(t), Status: domain.ArmoryRunFailed}
	if err := repo.UpdateArmoryRun(run); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found update error, got %v", err)
	}
	if err := repo.DeleteArmoryRun(run.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found delete error, got %v", err)
	}
}

func TestArmoryRepo_Entries(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	template := armoryTestTemplate(t, repo, "Template")
	run := armoryTestRun(t, repo, template)
	firstRequestID := testRequest(t, repo, nil)
	secondRequestID := testRequest(t, repo, nil)

	first := &domain.ArmoryEntry{RunID: run.ID, RequestID: firstRequestID}
	second := &domain.ArmoryEntry{RunID: run.ID, RequestID: secondRequestID}
	if err := repo.CreateArmoryEntry(first); err != nil {
		t.Fatalf("creating first armory entry: %v", err)
	}
	if err := repo.CreateArmoryEntry(second); err != nil {
		t.Fatalf("creating second armory entry: %v", err)
	}

	got, err := repo.GetArmoryEntries(run.ID)
	if err != nil {
		t.Fatalf("getting armory entries: %v", err)
	}
	want := []*domain.ArmoryEntry{first, second}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
	}

	if err := repo.CreateArmoryEntry(first); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("expected duplicate entry error, got %v", err)
	}
}

func TestArmoryRepo_RunTraffic(t *testing.T) {
	t.Run("should page linked traffic oldest first by request id", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		template := armoryTestTemplate(t, repo, "Template")
		run := armoryTestRun(t, repo, template)
		otherRun := armoryTestRun(t, repo, template)
		olderID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		newerID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		otherID := uuid.MustParse("01938033-298e-73dc-b640-eb321b621154")
		for _, id := range []uuid.UUID{newerID, otherID, olderID} {
			err := repo.InsertRequest(&domain.ProxyRequest{
				ID:          id,
				Scheme:      "https",
				Method:      "GET",
				Host:        "example.com",
				Path:        "/" + id.String(),
				Metadata:    map[string]any{"armory_run_id": run.ID.String(), "prettified-request": "omit"},
				RequestedAt: time.Date(2026, time.September, 17, 10, 30, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("inserting request %s: %v", id, err)
			}
		}
		if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: run.ID, RequestID: olderID}); err != nil {
			t.Fatalf("linking older request: %v", err)
		}
		if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: run.ID, RequestID: newerID}); err != nil {
			t.Fatalf("linking newer request: %v", err)
		}
		if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: otherRun.ID, RequestID: otherID}); err != nil {
			t.Fatalf("linking other run request: %v", err)
		}

		firstPage, nextCursor, err := repo.ListArmoryRunTraffic(run.ID, nil, 1)
		if err != nil {
			t.Fatalf("listing first page: %v", err)
		}
		if len(firstPage) != 1 || firstPage[0].ID != olderID || firstPage[0].StatusCode != -1 {
			t.Fatalf("expected older in-flight request, got %+v", firstPage)
		}
		if _, ok := firstPage[0].Metadata["prettified-request"]; ok {
			t.Fatalf("prettified metadata was not removed: %+v", firstPage[0].Metadata)
		}
		if nextCursor == nil || *nextCursor != olderID {
			t.Fatalf("expected next cursor %s, got %v", olderID, nextCursor)
		}

		secondPage, nextCursor, err := repo.ListArmoryRunTraffic(run.ID, nextCursor, 1)
		if err != nil {
			t.Fatalf("listing second page: %v", err)
		}
		if len(secondPage) != 1 || secondPage[0].ID != newerID {
			t.Fatalf("expected newer linked request, got %+v", secondPage)
		}
		if nextCursor != nil {
			t.Fatalf("expected final cursor to be nil, got %v", nextCursor)
		}
	})
}

func TestArmoryRepo_EntryForeignKeys(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	template := armoryTestTemplate(t, repo, "Template")
	run := armoryTestRun(t, repo, template)

	err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: run.ID, RequestID: armoryTestUUID(t)})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected request foreign key error, got %v", err)
	}

	err = repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: armoryTestUUID(t), RequestID: testRequest(t, repo, nil)})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected run foreign key error, got %v", err)
	}
}

func TestArmoryRepo_DeleteRunCascadesEntries(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	template := armoryTestTemplate(t, repo, "Template")
	run := armoryTestRun(t, repo, template)
	requestID := testRequest(t, repo, nil)
	if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: run.ID, RequestID: requestID}); err != nil {
		t.Fatalf("creating armory entry: %v", err)
	}

	if err := repo.DeleteArmoryRun(run.ID); err != nil {
		t.Fatalf("deleting armory run: %v", err)
	}
	entries, err := repo.GetArmoryEntries(run.ID)
	if err != nil {
		t.Fatalf("getting deleted run entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected entries to be deleted, got %d", len(entries))
	}
	if _, err := repo.GetArmoryTemplate(template.ID); err != nil {
		t.Fatalf("expected template to remain: %v", err)
	}
	if _, err := repo.GetRequestResponseRow(requestID); err != nil {
		t.Fatalf("expected captured request to remain: %v", err)
	}
}
