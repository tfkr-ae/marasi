package db

import (
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
}

func TestArmoryRepo_TemplateNotFound(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	template := &domain.ArmoryTemplate{ID: armoryTestUUID(t), Name: "Missing"}
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
