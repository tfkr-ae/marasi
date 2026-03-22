package domain

import (
	"time"

	"github.com/google/uuid"
)

// TestCase represents a single security test case with its metadata and associated data.
type TestCase struct {
	// ID is the unique identifier for the test case.
	ID          uuid.UUID
	// Title is the short name of the test case.
	Title       string
	// Description provides detailed information about what the test case covers.
	Description string
	// Category is the classification group for the test case.
	Category    string
	// Tags are labels used for filtering and organizing test cases.
	Tags        []string
	// Requests is a list of associated HTTP request IDs.
	Requests    []uuid.UUID
	// Artifacts is a list of metadata for files associated with this test case.
	Artifacts   []*ArtifactMetadata
	// Note contains additional researcher observations.
	Note        string
	// CreatedAt is the timestamp when the test case was first recorded.
	CreatedAt   time.Time
}

// Finding represents a security vulnerability or discovery identified during testing.
type Finding struct {
	// ID is the unique identifier for the finding.
	ID            uuid.UUID
	// TestCaseID is an optional reference to the test case that triggered this finding.
	TestCaseID    *uuid.UUID
	// Title is the short name of the finding.
	Title         string
	// Requests is a list of associated HTTP request IDs that demonstrate the finding.
	Requests      []uuid.UUID
	// CVSSVector is the CVSS v3.1 vector string.
	CVSSVector    string
	// CVSSScore is the numerical CVSS score.
	CVSSScore     float64
	// Severity is the qualitative rating (e.g., Low, Medium, High, Critical).
	Severity      string
	// WriteUp is the detailed explanation of the finding, impact, and reproduction steps.
	WriteUp       string
	// TreatmentPlan provides recommendations for remediation.
	TreatmentPlan string
	// Artifacts is a list of metadata for files associated with this finding.
	Artifacts     []*ArtifactMetadata
	// CreatedAt is the timestamp when the finding was first recorded.
	CreatedAt     time.Time
}

// ArtifactMetadata contains the properties of an associated file without its raw data.
type ArtifactMetadata struct {
	// ID is the unique identifier for the artifact.
	ID        uuid.UUID
	// Filename is the original name of the file.
	Filename  string
	// MimeType is the media type of the file content.
	MimeType  string
	// Size is the size of the file in bytes.
	Size      int64
	// CreatedAt is the timestamp when the artifact was uploaded.
	CreatedAt time.Time
}

// Artifact represents a file associated with a test case or finding, including its raw data.
type Artifact struct {
	// ArtifactMetadata embeds the file's properties.
	*ArtifactMetadata
	// TestCaseID is an optional reference to an associated test case.
	TestCaseID *uuid.UUID
	// FindingID is an optional reference to an associated finding.
	FindingID  *uuid.UUID
	// Data is the raw byte content of the file.
	Data       []byte
}

// ReportingRepository defines the interface for persisting and retrieving reporting data.
type ReportingRepository interface {
	// GetTestCase retrieves a single test case by its ID.
	GetTestCase(uuid.UUID) (*TestCase, error)
	// SaveTestCase persists a test case, performing an upsert if it already exists.
	SaveTestCase(*TestCase) error
	// ListTestCases returns all recorded test cases ordered by creation date.
	ListTestCases() ([]*TestCase, error)
	// DeleteTestCase removes a test case by its ID.
	DeleteTestCase(uuid.UUID) error
	// LinkRequestToTestCase associates an HTTP request with a test case.
	LinkRequestToTestCase(uuid.UUID, uuid.UUID) error
	// UnlinkRequestFromTestCase removes the association between a request and a test case.
	UnlinkRequestFromTestCase(uuid.UUID, uuid.UUID) error

	// GetFinding retrieves a single finding by its ID.
	GetFinding(uuid.UUID) (*Finding, error)
	// SaveFinding persists a finding, performing an upsert if it already exists.
	SaveFinding(*Finding) error
	// ListFindings returns all recorded findings ordered by creation date.
	ListFindings() ([]*Finding, error)
	// DeleteFinding removes a finding by its ID.
	DeleteFinding(uuid.UUID) error
	// LinkRequestToFinding associates an HTTP request with a finding.
	LinkRequestToFinding(uuid.UUID, uuid.UUID) error
	// UnlinkRequestFromFinding removes the association between a request and a finding.
	UnlinkRequestFromFinding(uuid.UUID, uuid.UUID) error

	// SaveArtifact persists an artifact and its raw data.
	SaveArtifact(*Artifact) error
	// GetArtifact retrieves an artifact, including its raw data, by its ID.
	GetArtifact(uuid.UUID) (*Artifact, error)
	// DeleteArtifact removes an artifact by its ID.
	DeleteArtifact(uuid.UUID) error
}
