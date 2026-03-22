package db

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.ReportingRepository = (*Repository)(nil)

// dbTestCase represents the database schema for a test case.
type dbTestCase struct {
	// ID is the primary key.
	ID          uuid.UUID   `db:"id"`
	// Title is the test case title.
	Title       string      `db:"title"`
	// Description is the test case description.
	Description string      `db:"description"`
	// Category is the test case category.
	Category    string      `db:"category"`
	// Tags is a custom string array type for database storage.
	Tags        StringArray `db:"tags"`
	// Note is the researcher note.
	Note        string      `db:"note"`
	// CreatedAt is the record creation timestamp.
	CreatedAt   time.Time   `db:"created_at"`
}

// toDomainTestCase converts a database test case model to a domain test case model.
func toDomainTestCase(dbTC *dbTestCase, requests []uuid.UUID, artifacts []*domain.ArtifactMetadata) *domain.TestCase {
	tags := []string(dbTC.Tags)
	if tags == nil {
		tags = make([]string, 0)
	}

	if requests == nil {
		requests = make([]uuid.UUID, 0)
	}

	if artifacts == nil {
		artifacts = make([]*domain.ArtifactMetadata, 0)
	}
	return &domain.TestCase{
		ID:          dbTC.ID,
		Title:       dbTC.Title,
		Description: dbTC.Description,
		Category:    dbTC.Category,
		Tags:        tags,
		Requests:    requests,
		Artifacts:   artifacts,
		Note:        dbTC.Note,
		CreatedAt:   dbTC.CreatedAt,
	}
}

// fromDomainTestCase converts a domain test case model to a database test case model.
func fromDomainTestCase(domainTC *domain.TestCase) *dbTestCase {
	return &dbTestCase{
		ID:          domainTC.ID,
		Title:       domainTC.Title,
		Description: domainTC.Description,
		Category:    domainTC.Category,
		Tags:        StringArray(domainTC.Tags),
		Note:        domainTC.Note,
	}
}

// GetTestCase retrieves a test case from the database by ID, including its associated requests and artifacts.
func (repo *Repository) GetTestCase(id uuid.UUID) (*domain.TestCase, error) {
	var dbTC dbTestCase

	queryTC := `
		SELECT id, title, description, category, tags, note, created_at 
		FROM test_cases 
		WHERE id = ?
	`

	err := repo.dbConn.Get(&dbTC, queryTC, id)
	if err != nil {
		return nil, fmt.Errorf("getting test case %s : %w", id, err)
	}

	requestIDs := make([]uuid.UUID, 0)
	queryRequests := `
		SELECT request_id 
		FROM test_case_requests 
		WHERE test_case_id = ?
	`
	err = repo.dbConn.Select(&requestIDs, queryRequests, id)
	if err != nil {
		return nil, fmt.Errorf("getting requests for test case %s : %w", id, err)
	}

	dbArtifacts := make([]dbArtifactMetadata, 0)
	queryArtifacts := `
		SELECT id, filename, mime_type, size_bytes, created_at 
		FROM artifacts 
		WHERE test_case_id = ?
		ORDER BY created_at ASC
	`

	err = repo.dbConn.Select(&dbArtifacts, queryArtifacts, id)
	if err != nil {
		return nil, fmt.Errorf("getting artifacts for test case %s : %w", id, err)
	}

	domainArtifacts := make([]*domain.ArtifactMetadata, len(dbArtifacts))
	for i, dbA := range dbArtifacts {
		domainArtifacts[i] = toDomainArtifactMetadata(&dbA)
	}

	return toDomainTestCase(&dbTC, requestIDs, domainArtifacts), nil
}

