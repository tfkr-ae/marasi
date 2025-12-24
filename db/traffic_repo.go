package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.TrafficRepository = (*Repository)(nil)

// dbRequestResponse represents a combined request and response entry as stored in the database.
// It differs from the domain.ProxyRequest and domain.ProxyResponse by using sql.Null* types
// for fields that might be absent (e.g., response details if a request hasn't received a response yet)
// and combines both request and response data into a single struct for database operations.
type dbRequestResponse struct {
	// Request
	ID          uuid.UUID `db:"id"`
	Scheme      string    `db:"scheme"`
	Method      string    `db:"method"`
	Host        string    `db:"host"`
	Path        string    `db:"path"`
	RequestRaw  []byte    `db:"request_raw"`
	RequestedAt time.Time `db:"requested_at"`

	// Response
	// TODO: DB will set default values for these columns so they will not be "null". Need to revist and either remove that DB restriction / keep these as normal fields
	Status      sql.NullString `db:"status"`
	StatusCode  sql.NullInt64  `db:"status_code"`
	ResponseRaw []byte         `db:"response_raw"`
	ContentType sql.NullString `db:"content_type"`
	Length      sql.NullString `db:"length"`
	RespondedAt sql.NullTime   `db:"responded_at"`

	// Common
	Metadata Metadata       `db:"metadata"`
	Note     sql.NullString `db:"note"`
}

// dbRequestResponseSummary represents a summarized version of a request and response entry
// for database queries where raw request/response bodies are not needed.
// It uses sql.Null* types for potentially absent response fields, similar to dbRequestResponse.
type dbRequestResponseSummary struct {
	// Request
	ID          uuid.UUID `db:"id"`
	Scheme      string    `db:"scheme"`
	Method      string    `db:"method"`
	Host        string    `db:"host"`
	Path        string    `db:"path"`
	RequestedAt time.Time `db:"requested_at"`

	// Response
	Status      sql.NullString `db:"status"`
	StatusCode  sql.NullInt64  `db:"status_code"`
	ContentType sql.NullString `db:"content_type"`
	Length      sql.NullString `db:"length"`
	RespondedAt sql.NullTime   `db:"responded_at"`

	// Common
	Metadata Metadata `db:"metadata"`
}

// fromDomainProxyRequest converts a domain.ProxyRequest into a dbRequestResponse for database insertion.
func fromDomainProxyRequest(preq *domain.ProxyRequest) *dbRequestResponse {
	return &dbRequestResponse{
		ID:          preq.ID,
		Scheme:      preq.Scheme,
		Method:      preq.Method,
		Host:        preq.Host,
		Path:        preq.Path,
		RequestRaw:  preq.Raw,
		RequestedAt: preq.RequestedAt,
		Metadata:    Metadata(preq.Metadata),
	}
}

// toDomainProxyRequest converts a dbRequestResponse into a domain.ProxyRequest.
func toDomainProxyRequest(dbReqRes *dbRequestResponse) *domain.ProxyRequest {
	return &domain.ProxyRequest{
		ID:          dbReqRes.ID,
		Scheme:      dbReqRes.Scheme,
		Method:      dbReqRes.Method,
		Host:        dbReqRes.Host,
		Path:        dbReqRes.Path,
		Raw:         dbReqRes.RequestRaw,
		RequestedAt: dbReqRes.RequestedAt,
		Metadata:    map[string]any(dbReqRes.Metadata),
	}
}

// fromDomainProxyResponse converts a domain.ProxyResponse into a dbRequestResponse for database update.
// It correctly handles nullable fields by converting them to sql.Null* types.
func fromDomainProxyResponse(presp *domain.ProxyResponse) *dbRequestResponse {
	return &dbRequestResponse{
		ID: presp.ID,
		Status: sql.NullString{
			String: presp.Status,
			Valid:  presp.Status != "",
		},
		StatusCode: sql.NullInt64{
			Int64: int64(presp.StatusCode),
			Valid: presp.StatusCode > 0,
		},
		ResponseRaw: presp.Raw,
		ContentType: sql.NullString{
			String: presp.ContentType,
			Valid:  presp.ContentType != "",
		},
		Length: sql.NullString{
			String: presp.Length,
			Valid:  presp.Length != "",
		},
		RespondedAt: sql.NullTime{
			Time:  presp.RespondedAt,
			Valid: !presp.RespondedAt.IsZero(),
		},
		Metadata: Metadata(presp.Metadata),
	}
}

