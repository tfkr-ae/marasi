package db

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.TrafficRepository = (*Repository)(nil)

// dbRequestResponse represents a combined request and response entry as stored in the database.
// It differs from the domain.ProxyRequest and domain.ProxyResponse by using sql.Null* types
// for fields that might be absent (e.g., response details if a request hasn't received a response yet)
// and combines both request and response data into a single struct for database operations.
type dbRequestResponse struct {
	// Request
	ID          uuid.UUID     `db:"id"`
	Number      sql.NullInt64 `db:"number"`
	Scheme      string        `db:"scheme"`
	Method      string        `db:"method"`
	Host        string        `db:"host"`
	Path        string        `db:"path"`
	RequestRaw  []byte        `db:"request_raw"`
	RequestedAt time.Time     `db:"requested_at"`

	// Response
	// TODO: DB will set default values for these columns so they will not be "null". Need to revist and either remove that DB restriction / keep these as normal fields
	Status      sql.NullString `db:"status"`
	StatusCode  sql.NullInt64  `db:"status_code"`
	ResponseRaw []byte         `db:"response_raw"`
	ContentType sql.NullString `db:"content_type"`
	Length      sql.NullString `db:"length"`
	RespondedAt sql.NullTime   `db:"responded_at"`
	// Route snapshot, NULL when the pair has none
	// (db/migrations/00026_pair_route.sql).
	UpstreamProxyName    sql.NullString `db:"upstream_proxy_name"`
	UpstreamProxyAddress sql.NullString `db:"upstream_proxy_address"`

	// Common
	Metadata  pairMetadata   `db:"metadata"`
	Note      sql.NullString `db:"note"`
	Highlight sql.NullInt64  `db:"highlight"`
	dbPrettified
}

// dbRequestResponseSummary represents a summarized version of a request and response entry
// for database queries where raw request/response bodies are not needed.
// It uses sql.Null* types for potentially absent response fields, similar to dbRequestResponse.
type dbRequestResponseSummary struct {
	// Request
	ID          uuid.UUID     `db:"id"`
	Number      sql.NullInt64 `db:"number"`
	Scheme      string        `db:"scheme"`
	Method      string        `db:"method"`
	Host        string        `db:"host"`
	Path        string        `db:"path"`
	RequestedAt time.Time     `db:"requested_at"`

	// Response
	Status      sql.NullString `db:"status"`
	StatusCode  sql.NullInt64  `db:"status_code"`
	ContentType sql.NullString `db:"content_type"`
	Length      sql.NullString `db:"length"`
	RespondedAt sql.NullTime   `db:"responded_at"`
	// Route snapshot, NULL when the pair has none
	// (db/migrations/00026_pair_route.sql).
	UpstreamProxyName    sql.NullString `db:"upstream_proxy_name"`
	UpstreamProxyAddress sql.NullString `db:"upstream_proxy_address"`

	// Common
	Metadata  pairMetadata  `db:"metadata"`
	Highlight sql.NullInt64 `db:"highlight"`
}

type dbNoteSummary struct {
	dbRequestResponseSummary
	Note string `db:"note"`
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
		Metadata:    pairMetadata(preq.Metadata),
		dbPrettified: dbPrettified{Request: sql.NullString{
			String: prettifiedCopy(preq.Prettified, preq.Metadata, prettifiedRequestKey),
		}},
	}
}

