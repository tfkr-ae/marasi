package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.ArmoryRepository = (*Repository)(nil)

// dbArmoryTemplate represents an Armory template stored in SQLite.
type dbArmoryTemplate struct {
	// ID is the template primary key.
	ID uuid.UUID `db:"id"`
	// Name is the template display name.
	Name string `db:"name"`
	// Description explains the purpose of the template.
	Description string `db:"description"`
	// RawTemplate contains the raw HTTP Go template.
	RawTemplate string `db:"raw_template"`
}

// dbArmoryRun represents an Armory run stored in SQLite.
type dbArmoryRun struct {
	// ID is the run primary key.
	ID uuid.UUID `db:"id"`
	// TemplateID references the source Armory template.
	TemplateID uuid.UUID `db:"template_id"`
	// TemplateSnapshot preserves the raw template used for the run.
	TemplateSnapshot string `db:"template_snapshot"`
	// UseHTTPS records whether generated requests use HTTPS.
	UseHTTPS bool `db:"use_https"`
	// Wordlists contains the ordered wordlist names as JSON.
	Wordlists StringArray `db:"wordlists"`
	// Status is the persisted run lifecycle state.
	Status string `db:"status"`
	// AttackType identifies the payload application strategy.
	AttackType string `db:"attack_type"`
	// MaxConcurrent limits requests executing at once.
	MaxConcurrent int `db:"max_concurrent"`
	// CreatedAt is when the run was created.
	CreatedAt time.Time `db:"created_at"`
	// StartedAt is the nullable execution start time.
	StartedAt sql.NullTime `db:"started_at"`
	// FinishedAt is the nullable execution finish time.
	FinishedAt sql.NullTime `db:"finished_at"`
}

// dbArmoryEntry represents a stored association between a run and a request.
type dbArmoryEntry struct {
	// RunID references the Armory run.
	RunID uuid.UUID `db:"run_id"`
	// RequestID references the generated proxy request.
	RequestID uuid.UUID `db:"request_id"`
}

// toDomainArmoryTemplate converts a database template to its domain representation.
func toDomainArmoryTemplate(template *dbArmoryTemplate) *domain.ArmoryTemplate {
	return &domain.ArmoryTemplate{
		ID:          template.ID,
		Name:        template.Name,
		Description: template.Description,
		RawTemplate: template.RawTemplate,
	}
}

// toDomainArmoryRun converts a database run to its domain representation.
func toDomainArmoryRun(run *dbArmoryRun) *domain.ArmoryRun {
	domainRun := &domain.ArmoryRun{
		ID:               run.ID,
		TemplateID:       run.TemplateID,
		TemplateSnapshot: run.TemplateSnapshot,
		UseHTTPS:         run.UseHTTPS,
		Wordlists:        []string(run.Wordlists),
		Status:           domain.ArmoryRunStatus(run.Status),
		AttackType:       domain.ArmoryAttackType(run.AttackType),
		MaxConcurrent:    run.MaxConcurrent,
		CreatedAt:        run.CreatedAt,
	}
	if domainRun.Wordlists == nil {
		domainRun.Wordlists = make([]string, 0)
	}
	if run.StartedAt.Valid {
		domainRun.StartedAt = &run.StartedAt.Time
	}
	if run.FinishedAt.Valid {
		domainRun.FinishedAt = &run.FinishedAt.Time
	}
	return domainRun
}

// GetArmoryTemplates returns all Armory templates in creation order.
func (repo *Repository) GetArmoryTemplates() ([]*domain.ArmoryTemplate, error) {
	var templates []*dbArmoryTemplate
	query := `SELECT id, name, description, raw_template FROM armory_template ORDER BY id ASC`
	if err := repo.dbConn.Select(&templates, query); err != nil {
		return nil, fmt.Errorf("getting armory templates: %w", err)
	}

	result := make([]*domain.ArmoryTemplate, len(templates))
	for i, template := range templates {
		result[i] = toDomainArmoryTemplate(template)
	}
	return result, nil
}

// GetArmoryTemplate returns an Armory template by ID.
func (repo *Repository) GetArmoryTemplate(id uuid.UUID) (*domain.ArmoryTemplate, error) {
	var template dbArmoryTemplate
	query := `SELECT id, name, description, raw_template FROM armory_template WHERE id = ?`
	if err := repo.dbConn.Get(&template, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", domain.ErrArmoryTemplateNotFound, id)
		}
		return nil, fmt.Errorf("getting armory template %s: %w", id, err)
	}
	return toDomainArmoryTemplate(&template), nil
}

// CreateArmoryTemplate inserts an Armory template.
func (repo *Repository) CreateArmoryTemplate(template *domain.ArmoryTemplate) error {
	query := `INSERT INTO armory_template (id, name, description, raw_template) VALUES (?, ?, ?, ?)`
	_, err := repo.dbConn.Exec(query, template.ID, template.Name, template.Description, template.RawTemplate)
	if err != nil {
		return fmt.Errorf("creating armory template %s: %w", template.Name, err)
	}
	return nil
}

