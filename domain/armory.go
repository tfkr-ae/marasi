package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrArmoryTemplateNotFound means no Armory template has the requested ID.
var ErrArmoryTemplateNotFound = errors.New("armory template not found")

// ArmoryAttackType identifies how payloads are applied to a template.
type ArmoryAttackType string

const (
	// ArmoryAttackHarpoon applies one payload set to each template input in turn.
	ArmoryAttackHarpoon ArmoryAttackType = "harpoon"
	// ArmoryAttackBroadside applies the same payload to every template input.
	ArmoryAttackBroadside ArmoryAttackType = "broadside"
	// ArmoryAttackTandem advances multiple payload sets together.
	ArmoryAttackTandem ArmoryAttackType = "tandem"
	// ArmoryAttackMaelstrom generates every combination of its payload sets.
	ArmoryAttackMaelstrom ArmoryAttackType = "maelstrom"
)

// ArmoryRunStatus identifies the lifecycle state of an Armory run.
type ArmoryRunStatus string

const (
	// ArmoryRunDraft indicates that a run has been created but not started.
	ArmoryRunDraft ArmoryRunStatus = "draft"
	// ArmoryRunInProgress indicates that a run is executing.
	ArmoryRunInProgress ArmoryRunStatus = "in_progress"
	// ArmoryRunCompleted indicates that a run finished successfully.
	ArmoryRunCompleted ArmoryRunStatus = "complete"
	// ArmoryRunFailed indicates that a run stopped because of an error.
	ArmoryRunFailed ArmoryRunStatus = "failed"
	// ArmoryRunCancelled indicates that a user stopped a run.
	ArmoryRunCancelled ArmoryRunStatus = "cancelled"
)

// ArmoryTemplate is a reusable raw HTTP request template.
type ArmoryTemplate struct {
	// ID uniquely identifies the template.
	ID uuid.UUID
	// Name is the display name of the template.
	Name string
	// Description explains the purpose of the template.
	Description string
	// RawTemplate contains the Go template used to generate requests.
	RawTemplate string
}

// ArmoryRun is an execution of an Armory template with a fixed configuration.
type ArmoryRun struct {
	// ID uniquely identifies the run.
	ID uuid.UUID
	// TemplateID identifies the source template.
	TemplateID uuid.UUID
	// TemplateSnapshot preserves the raw template used by this run.
	TemplateSnapshot string
	// UseHTTPS controls whether generated requests use HTTPS.
	UseHTTPS bool
	// Wordlists contains the ordered names of the wordlists used by the run.
	Wordlists []string
	// Status is the current lifecycle state of the run.
	Status ArmoryRunStatus
	// AttackType determines how payloads are applied to template inputs.
	AttackType ArmoryAttackType
	// MaxConcurrent limits the number of requests executing at once.
	MaxConcurrent int

	// CreatedAt is when the run was created.
	CreatedAt time.Time
	// StartedAt is when execution began, or nil if the run has not started.
	StartedAt *time.Time
	// FinishedAt is when execution ended, or nil if the run has not finished.
	FinishedAt *time.Time
}

// ArmoryEntry associates a generated proxy request with an Armory run.
type ArmoryEntry struct {
	// RunID identifies the Armory run.
	RunID uuid.UUID
	// RequestID identifies the generated proxy request.
	RequestID uuid.UUID
}

// ArmoryRepository defines persistence operations for templates, runs, and request links.
type ArmoryRepository interface {
	// GetArmoryTemplates returns all Armory templates.
	GetArmoryTemplates() ([]*ArmoryTemplate, error)
	// GetArmoryTemplate returns an Armory template by ID.
	GetArmoryTemplate(id uuid.UUID) (*ArmoryTemplate, error)
	// CreateArmoryTemplate persists an Armory template.
	CreateArmoryTemplate(template *ArmoryTemplate) error
	// UpdateArmoryTemplate updates an existing Armory template.
	UpdateArmoryTemplate(template *ArmoryTemplate) error
	// DeleteArmoryTemplate deletes an Armory template by ID.
	DeleteArmoryTemplate(id uuid.UUID) error

	// GetArmoryRun returns an Armory run by ID.
	GetArmoryRun(id uuid.UUID) (*ArmoryRun, error)
	// GetArmoryRuns returns all runs created from a template.
	GetArmoryRuns(templateID uuid.UUID) ([]*ArmoryRun, error)
	// CreateArmoryRun persists an Armory run.
	CreateArmoryRun(run *ArmoryRun) error
	// UpdateArmoryRun updates the lifecycle state and timestamps of a run.
	UpdateArmoryRun(run *ArmoryRun) error
	// DeleteArmoryRun deletes an Armory run by ID.
	DeleteArmoryRun(id uuid.UUID) error

	// GetArmoryEntries returns all request links for a run.
	GetArmoryEntries(runID uuid.UUID) ([]*ArmoryEntry, error)
	// CreateArmoryEntry links a generated request to a run.
	CreateArmoryEntry(entry *ArmoryEntry) error
}