// toDomainProxyRequest converts a dbRequestResponse into a domain.ProxyRequest.
func toDomainProxyRequest(dbReqRes *dbRequestResponse) *domain.ProxyRequest {
	return &domain.ProxyRequest{
		ID:          dbReqRes.ID,
		Number:      dbReqRes.Number.Int64,
		Scheme:      dbReqRes.Scheme,
		Method:      dbReqRes.Method,
		Host:        dbReqRes.Host,
		Path:        dbReqRes.Path,
		Raw:         dbReqRes.RequestRaw,
		RequestedAt: dbReqRes.RequestedAt,
		Metadata:    map[string]any(dbReqRes.Metadata),
		Prettified:  dbReqRes.dbPrettified.Request.String,
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
		// A response with no Content-Length stores 0, so it sorts as 0
		// (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md). Only a pair with no response has a NULL length.
		Length: sql.NullString{
			String: cmp.Or(presp.Length, "0"),
			Valid:  true,
		},
		RespondedAt: sql.NullTime{
			Time:  presp.RespondedAt,
			Valid: !presp.RespondedAt.IsZero(),
		},
		Metadata:             pairMetadata(presp.Metadata),
		UpstreamProxyName:    routeName(presp.Route),
		UpstreamProxyAddress: routeAddress(presp.Route),
		dbPrettified: dbPrettified{Response: sql.NullString{
			String: prettifiedCopy(presp.Prettified, presp.Metadata, prettifiedResponseKey),
		}},
	}
}

// toDomainProxyResponse converts a dbRequestResponse into a domain.ProxyResponse.
// It safely extracts values from sql.Null* types.
func toDomainProxyResponse(dbReqRes *dbRequestResponse) *domain.ProxyResponse {
	resp := &domain.ProxyResponse{
		ID:         dbReqRes.ID,
		Raw:        dbReqRes.ResponseRaw,
		Metadata:   map[string]any(dbReqRes.Metadata),
		Prettified: dbReqRes.dbPrettified.Response.String,
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

	// A pair with no response stores a NULL length (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md) and reads
	// as "0", as it did when the column defaulted to '0'.
	resp.Length = cmp.Or(dbReqRes.Length.String, "0")

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
		Prettified: domain.Prettified{
			Request:  req.Prettified,
			Response: resp.Prettified,
		},
	}

	if dbReqRes.Note.Valid {
		row.Note = dbReqRes.Note.String
	}
	row.Highlight = highlightOf(dbReqRes.Highlight)
	row.Route = routeOf(dbReqRes.UpstreamProxyName, dbReqRes.UpstreamProxyAddress)

	return row
}

// routeName and routeAddress store a route snapshot, NULL when route is nil.
func routeName(route *domain.RouteSnapshot) sql.NullString {
	if route == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: route.Name, Valid: true}
}

func routeAddress(route *domain.RouteSnapshot) sql.NullString {
	if route == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: route.Address, Valid: true}
}

// routeOf returns a read route snapshot, or nil when the pair has none.
func routeOf(name, address sql.NullString) *domain.RouteSnapshot {
	if !name.Valid {
		return nil
	}
	return &domain.RouteSnapshot{Name: name.String, Address: address.String}
}

