package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// highlightJoin attaches each pair's highlight to a read of request. A pair
// with no highlight reads a NULL highlight.
const highlightJoin = ` LEFT JOIN pair_highlight ON pair_highlight.request_id = request.id`

// SetHighlight gives every pair in sel the highlight h and returns the number
// of pairs whose highlight changed. It writes only pair_highlight (ADR-0035).
func (repo *Repository) SetHighlight(sel domain.Selection, h int) (int, error) {
	if h < domain.MinHighlight || h > domain.MaxHighlight {
		return 0, fmt.Errorf("%w: %d is not from %d to %d", domain.ErrInvalidHighlight, h, domain.MinHighlight, domain.MaxHighlight)
	}
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		// WHERE true keeps SQLite from reading ON CONFLICT as a join
		// constraint of the SELECT. The upsert's WHERE leaves pairs that
		// already have h untouched, so they are not counted.
		query := `INSERT INTO pair_highlight (request_id, highlight) SELECT id, ? FROM (` + selected + `) WHERE true
			ON CONFLICT (request_id) DO UPDATE SET highlight = excluded.highlight WHERE highlight != excluded.highlight`
		return execCount(tx, fmt.Sprintf("setting highlight %d", h), query, append([]any{h}, args...)...)
	})
}

// ClearHighlight removes the highlight of every pair in sel and returns the
// number of pairs that had one.
func (repo *Repository) ClearHighlight(sel domain.Selection) (int, error) {
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		query := `DELETE FROM pair_highlight WHERE request_id IN (` + selected + `)`
		return execCount(tx, "clearing highlights", query, args...)
	})
}