// SaveTestCase upserts a test case and updates its request links in a transaction.
func (repo *Repository) SaveTestCase(domainTC *domain.TestCase) error {
	dbTestCase := fromDomainTestCase(domainTC)

	tx, err := repo.dbConn.Beginx()
	if err != nil {
		return fmt.Errorf("beginning transaction : %w", err)
	}
	defer tx.Rollback()

	upsertTestCase := `
		INSERT INTO test_cases (id, title, description, category, tags, note, created_at)
			VALUES (:id, :title, :description, :category, :tags, :note, CURRENT_TIMESTAMP)
			ON CONFLICT(id) DO UPDATE SET
				title = excluded.title,
				description = excluded.description,
				category = excluded.category,
				tags = excluded.tags,
				note = excluded.note
	`

	_, err = tx.NamedExec(upsertTestCase, dbTestCase)
	if err != nil {
		return fmt.Errorf("inserting test case %s : %w", dbTestCase.Title, err)
	}

	_, err = tx.Exec(`DELETE FROM test_case_requests WHERE test_case_id = ?`, dbTestCase.ID)
	if err != nil {
		return fmt.Errorf("deleting test case requests for test case %s : %w", dbTestCase.ID, err)
	}

	if len(domainTC.Requests) > 0 {
		insertTestCaseRequests := `INSERT INTO test_case_requests (test_case_id, request_id) VALUES (?, ?)`

		stmt, err := tx.Preparex(insertTestCaseRequests)
		if err != nil {
			return fmt.Errorf("preparing query test case request query : %w", err)
		}
		defer stmt.Close()

		for _, reqID := range domainTC.Requests {
			_, err := stmt.Exec(dbTestCase.ID, reqID)
			if err != nil {
				return fmt.Errorf("linking test case to request %s : %w", reqID, err)
			}
		}
	}

	return tx.Commit()
}

// ListTestCases retrieves all test cases with their associated requests and artifacts.
func (repo *Repository) ListTestCases() ([]*domain.TestCase, error) {
	type tcRequestRow struct {
		TestCaseID uuid.UUID `db:"test_case_id"`
		RequestID  uuid.UUID `db:"request_id"`
	}

	type listArtifactRow struct {
		TestCaseID uuid.UUID `db:"test_case_id"`
		dbArtifactMetadata
	}

	var dbTCs []dbTestCase

	queryTCs := `
		SELECT id, title, description, category, tags, note, created_at 
		FROM test_cases 
		ORDER BY created_at DESC
	`

	err := repo.dbConn.Select(&dbTCs, queryTCs)
	if err != nil {
		return nil, fmt.Errorf("listing test cases: %w", err)
	}

	if len(dbTCs) == 0 {
		return make([]*domain.TestCase, 0), nil
	}

	var junctionRows []tcRequestRow
	queryRequests := `
		SELECT test_case_id, request_id 
		FROM test_case_requests
	`
	err = repo.dbConn.Select(&junctionRows, queryRequests)
	if err != nil {
		return nil, fmt.Errorf("listing test case requests: %w", err)
	}

	var artifactRows []listArtifactRow
	queryArtifacts := `
		SELECT id, test_case_id, filename, mime_type, size_bytes, created_at 
		FROM artifacts
		WHERE test_case_id IS NOT NULL
		ORDER BY created_at ASC
	`

	err = repo.dbConn.Select(&artifactRows, queryArtifacts)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}

	requestsMap := make(map[uuid.UUID][]uuid.UUID)
	for _, row := range junctionRows {
		requestsMap[row.TestCaseID] = append(requestsMap[row.TestCaseID], row.RequestID)
	}

	artifactsMap := make(map[uuid.UUID][]*domain.ArtifactMetadata)
	for _, row := range artifactRows {
		domainMetaPtr := toDomainArtifactMetadata(&row.dbArtifactMetadata)
		artifactsMap[row.TestCaseID] = append(artifactsMap[row.TestCaseID], domainMetaPtr)
	}

	result := make([]*domain.TestCase, len(dbTCs))
	for i, dbTC := range dbTCs {
		reqs := requestsMap[dbTC.ID]
		arts := artifactsMap[dbTC.ID]

		result[i] = toDomainTestCase(&dbTC, reqs, arts)
	}

	return result, nil
}