// toDomainRequestResponseSummary converts a dbRequestResponseSummary into a domain.RequestResponseSummary,
// extracting relevant fields for a high-level overview.
func toDomainRequestResponseSummary(dbSummary *dbRequestResponseSummary) *domain.RequestResponseSummary {
	reqResSummary := &domain.RequestResponseSummary{
		ID:          dbSummary.ID,
		Number:      dbSummary.Number.Int64,
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

	// A pair with no response stores a NULL length (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md) and reads
	// as "0", as it did when the column defaulted to '0'.
	reqResSummary.Length = cmp.Or(dbSummary.Length.String, "0")

	if dbSummary.RespondedAt.Valid {
		reqResSummary.RespondedAt = dbSummary.RespondedAt.Time
	}
	reqResSummary.Highlight = highlightOf(dbSummary.Highlight)
	reqResSummary.Route = routeOf(dbSummary.UpstreamProxyName, dbSummary.UpstreamProxyAddress)

	return reqResSummary
}

// highlightOf returns a read highlight, or nil when the pair has none.
func highlightOf(h sql.NullInt64) *int {
	if !h.Valid {
		return nil
	}
	value := int(h.Int64)
	return &value
}

// InsertRequest inserts a new domain.ProxyRequest into the database.
// A request with no Number takes the next pair number, and req.Number is
// set to it.
func (repo *Repository) InsertRequest(req *domain.ProxyRequest) error {
	if req.Number == 0 {
		req.Number = repo.NextPairNumber()
	}
	dbRequest := fromDomainProxyRequest(req)
	// length is NULL until the response arrives (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md); the column's
	// '0' default predates the length sort.
	query := `INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at, metadata, length)
			  VALUES(:id, :scheme, :method, :host, :path, :request_raw, :requested_at, :metadata, NULL)`
	err := repo.inTx(func(tx *sqlx.Tx) error {
		if _, err := tx.NamedExec(query, dbRequest); err != nil {
			return err
		}
		if err := insertPairNumber(tx, req.ID, req.Number); err != nil {
			return err
		}
		if _, ok := req.Metadata[interceptedKey]; ok {
			if err := syncIntercepted(tx, req.ID); err != nil {
				return err
			}
		}
		if err := advanceHighWater(tx, req.ID); err != nil {
			return err
		}
		if err := savePrettified(tx, req.ID, "request", dbRequest.dbPrettified.Request.String); err != nil {
			return err
		}
		if err := linkPairToSitemap(tx, req.ID, req.Scheme, req.Host, req.Path, sitemapBucket{}); err != nil {
			return err
		}
		return queueIndex(tx, req.ID)
	})
	if err != nil {
		return fmt.Errorf("inserting request %s : %w", req.ID, err)
	}
	repo.indexQueued()
	repo.pairCaptured()
	return nil
}

// InsertResponse updates an existing request entry with response details.
// It expects a domain.ProxyResponse and uses its ID to locate and update the corresponding row.
// A pair that no longer exists, deleted while in flight, gets no row and
// returns an error wrapping domain.ErrPairNotFound.
// The response metadata is merged into the stored metadata with json_patch
// (RFC 7396), so a key set while the pair was in flight survives; a null
// value in the response metadata deletes its key.
func (repo *Repository) InsertResponse(resp *domain.ProxyResponse) error {
	dbResponse := fromDomainProxyResponse(resp)
	query := `UPDATE request SET
				status = :status,
				status_code = :status_code,
				response_raw = :response_raw,
				content_type = :content_type,
				length = :length,
				responded_at = :responded_at,
				upstream_proxy_name = :upstream_proxy_name,
				upstream_proxy_address = :upstream_proxy_address,
				metadata = json_patch(coalesce(metadata, '{}'), :metadata)
			  WHERE id = :id`
	err := repo.inTx(func(tx *sqlx.Tx) error {
		// Copies an older binary left in the stored metadata move out
		// first, so the merge below cannot keep them there.
		if _, err := movePrettified(tx, resp.ID); err != nil {
			return err
		}
		node, before, err := sitemapState(tx, resp.ID)
		if err != nil {
			return err
		}
		result, err := tx.NamedExec(query, dbResponse)
		if err != nil {
			return err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("checking rows affected: %w", err)
		}
		if rowsAffected == 0 {
			return domain.ErrPairNotFound
		}
		if err := savePrettified(tx, resp.ID, "response", dbResponse.dbPrettified.Response.String); err != nil {
			return err
		}
		// Sorted walks started before this write keep the pair with the
		// pairs that have no value (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md).
		if _, err := tx.Exec(`INSERT INTO pair_response_order (request_id) VALUES (?)`, resp.ID); err != nil {
			return fmt.Errorf("recording the response order: %w", err)
		}
		if _, ok := resp.Metadata[interceptedKey]; ok {
			if err := syncIntercepted(tx, resp.ID); err != nil {
				return err
			}
		}
		if node.Valid {
			after := bucketOf(dbResponse.ContentType, dbResponse.StatusCode)
			if err := rebucketInSitemap(tx, node.Int64, before, after); err != nil {
				return err
			}
		}
		return queueIndex(tx, resp.ID)
	})
	if err != nil {
		return fmt.Errorf("inserting response %s : %w", resp.ID, err)
	}
	repo.indexQueued()
	return nil
}

// GetResponse retrieves the response details for a given request ID.
// It returns a domain.ProxyResponse or an error if the ID is not found.
func (repo *Repository) GetResponse(id uuid.UUID) (*domain.ProxyResponse, error) {
	var dbRow dbRequestResponse
	query := `SELECT r.id, r.status, r.status_code, r.response_raw, r.content_type, r.length, r.responded_at, r.metadata,
			  ` + prettifiedColumns + `
			  FROM request r
			  LEFT JOIN pair_prettified p ON p.request_id = r.id
			  WHERE r.id = ?`

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
			  r.upstream_proxy_name, r.upstream_proxy_address,
			  r.metadata, n.note, h.highlight, pn.number, ` + prettifiedColumns + `
			  FROM request r
			  LEFT JOIN notes n ON r.id = n.request_id
			  LEFT JOIN pair_highlight h ON h.request_id = r.id
			  LEFT JOIN pair_number pn ON pn.request_id = r.id
			  LEFT JOIN pair_prettified p ON p.request_id = r.id
			  WHERE r.id = ?`

	err := repo.dbConn.Get(&dbRow, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting request & response with id %s : %w", id, err)
	}

	return toDomainRequestResponseRow(&dbRow), nil
}

// GetRequestResponseRows retrieves multiple complete request-response pairs,
// including any associated notes, for a slice of request IDs in a single query.
func (repo *Repository) GetRequestResponseRows(ids []uuid.UUID) ([]*domain.RequestResponseRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := `SELECT
			  r.id, r.scheme, r.method, r.host, r.path, r.request_raw, r.requested_at,
			  r.status, r.status_code, r.response_raw, r.content_type, r.length, r.responded_at,
			  r.upstream_proxy_name, r.upstream_proxy_address,
			  r.metadata, n.note, h.highlight, pn.number, ` + prettifiedColumns + `
			  FROM request r
			  LEFT JOIN notes n ON r.id = n.request_id
			  LEFT JOIN pair_highlight h ON h.request_id = r.id
			  LEFT JOIN pair_number pn ON pn.request_id = r.id
			  LEFT JOIN pair_prettified p ON p.request_id = r.id
			  WHERE r.id IN (?)`

	query, args, err := sqlx.In(query, ids)
	if err != nil {
		return nil, fmt.Errorf("binding slice args for request rows: %w", err)
	}

	query = repo.dbConn.Rebind(query)

	var dbRows []*dbRequestResponse
	err = repo.dbConn.Select(&dbRows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting bulk request & responses: %w", err)
	}

	rowMap := make(map[uuid.UUID]*domain.RequestResponseRow, len(dbRows))
	for _, row := range dbRows {
		rowMap[row.ID] = toDomainRequestResponseRow(row)
	}

	reqRows := make([]*domain.RequestResponseRow, 0, len(ids))
	for _, id := range ids {
		if domainRow, exists := rowMap[id]; exists {
			reqRows = append(reqRows, domainRow)
		}
	}

	return reqRows, nil
}

// GetRequestResponseSummary retrieves a list of summarized request-response entries.
// It excludes raw request/response bodies and prettified copies for efficiency.
func (repo *Repository) GetRequestResponseSummary() ([]*domain.RequestResponseSummary, error) {
	var dbSummary []*dbRequestResponseSummary
	query := `SELECT
			  id, scheme, method, host, path, requested_at,
			  status, status_code, content_type, length, responded_at,
			  upstream_proxy_name, upstream_proxy_address,
			  metadata,
			  pair_highlight.highlight, pair_number.number
			  FROM request` + highlightJoin + numberJoin + `
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
	var dbMeta pairMetadata
	query := `SELECT metadata FROM request WHERE id = ?`

	err := repo.dbConn.Get(&dbMeta, query, id)
	if err != nil {
		return dbMeta, fmt.Errorf("selecting metadata for request %v : %w", id, err)
	}

	return map[string]any(dbMeta), nil
}

// UpdateMetadata updates the metadata for one or more requests identified by their IDs.
// All requests and their index rows are updated in one transaction.
func (repo *Repository) UpdateMetadata(metadata map[string]any, ids ...uuid.UUID) error {
	dbMeta := pairMetadata(metadata)
	query := `UPDATE request SET metadata = ? WHERE id = ?`

	return repo.inTx(func(tx *sqlx.Tx) error {
		// The new metadata replaces the old, so copies an older binary left
		// there move out first.
		if _, err := movePrettified(tx, ids...); err != nil {
			return err
		}
		for _, id := range ids {
			result, err := tx.Exec(query, dbMeta, id)
			if err != nil {
				return fmt.Errorf("updating metadata %v for %v : %w", dbMeta, id, err)
			}
			rowsAffected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("updating metadata %v for %v : %w", dbMeta, id, err)
			}
			if rowsAffected == 0 {
				// No such request: nothing changed, so there is nothing to index.
				continue
			}
			if err := indexAnnotations(tx, id); err != nil {
				return fmt.Errorf("updating metadata %v for %v : %w", dbMeta, id, err)
			}
			if err := syncIntercepted(tx, id); err != nil {
				return err
			}
		}
		return nil
	})
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
// The pair's annotation index row is rebuilt in the same transaction.
func (repo *Repository) UpdateNote(requestID uuid.UUID, note string) error {
	query := `INSERT INTO notes (request_id, note, created_at)
              VALUES (?, ?, CURRENT_TIMESTAMP)
              ON CONFLICT(request_id)
			  DO UPDATE SET
				note = excluded.note,
				created_at = CURRENT_TIMESTAMP;`

	err := repo.inTx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(query, requestID, note); err != nil {
			return err
		}
		return indexAnnotations(tx, requestID)
	})
	if err != nil {
		return fmt.Errorf("updating note for request %s: %w", requestID, err)
	}
	return nil
}

