package db

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// trafficIndexRow is the searchable text of one request/response pair, one
// field per column of the traffic_fts index. A nil field indexes nothing.
type trafficIndexRow struct {
	rowID        int64
	requestHead  any
	requestBody  any
	responseHead any
	responseBody any
	note         any
	metadata     any
}

// storedPair is the stored text of a pair that the index is built from.
type storedPair struct {
	RowID       int64  `db:"rowid"`
	RequestRaw  []byte `db:"request_raw"`
	ResponseRaw []byte `db:"response_raw"`
}

// newTrafficIndexRow builds the whole index row for a stored pair. Heads are
// always indexed. A body is left out when it is binary.
func newTrafficIndexRow(pair storedPair) trafficIndexRow {
	requestHead, requestBody := splitHTTPMessage(pair.RequestRaw)
	responseHead, responseBody := splitHTTPMessage(pair.ResponseRaw)
	return trafficIndexRow{
		rowID:        pair.RowID,
		requestHead:  indexableHead(requestHead),
		requestBody:  indexableBody(requestBody),
		responseHead: indexableHead(responseHead),
		responseBody: indexableBody(responseBody),
	}
}

// indexPair writes the whole index row for the pair with id, replacing any row
// it already has. Call it in the transaction that changed the pair's text.
func indexPair(tx sqlx.Ext, id uuid.UUID) error {
	var pair storedPair
	err := sqlx.Get(tx, &pair, `SELECT rowid, request_raw, response_raw FROM request WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("reading pair %s to index: %w", id, err)
	}
	if err := writeTrafficIndexRow(tx, newTrafficIndexRow(pair)); err != nil {
		return fmt.Errorf("indexing pair %s: %w", id, err)
	}
	return nil
}

// writeTrafficIndexRow replaces the index row. A plain INSERT over an existing
// rowid would keep the old text matching, so the row is always replaced whole.
func writeTrafficIndexRow(tx sqlx.Execer, row trafficIndexRow) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO traffic_fts
		(rowid, request_head, request_body, response_head, response_body, note, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.rowID, row.requestHead, row.requestBody, row.responseHead, row.responseBody, row.note, row.metadata)
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