// DeleteTestCase removes a test case from the database.
func (repo *Repository) DeleteTestCase(id uuid.UUID) error {
	query := `DELETE FROM test_cases WHERE id = ?`

	_, err := repo.dbConn.Exec(query, id)
	if err != nil {
		return fmt.Errorf("deleting test case %s: %w", id, err)
	}

	return nil
}

// dbFinding represents the database schema for a finding.
type dbFinding struct {
	// ID is the primary key.
	ID            uuid.UUID  `db:"id"`
	// TestCaseID is the foreign key to the associated test case.
	TestCaseID    *uuid.UUID `db:"test_case_id"`
	// Title is the finding title.
	Title         string     `db:"title"`
	// CVSSVector is the CVSS vector string.
	CVSSVector    string     `db:"cvss_vector"`
	// CVSSScore is the numerical CVSS score.
	CVSSScore     float64    `db:"cvss_score"`
	// Severity is the finding severity level.
	Severity      string     `db:"severity"`
	// WriteUp is the finding write-up.
	WriteUp       string     `db:"writeup"`
	// TreatmentPlan is the remediation plan.
	TreatmentPlan string     `db:"treatment_plan"`
	// CreatedAt is the record creation timestamp.
	CreatedAt     time.Time  `db:"created_at"`
}

// toDomainFinding converts a database finding model to a domain finding model.
func toDomainFinding(dbF *dbFinding, requests []uuid.UUID, artifacts []*domain.ArtifactMetadata) *domain.Finding {
	if requests == nil {
		requests = make([]uuid.UUID, 0)
	}

	if artifacts == nil {
		artifacts = make([]*domain.ArtifactMetadata, 0)
	}
	return &domain.Finding{
		ID:            dbF.ID,
		TestCaseID:    dbF.TestCaseID,
		Title:         dbF.Title,
		Requests:      requests,
		CVSSVector:    dbF.CVSSVector,
		CVSSScore:     dbF.CVSSScore,
		Severity:      dbF.Severity,
		WriteUp:       dbF.WriteUp,
		TreatmentPlan: dbF.TreatmentPlan,
		Artifacts:     artifacts,
		CreatedAt:     dbF.CreatedAt,
	}
}

// fromDomainFinding converts a domain finding model to a database finding model.
func fromDomainFinding(domainFinding *domain.Finding) *dbFinding {
	return &dbFinding{
		ID:            domainFinding.ID,
		TestCaseID:    domainFinding.TestCaseID,
		Title:         domainFinding.Title,
		CVSSVector:    domainFinding.CVSSVector,
		CVSSScore:     domainFinding.CVSSScore,
		Severity:      domainFinding.Severity,
		WriteUp:       domainFinding.WriteUp,
		TreatmentPlan: domainFinding.TreatmentPlan,
		CreatedAt:     domainFinding.CreatedAt,
	}
}

// GetFinding retrieves a finding from the database by ID, including its associated requests and artifacts.
func (repo *Repository) GetFinding(id uuid.UUID) (*domain.Finding, error) {
	var dbF dbFinding

	queryF := `
		SELECT id, test_case_id, title, cvss_vector, cvss_score, severity, writeup, treatment_plan, created_at 
		FROM findings
		WHERE id = ?
	`

	err := repo.dbConn.Get(&dbF, queryF, id)
	if err != nil {
		return nil, fmt.Errorf("getting finding %s : %w", id, err)
	}

	requestIDs := make([]uuid.UUID, 0)
	queryRequests := `
		SELECT request_id 
		FROM finding_requests 
		WHERE finding_id = ?
	`
	err = repo.dbConn.Select(&requestIDs, queryRequests, id)
	if err != nil {
		return nil, fmt.Errorf("getting requests for finding %s : %w", id, err)
	}

	dbArtifacts := make([]dbArtifactMetadata, 0)
	queryArtifacts := `
		SELECT id, filename, mime_type, size_bytes, created_at 
		FROM artifacts 
		WHERE finding_id = ?
		ORDER BY created_at ASC
	`

	err = repo.dbConn.Select(&dbArtifacts, queryArtifacts, id)
	if err != nil {
		return nil, fmt.Errorf("getting artifacts for finding %s : %w", id, err)
	}

	domainArtifacts := make([]*domain.ArtifactMetadata, len(dbArtifacts))
	for i, dbA := range dbArtifacts {
		domainArtifacts[i] = toDomainArtifactMetadata(&dbA)
	}

	return toDomainFinding(&dbF, requestIDs, domainArtifacts), nil
}