// DeleteNote removes the note row for a request ID and rebuilds the pair's
// annotation index row in the same transaction.
func (repo *Repository) DeleteNote(requestID uuid.UUID) error {
	err := repo.inTx(func(tx *sqlx.Tx) error {
		result, err := tx.Exec(`DELETE FROM notes WHERE request_id = ?`, requestID)
		if err != nil {
			return err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return sql.ErrNoRows
		}
		return indexAnnotations(tx, requestID)
	})
	if err != nil {
		return fmt.Errorf("deleting note for request %s: %w", requestID, err)
	}
	return nil
}

// inTx runs fn in a transaction and commits when fn succeeds.
func (repo *Repository) inTx(fn func(tx *sqlx.Tx) error) error {
	return repo.inTxContext(context.Background(), fn)
}

// inTxContext runs fn in a transaction bound to ctx: when ctx ends, the
// statements fn runs with ctx are interrupted and the transaction rolls back.
func (repo *Repository) inTxContext(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	tx, err := repo.dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ListNotes returns a newest-first page of summaries that have a non-empty note.
func (repo *Repository) ListNotes(cursor *uuid.UUID, limit int) ([]*domain.RequestResponseSummary, *uuid.UUID, error) {
	query := `SELECT
			  r.id, r.scheme, r.method, r.host, r.path, r.requested_at,
			  r.status, r.status_code, r.content_type, r.length, r.responded_at,
			  r.upstream_proxy_name, r.upstream_proxy_address,
			  r.metadata,
			  n.note, h.highlight, pn.number
			  FROM request r
			  INNER JOIN notes n ON r.id = n.request_id
			  LEFT JOIN pair_highlight h ON h.request_id = r.id
			  LEFT JOIN pair_number pn ON pn.request_id = r.id
			  WHERE n.note IS NOT NULL AND n.note != ''`
	args := make([]any, 0, 2)
	if cursor != nil {
		query += ` AND r.id < ?`
		args = append(args, *cursor)
	}
	query += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit+1)

	var rows []*dbNoteSummary
	err := repo.dbConn.Select(&rows, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("listing notes: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	items := make([]*domain.RequestResponseSummary, len(rows))
	for i, row := range rows {
		item := toDomainRequestResponseSummary(&row.dbRequestResponseSummary)
		item.Note = row.Note
		items[i] = item
	}

	var nextCursor *uuid.UUID
	if hasMore {
		id := items[len(items)-1].ID
		nextCursor = &id
	}
	return items, nextCursor, nil
}

// SearchByMetadata retrieves requests where the value at the specified JSON path matches the provided value.
func (repo *Repository) SearchByMetadata(path string, value any) ([]*domain.RequestResponseSummary, error) {
	var dbSummary []*dbRequestResponseSummary
	query := `SELECT
			  id, scheme, method, host, path, requested_at,
			  status, status_code, content_type, length, responded_at,
			  upstream_proxy_name, upstream_proxy_address,
			  metadata,
			  pair_highlight.highlight, pair_number.number
			  FROM request` + highlightJoin + numberJoin + `
			  WHERE json_extract(metadata, ?) = ?
			  ORDER BY id ASC`

	err := repo.dbConn.Select(&dbSummary, query, path, value)
	if err != nil {
		return nil, fmt.Errorf("searching metadata with path %s: %w", path, err)
	}

	reqResSummary := make([]*domain.RequestResponseSummary, len(dbSummary))
	for i, row := range dbSummary {
		reqResSummary[i] = toDomainRequestResponseSummary(row)
	}
	return reqResSummary, nil
}

// CountContentTypes returns every distinct response content type with its row
// count. NULL and empty content types are grouped under "". Once the sitemap
// job is complete the counts are the sums of the origins' sitemap buckets, one
// range read; until then they are grouped from the pairs themselves.
func (repo *Repository) CountContentTypes() (domain.ContentTypeCounts, error) {
	return repo.CountContentTypesContext(context.Background())
}

// CountContentTypesContext is CountContentTypes for a read that stops when
// ctx ends.
func (repo *Repository) CountContentTypesContext(ctx context.Context) (domain.ContentTypeCounts, error) {
	var rows []struct {
		ContentType string `db:"content_type"`
		Count       int    `db:"count"`
	}
	result := domain.ContentTypeCounts{Counts: []*domain.ContentTypeCount{}}
	err := repo.inTxContext(ctx, func(tx *sqlx.Tx) error {
		job, err := loadJob(tx, domain.JobSitemap)
		if err != nil {
			return fmt.Errorf("reading the sitemap job: %w", err)
		}
		result.Complete = map[domain.JobName]bool{domain.JobSitemap: job.Complete}
		// Origins have parent 0 in sitemap_count, and their buckets hold
		// every linked pair.
		query := `SELECT content_type, sum(pairs) AS count
			  FROM sitemap_count
			  WHERE parent_id = 0
			  GROUP BY content_type
			  HAVING count > 0
			  ORDER BY count DESC, content_type ASC`
		if !job.Complete {
			query = `SELECT
			  COALESCE(content_type, '') AS content_type,
			  COUNT(*) AS count
			  FROM request
			  GROUP BY COALESCE(content_type, '')
			  ORDER BY count DESC, content_type ASC`
		}
		return tx.SelectContext(ctx, &rows, query)
	})
	if err != nil {
		return domain.ContentTypeCounts{}, fmt.Errorf("counting content types: %w", err)
	}

	for _, row := range rows {
		result.Counts = append(result.Counts, &domain.ContentTypeCount{ContentType: row.ContentType, Count: row.Count})
	}
	return result, nil
}