// toDomainProxyResponse converts a dbRequestResponse into a domain.ProxyResponse.
// It safely extracts values from sql.Null* types.
func toDomainProxyResponse(dbReqRes *dbRequestResponse) *domain.ProxyResponse {
	resp := &domain.ProxyResponse{
		ID:       dbReqRes.ID,
		Raw:      dbReqRes.ResponseRaw,
		Metadata: map[string]any(dbReqRes.Metadata),
	}

	if dbReqRes.Status.Valid {
		resp.Status = dbReqRes.Status.String
	}

	if dbReqRes.StatusCode.Valid {
		resp.StatusCode = int(dbReqRes.StatusCode.Int64)
	}

	if dbReqRes.ContentType.Valid {
		resp.ContentType = dbReqRes.ContentType.String
	}

	if dbReqRes.Length.Valid {
		resp.Length = dbReqRes.Length.String
	}

	if dbReqRes.RespondedAt.Valid {
		resp.RespondedAt = dbReqRes.RespondedAt.Time
	}
	return resp
}

// toDomainRequestResponseRow converts a dbRequestResponse into a domain.RequestResponseRow,
// combining both request and response details, and handling the note field.
func toDomainRequestResponseRow(dbReqRes *dbRequestResponse) *domain.RequestResponseRow {
	req := toDomainProxyRequest(dbReqRes)
	resp := toDomainProxyResponse(dbReqRes)

	row := &domain.RequestResponseRow{
		Request:  *req,
		Response: *resp,
		Metadata: map[string]any(dbReqRes.Metadata),
	}

	if dbReqRes.Note.Valid {
		row.Note = dbReqRes.Note.String
	}

	return row
}

// toDomainRequestResponseSummary converts a dbRequestResponseSummary into a domain.RequestResponseSummary,
// extracting relevant fields for a high-level overview.
func toDomainRequestResponseSummary(dbSummary *dbRequestResponseSummary) *domain.RequestResponseSummary {
	reqResSummary := &domain.RequestResponseSummary{
		ID:          dbSummary.ID,
		Scheme:      dbSummary.Scheme,
		Method:      dbSummary.Method,
		Host:        dbSummary.Host,
		Path:        dbSummary.Path,
		RequestedAt: dbSummary.RequestedAt,
		Metadata:    map[string]any(dbSummary.Metadata),
	}

	if dbSummary.Status.Valid {
		reqResSummary.Status = dbSummary.Status.String
	}

	if dbSummary.StatusCode.Valid {
		reqResSummary.StatusCode = int(dbSummary.StatusCode.Int64)
	}

	if dbSummary.ContentType.Valid {
		reqResSummary.ContentType = dbSummary.ContentType.String
	}

	if dbSummary.Length.Valid {
		reqResSummary.Length = dbSummary.Length.String
	}

	if dbSummary.RespondedAt.Valid {
		reqResSummary.RespondedAt = dbSummary.RespondedAt.Time
	}

	return reqResSummary
}

// InsertRequest inserts a new domain.ProxyRequest into the database.
func (repo *Repository) InsertRequest(req *domain.ProxyRequest) error {
	dbRequest := fromDomainProxyRequest(req)
	query := `INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at, metadata)
			  VALUES(:id, :scheme, :method, :host, :path, :request_raw, :requested_at, :metadata)`
	_, err := repo.dbConn.NamedExec(query, dbRequest)
	if err != nil {
		return fmt.Errorf("inserting request %d : %w", req.ID, err)
	}
	return nil
}

// InsertResponse updates an existing request entry with response details.
// It expects a domain.ProxyResponse and uses its ID to locate and update the corresponding row.
func (repo *Repository) InsertResponse(resp *domain.ProxyResponse) error {
	dbResponse := fromDomainProxyResponse(resp)
	query := `UPDATE request SET
				status = :status,
				status_code = :status_code,
				response_raw = :response_raw,
				content_type = :content_type,
				length = :length,
				responded_at = :responded_at,
				metadata = :metadata
			  WHERE id = :id`
	result, err := repo.dbConn.NamedExec(query, dbResponse)
	if err != nil {
		return fmt.Errorf("inserting request %d : %w", resp.ID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for response %s : %w", resp.ID, err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no request found with id %s to update", resp.ID)
	}
	return nil
}

