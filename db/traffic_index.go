package db

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// trafficIndexRow is the searchable text of a pair's request and response,
// one field per head and body column of the traffic_index table. A nil field
// indexes nothing.
type trafficIndexRow struct {
	rowID        int64
	requestHead  any
	requestBody  any
	responseHead any
	responseBody any
}

// annotationIndexRow is the searchable text of a pair's note and metadata,
// one field per column of the traffic_annotation_index table. A nil field
// indexes nothing.
type annotationIndexRow struct {
	rowID    int64
	note     any
	metadata any
}

// storedPair is the stored text of a pair that the body index is built from.
type storedPair struct {
	// IndexRowID is the pair's traffic_index_key id, which is the rowid of
	// its rows in both indexes.
	IndexRowID  int64  `db:"index_rowid"`
	RequestRaw  []byte `db:"request_raw"`
	ResponseRaw []byte `db:"response_raw"`
}

// storedAnnotations is the stored note and metadata of a pair that the
// annotation index is built from.
type storedAnnotations struct {
	IndexRowID int64          `db:"index_rowid"`
	Note       sql.NullString `db:"note"`
	// Metadata is the metadata JSON without the prettified-request and
	// prettified-response keys, which repeat the raw text.
	Metadata sql.NullString `db:"metadata"`
}

// newTrafficIndexRow builds the body index row for a stored pair. Heads are
// always indexed. Bodies are decoded by their Content-Encoding, and a body is
// left out when it is binary.
func newTrafficIndexRow(pair storedPair) trafficIndexRow {
	requestHead, requestBody := splitHTTPMessage(pair.RequestRaw)
	responseHead, responseBody := splitHTTPMessage(pair.ResponseRaw)
	return trafficIndexRow{
		rowID:        pair.IndexRowID,
		requestHead:  indexableHead(requestHead),
		requestBody:  indexableBody(requestBody),
		responseHead: indexableHead(responseHead),
		responseBody: indexableBody(responseBody),
	}
}

// newAnnotationIndexRow builds the annotation index row for a pair.
func newAnnotationIndexRow(annotations storedAnnotations) annotationIndexRow {
	return annotationIndexRow{
		rowID:    annotations.IndexRowID,
		note:     indexableText(annotations.Note),
		metadata: indexableText(annotations.Metadata),
	}
}

// keyPair gives the pair with id its traffic_index_key id, the rowid of its
// rows in both indexes. The key is assigned once and kept, so a replaced
// index row replaces the pair's own row.
func keyPair(tx sqlx.Execer, id uuid.UUID) error {
	_, err := tx.Exec(`INSERT INTO traffic_index_key (request_id) VALUES (?)
		ON CONFLICT (request_id) DO NOTHING`, id)
	if err != nil {
		return fmt.Errorf("keying pair %s to index: %w", id, err)
	}
	return nil
}

// indexPair writes both index rows for the pair with id, replacing any rows
// it already has. Call it in the transaction that captured the pair's request
// or response.
func indexPair(tx sqlx.Ext, id uuid.UUID) error {
	if err := indexBodies(tx, id); err != nil {
		return err
	}
	return indexAnnotations(tx, id)
}

// indexBodies writes the body index row for the pair with id, replacing any
// row it already has.
func indexBodies(tx sqlx.Ext, id uuid.UUID) error {
	if err := keyPair(tx, id); err != nil {
		return err
	}
	var pair storedPair
	err := sqlx.Get(tx, &pair, `SELECT k.id AS index_rowid, r.request_raw, r.response_raw
		FROM request r
		JOIN traffic_index_key k ON k.request_id = r.id
		WHERE r.id = ?`, id)
	if err != nil {
		return fmt.Errorf("reading pair %s to index: %w", id, err)
	}
	if err := writeTrafficIndexRow(tx, newTrafficIndexRow(pair)); err != nil {
		return fmt.Errorf("indexing pair %s: %w", id, err)
	}
	return nil
}

// indexAnnotations writes the annotation index row for the pair with id,
// replacing any row it already has. Call it in the transaction that changed
// the pair's note or metadata. It never writes the body index.
func indexAnnotations(tx sqlx.Ext, id uuid.UUID) error {
	if err := keyPair(tx, id); err != nil {
		return err
	}
	var annotations storedAnnotations
	err := sqlx.Get(tx, &annotations, `SELECT k.id AS index_rowid, n.note,
		json_remove(r.metadata, '$."prettified-request"', '$."prettified-response"') AS metadata
		FROM request r
		JOIN traffic_index_key k ON k.request_id = r.id
		LEFT JOIN notes n ON n.request_id = r.id
		WHERE r.id = ?`, id)
	if err != nil {
		return fmt.Errorf("reading the annotations of pair %s to index: %w", id, err)
	}
	if err := writeAnnotationIndexRow(tx, newAnnotationIndexRow(annotations)); err != nil {
		return fmt.Errorf("indexing the annotations of pair %s: %w", id, err)
	}
	return nil
}

// writeTrafficIndexRow replaces the body index row. A plain INSERT over an
// existing rowid would keep the old text matching, so the row is always
// replaced whole. Searches no longer read the body index's note and metadata
// columns (ADR-0038), so they are written NULL, which also drops the note and
// metadata text a row from an older binary held.
func writeTrafficIndexRow(tx sqlx.Execer, row trafficIndexRow) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO traffic_index
		(rowid, request_head, request_body, response_head, response_body, note, metadata)
		VALUES (?, ?, ?, ?, ?, NULL, NULL)`,
		row.rowID, row.requestHead, row.requestBody, row.responseHead, row.responseBody)
	return err
}

// writeAnnotationIndexRow replaces the annotation index row, whole for the
// same reason as writeTrafficIndexRow.
func writeAnnotationIndexRow(tx sqlx.Execer, row annotationIndexRow) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO traffic_annotation_index (rowid, note, metadata) VALUES (?, ?, ?)`,
		row.rowID, row.note, row.metadata)
	return err
}

// splitHTTPMessage splits a raw HTTP message into its head (start line and
// headers) and body at the first blank line. A message without a blank line is
// all head.
func splitHTTPMessage(raw []byte) (head, body []byte) {
	for _, separator := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n")} {
		if i := bytes.Index(raw, separator); i >= 0 {
			return raw[:i], raw[i+len(separator):]
		}
	}
	return raw, nil
}

// indexableHead returns a head as valid UTF-8 text without NUL bytes, or nil
// when it is empty.
func indexableHead(head []byte) any {
	if len(head) == 0 {
		return nil
	}
	text := strings.ToValidUTF8(string(head), "�")
	return strings.ReplaceAll(text, "\x00", "�")
}

// indexableBody returns a body as text, or nil when it is empty or binary: it
// contains a NUL byte or is not valid UTF-8.
func indexableBody(body []byte) any {
	if len(body) == 0 || bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		return nil
	}
	return string(body)
}

// indexableText returns text as valid UTF-8 without NUL bytes, or nil when it
// is NULL or empty.
func indexableText(text sql.NullString) any {
	if !text.Valid {
		return nil
	}
	return indexableHead([]byte(text.String))
}