// SaveFinding upserts a finding and updates its request links in a transaction.
func (repo *Repository) SaveFinding(domainF *domain.Finding) error {
	dbFinding := fromDomainFinding(domainF)

	tx, err := repo.dbConn.Beginx()
	if err != nil {
		return fmt.Errorf("beginning transaction : %w", err)
	}
	defer tx.Rollback()

	upsertFinding := `
    INSERT INTO findings (
        id, test_case_id, title, cvss_vector, cvss_score, 
        severity, writeup, treatment_plan, created_at
    )
    VALUES (
        :id, :test_case_id, :title, :cvss_vector, :cvss_score, 
        :severity, :writeup, :treatment_plan, CURRENT_TIMESTAMP
    )
    ON CONFLICT(id) DO UPDATE SET
        test_case_id = excluded.test_case_id,
        title = excluded.title,
        cvss_vector = excluded.cvss_vector,
        cvss_score = excluded.cvss_score,
        severity = excluded.severity,
        writeup = excluded.writeup,
        treatment_plan = excluded.treatment_plan
`

	_, err = tx.NamedExec(upsertFinding, dbFinding)
	if err != nil {
		return fmt.Errorf("inserting finding %s : %w", dbFinding.Title, err)
	}

	_, err = tx.Exec(`DELETE FROM finding_requests WHERE finding_id = ?`, dbFinding.ID)
	if err != nil {
		return fmt.Errorf("deleting finding requests for finding %s : %w", dbFinding.ID, err)
	}

	if len(domainF.Requests) > 0 {
		insertFindingRequests := `INSERT INTO finding_requests (finding_id, request_id) VALUES (?, ?)`

		stmt, err := tx.Preparex(insertFindingRequests)
		if err != nil {
			return fmt.Errorf("preparing query finding request query : %w", err)
		}
		defer stmt.Close()

		for _, reqID := range domainF.Requests {
			_, err := stmt.Exec(dbFinding.ID, reqID)
			if err != nil {
				return fmt.Errorf("linking finding to request %s : %w", reqID, err)
			}
		}
	}

	return tx.Commit()
}

// ListFindings retrieves all findings with their associated requests and artifacts.
func (repo *Repository) ListFindings() ([]*domain.Finding, error) {
	type fRequestRow struct {
		FindingID uuid.UUID `db:"finding_id"`
		RequestID uuid.UUID `db:"request_id"`
	}

	type listArtifactRow struct {
		FindingID uuid.UUID `db:"finding_id"`
		dbArtifactMetadata
	}

	var dbFs []dbFinding

	queryFs := `
		SELECT id, test_case_id, title, cvss_vector, cvss_score, severity, writeup, treatment_plan, created_at 
		FROM findings
		ORDER BY created_at DESC
	`

	err := repo.dbConn.Select(&dbFs, queryFs)
	if err != nil {
		return nil, fmt.Errorf("listing findings: %w", err)
	}

	if len(dbFs) == 0 {
		return make([]*domain.Finding, 0), nil
	}

	var junctionRows []fRequestRow
	queryRequests := `
		SELECT finding_id, request_id 
		FROM finding_requests
	`
	err = repo.dbConn.Select(&junctionRows, queryRequests)
	if err != nil {
		return nil, fmt.Errorf("listing finding requests: %w", err)
	}

	var artifactRows []listArtifactRow
	queryArtifacts := `
		SELECT id, finding_id, filename, mime_type, size_bytes, created_at 
		FROM artifacts
		WHERE finding_id IS NOT NULL
		ORDER BY created_at ASC
	`

	err = repo.dbConn.Select(&artifactRows, queryArtifacts)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}

	requestsMap := make(map[uuid.UUID][]uuid.UUID)
	for _, row := range junctionRows {
		requestsMap[row.FindingID] = append(requestsMap[row.FindingID], row.RequestID)
	}

	artifactsMap := make(map[uuid.UUID][]*domain.ArtifactMetadata)
	for _, row := range artifactRows {
		domainMetaPtr := toDomainArtifactMetadata(&row.dbArtifactMetadata)
		artifactsMap[row.FindingID] = append(artifactsMap[row.FindingID], domainMetaPtr)
	}

	result := make([]*domain.Finding, len(dbFs))
	for i, dbF := range dbFs {
		reqs := requestsMap[dbF.ID]
		arts := artifactsMap[dbF.ID]

		result[i] = toDomainFinding(&dbF, reqs, arts)
	}

	return result, nil
}