// GetResponse retrieves the response details for a given request ID.
// It returns a domain.ProxyResponse or an error if the ID is not found.
func (repo *Repository) GetResponse(id uuid.UUID) (*domain.ProxyResponse, error) {
	var dbRow dbRequestResponse
	query := `SELECT id, status, status_code, response_raw, content_type, length, responded_at, metadata
		      FROM request
			  WHERE id = ?`

	err := repo.dbConn.Get(&dbRow, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting response with id %s : %w", id, err)
	}

	return toDomainProxyResponse(&dbRow), nil
}

// GetRequestResponseRow retrieves a complete request-response pair, including any associated note,
// for a given request ID. It returns a domain.RequestResponseRow.
func (repo *Repository) GetRequestResponseRow(id uuid.UUID) (*domain.RequestResponseRow, error) {
	var dbRow dbRequestResponse
	query := `SELECT
			  r.id, r.scheme, r.method, r.host, r.path, r.request_raw, r.requested_at,
			  r.status, r.status_code, r.response_raw, r.content_type, r.length, r.responded_at,
			  r.metadata, n.note
			  FROM request r
			  LEFT JOIN notes n ON r.id = n.request_id
			  WHERE r.id = ?`

	err := repo.dbConn.Get(&dbRow, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting request & response with id %s : %w", id, err)
	}

	return toDomainRequestResponseRow(&dbRow), nil
}

// GetRequestResponseSummary retrieves a list of summarized request-response entries.
// It excludes raw request/response bodies and prettified metadata for efficiency.
func (repo *Repository) GetRequestResponseSummary() ([]*domain.RequestResponseSummary, error) {
	var dbSummary []*dbRequestResponseSummary
	query := `SELECT
			  id, scheme, method, host, path, requested_at,
			  status, status_code, content_type, length, responded_at,
			  json_remove(metadata, '$.prettified-request', '$.prettified-response') AS metadata
			  FROM request
			  ORDER BY id ASC`

	err := repo.dbConn.Select(&dbSummary, query)
	if err != nil {
		return nil, fmt.Errorf("getting request & response summary : %w", err)
	}

	reqResSummary := make([]*domain.RequestResponseSummary, len(dbSummary))
	for i, row := range dbSummary {
		reqResSummary[i] = toDomainRequestResponseSummary(row)
	}
	return reqResSummary, nil
}

// GetMetadata retrieves the metadata map for a specific request ID.
func (repo *Repository) GetMetadata(id uuid.UUID) (map[string]any, error) {
	var dbMeta Metadata
	query := `SELECT metadata FROM request WHERE id = ?`

	err := repo.dbConn.Get(&dbMeta, query, id)
	if err != nil {
		return dbMeta, fmt.Errorf("selecting metadata for request %v : %w", id, err)
	}

	return map[string]any(dbMeta), nil
}

// UpdateMetadata updates the metadata for one or more requests identified by their IDs.
func (repo *Repository) UpdateMetadata(metadata map[string]any, ids ...uuid.UUID) error {
	dbMeta := Metadata(metadata)
	query := `UPDATE request SET metadata = ? WHERE id = ?`

	for _, id := range ids {
		_, err := repo.dbConn.Exec(query, dbMeta, id)
		if err != nil {
			return fmt.Errorf("updating metadata %v for %v : %w", dbMeta, id, err)
		}
	}
	return nil
}

// GetNote retrieves the user-created note associated with a specific request ID.
func (repo *Repository) GetNote(requestID uuid.UUID) (string, error) {
	var note string
	query := `SELECT note FROM notes WHERE request_id = ?`

	err := repo.dbConn.Get(&note, query, requestID)

	if err != nil {
		return "", fmt.Errorf("getting note for request %s: %w", requestID, err)
	}

	return note, nil
}

// UpdateNote creates or updates a user-created note for a specific request ID.
// If a note already exists for the request, it will be updated; otherwise, a new note will be inserted.
func (repo *Repository) UpdateNote(requestID uuid.UUID, note string) error {
	query := `INSERT INTO notes (request_id, note, created_at)
              VALUES (?, ?, CURRENT_TIMESTAMP)
              ON CONFLICT(request_id) 
			  DO UPDATE SET
				note = excluded.note,
				created_at = CURRENT_TIMESTAMP;`

	_, err := repo.dbConn.Exec(query, requestID, note)

	if err != nil {
		return fmt.Errorf("updating note for request %s: %w", requestID, err)
	}

	return nil
}