// UpdateArmoryTemplate updates all editable fields of an Armory template.
func (repo *Repository) UpdateArmoryTemplate(template *domain.ArmoryTemplate) error {
	query := `UPDATE armory_template SET name = ?, description = ?, raw_template = ? WHERE id = ?`
	result, err := repo.dbConn.Exec(query, template.Name, template.Description, template.RawTemplate, template.ID)
	if err != nil {
		return fmt.Errorf("updating armory template %s: %w", template.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("getting updated armory template rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", domain.ErrArmoryTemplateNotFound, template.ID)
	}
	return nil
}

// DeleteArmoryTemplate deletes an Armory template by ID.
func (repo *Repository) DeleteArmoryTemplate(id uuid.UUID) error {
	result, err := repo.dbConn.Exec(`DELETE FROM armory_template WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting armory template %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("getting deleted armory template rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", domain.ErrArmoryTemplateNotFound, id)
	}
	return nil
}

// GetArmoryRun returns an Armory run by ID.
func (repo *Repository) GetArmoryRun(id uuid.UUID) (*domain.ArmoryRun, error) {
	var run dbArmoryRun
	query := `SELECT * FROM armory_run WHERE id = ?`
	if err := repo.dbConn.Get(&run, query, id); err != nil {
		return nil, fmt.Errorf("getting armory run %s: %w", id, err)
	}
	return toDomainArmoryRun(&run), nil
}

// GetArmoryRuns returns all runs for a template in creation order.
func (repo *Repository) GetArmoryRuns(templateID uuid.UUID) ([]*domain.ArmoryRun, error) {
	var runs []*dbArmoryRun
	query := `SELECT * FROM armory_run WHERE template_id = ? ORDER BY created_at ASC, id ASC`
	if err := repo.dbConn.Select(&runs, query, templateID); err != nil {
		return nil, fmt.Errorf("getting armory runs for template %s: %w", templateID, err)
	}

	result := make([]*domain.ArmoryRun, len(runs))
	for i, run := range runs {
		result[i] = toDomainArmoryRun(run)
	}
	return result, nil
}

// CreateArmoryRun inserts an Armory run and its immutable configuration.
func (repo *Repository) CreateArmoryRun(run *domain.ArmoryRun) error {
	query := `
		INSERT INTO armory_run (
			id, template_id, template_snapshot, use_https, wordlists, status,
			attack_type, max_concurrent, created_at, started_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	createdAt := run.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := repo.dbConn.Exec(
		query,
		run.ID,
		run.TemplateID,
		run.TemplateSnapshot,
		run.UseHTTPS,
		StringArray(run.Wordlists),
		run.Status,
		run.AttackType,
		run.MaxConcurrent,
		createdAt,
		run.StartedAt,
		run.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("creating armory run %s: %w", run.ID, err)
	}
	return nil
}

// UpdateArmoryRun updates the lifecycle state and timestamps of an Armory run.
func (repo *Repository) UpdateArmoryRun(run *domain.ArmoryRun) error {
	query := `UPDATE armory_run SET status = ?, started_at = ?, finished_at = ? WHERE id = ?`
	result, err := repo.dbConn.Exec(query, run.Status, run.StartedAt, run.FinishedAt, run.ID)
	if err != nil {
		return fmt.Errorf("updating armory run %s: %w", run.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("getting updated armory run rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("armory run %s not found", run.ID)
	}
	return nil
}

// DeleteArmoryRun deletes an Armory run by ID.
func (repo *Repository) DeleteArmoryRun(id uuid.UUID) error {
	result, err := repo.dbConn.Exec(`DELETE FROM armory_run WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting armory run %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("getting deleted armory run rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("armory run %s not found", id)
	}
	return nil
}

// GetArmoryEntries returns all request links for an Armory run.
func (repo *Repository) GetArmoryEntries(runID uuid.UUID) ([]*domain.ArmoryEntry, error) {
	entries := make([]dbArmoryEntry, 0)
	query := `SELECT run_id, request_id FROM armory_entry WHERE run_id = ? ORDER BY request_id ASC`
	if err := repo.dbConn.Select(&entries, query, runID); err != nil {
		return nil, fmt.Errorf("getting entries for armory run %s: %w", runID, err)
	}

	result := make([]*domain.ArmoryEntry, len(entries))
	for i, entry := range entries {
		result[i] = &domain.ArmoryEntry{RunID: entry.RunID, RequestID: entry.RequestID}
	}
	return result, nil
}

// CreateArmoryEntry links a generated proxy request to an Armory run.
func (repo *Repository) CreateArmoryEntry(entry *domain.ArmoryEntry) error {
	query := `INSERT INTO armory_entry (run_id, request_id) VALUES (?, ?)`
	_, err := repo.dbConn.Exec(query, entry.RunID, entry.RequestID)
	if err != nil {
		return fmt.Errorf("creating armory entry for run %s: %w", entry.RunID, err)
	}
	return nil
}