// DeleteFinding removes a finding from the database.
func (repo *Repository) DeleteFinding(id uuid.UUID) error {
	query := `DELETE FROM findings WHERE id = ?`

	_, err := repo.dbConn.Exec(query, id)
	if err != nil {
		return fmt.Errorf("deleting finding %s: %w", id, err)
	}

	return nil
}

// dbArtifact represents the full database schema for an artifact, including binary data.
type dbArtifact struct {
	// ID is the primary key.
	ID         uuid.UUID  `db:"id"`
	// TestCaseID is the foreign key to an associated test case.
	TestCaseID *uuid.UUID `db:"test_case_id"`
	// FindingID is the foreign key to an associated finding.
	FindingID  *uuid.UUID `db:"finding_id"`
	// Filename is the artifact filename.
	Filename   string     `db:"filename"`
	// MimeType is the artifact media type.
	MimeType   string     `db:"mime_type"`
	// Size is the size in bytes.
	Size       int64      `db:"size_bytes"`
	// Data is the raw binary content.
	Data       []byte     `db:"data"`
	// CreatedAt is the record creation timestamp.
	CreatedAt  time.Time  `db:"created_at"`
}

// dbArtifactMetadata represents the database schema for artifact metadata.
type dbArtifactMetadata struct {
	// ID is the primary key.
	ID        uuid.UUID `db:"id"`
	// Filename is the artifact filename.
	Filename  string    `db:"filename"`
	// MimeType is the artifact media type.
	MimeType  string    `db:"mime_type"`
	// Size is the size in bytes.
	Size      int64     `db:"size_bytes"`
	// CreatedAt is the record creation timestamp.
	CreatedAt time.Time `db:"created_at"`
}

// fromDomainArtifact converts a domain artifact model to a database artifact model.
func fromDomainArtifact(a *domain.Artifact) *dbArtifact {
	return &dbArtifact{
		ID:         a.ID,
		TestCaseID: a.TestCaseID,
		FindingID:  a.FindingID,
		Filename:   a.Filename,
		MimeType:   a.MimeType,
		Size:       a.Size,
		Data:       a.Data,
		CreatedAt:  a.CreatedAt,
	}
}

// toDomainArtifact converts a database artifact model to a domain artifact model.
func toDomainArtifact(dbA *dbArtifact) *domain.Artifact {
	return &domain.Artifact{
		ArtifactMetadata: &domain.ArtifactMetadata{
			ID:        dbA.ID,
			Filename:  dbA.Filename,
			MimeType:  dbA.MimeType,
			Size:      dbA.Size,
			CreatedAt: dbA.CreatedAt,
		},
		TestCaseID: dbA.TestCaseID,
		FindingID:  dbA.FindingID,
		Data:       dbA.Data,
	}
}

// toDomainArtifactMetadata converts a database artifact metadata model to a domain model.
func toDomainArtifactMetadata(dbAM *dbArtifactMetadata) *domain.ArtifactMetadata {
	return &domain.ArtifactMetadata{
		ID:        dbAM.ID,
		Filename:  dbAM.Filename,
		MimeType:  dbAM.MimeType,
		Size:      dbAM.Size,
		CreatedAt: dbAM.CreatedAt,
	}
}

// SaveArtifact persists an artifact in the database.
func (repo *Repository) SaveArtifact(a *domain.Artifact) error {
	dbModel := fromDomainArtifact(a)

	query := `
		INSERT INTO artifacts (id, test_case_id, finding_id, filename, mime_type, size_bytes, data, created_at)
		VALUES (:id, :test_case_id, :finding_id, :filename, :mime_type, :size_bytes, :data, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			filename = excluded.filename,
			mime_type = excluded.mime_type,
			size_bytes = excluded.size_bytes,
			data = excluded.data
	`

	_, err := repo.dbConn.NamedExec(query, dbModel)
	if err != nil {
		return fmt.Errorf("saving artifact %s: %w", a.ID, err)
	}

	return nil
}

// GetArtifact retrieves an artifact from the database by ID.
func (repo *Repository) GetArtifact(id uuid.UUID) (*domain.Artifact, error) {
	var dbA dbArtifact

	query := `
		SELECT id, test_case_id, finding_id, filename, mime_type, size_bytes, data, created_at 
		FROM artifacts 
		WHERE id = ?
	`

	err := repo.dbConn.Get(&dbA, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting artifact %s: %w", id, err)
	}

	return toDomainArtifact(&dbA), nil
}

// DeleteArtifact removes an artifact from the database.
func (repo *Repository) DeleteArtifact(id uuid.UUID) error {
	query := `DELETE FROM artifacts WHERE id = ?`

	_, err := repo.dbConn.Exec(query, id)
	if err != nil {
		return fmt.Errorf("deleting artifact %s: %w", id, err)
	}

	return nil
}

// LinkRequestToTestCase creates an association between a test case and a request.
func (repo *Repository) LinkRequestToTestCase(tcID, reqID uuid.UUID) error {
	query := `
		INSERT INTO test_case_requests (test_case_id, request_id) 
		VALUES (?, ?) 
		ON CONFLICT(test_case_id, request_id) DO NOTHING
	`
	_, err := repo.dbConn.Exec(query, tcID, reqID)
	if err != nil {
		return fmt.Errorf("linking request %s to test case %s: %w", reqID, tcID, err)
	}
	return nil
}

// UnlinkRequestFromTestCase removes the association between a test case and a request.
func (repo *Repository) UnlinkRequestFromTestCase(tcID, reqID uuid.UUID) error {
	query := `DELETE FROM test_case_requests WHERE test_case_id = ? AND request_id = ?`
	_, err := repo.dbConn.Exec(query, tcID, reqID)
	if err != nil {
		return fmt.Errorf("unlinking request %s from test case %s: %w", reqID, tcID, err)
	}
	return nil
}

// LinkRequestToFinding creates an association between a finding and a request.
func (repo *Repository) LinkRequestToFinding(fID, reqID uuid.UUID) error {
	query := `
		INSERT INTO finding_requests (finding_id, request_id) 
		VALUES (?, ?) 
		ON CONFLICT(finding_id, request_id) DO NOTHING
	`
	_, err := repo.dbConn.Exec(query, fID, reqID)
	if err != nil {
		return fmt.Errorf("linking request %s to finding %s: %w", reqID, fID, err)
	}
	return nil
}

// UnlinkRequestFromFinding removes the association between a finding and a request.
func (repo *Repository) UnlinkRequestFromFinding(fID, reqID uuid.UUID) error {
	query := `DELETE FROM finding_requests WHERE finding_id = ? AND request_id = ?`
	_, err := repo.dbConn.Exec(query, fID, reqID)
	if err != nil {
		return fmt.Errorf("unlinking request %s from finding %s: %w", reqID, fID, err)
	}
	return nil
}
