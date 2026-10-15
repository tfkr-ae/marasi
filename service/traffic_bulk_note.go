package service

import (
	"net/http"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// bulkNoteBody is the body of POST /traffic/bulk/note: a selection and
// either a non-empty append text or clear set to true.
type bulkNoteBody struct {
	bulkSelection
	Append *string `json:"append"`
	Clear  bool    `json:"clear"`
}

// apply appends to or clears the notes of sel, or returns
// errInvalidBulkRequest when the body does not name exactly one of them.
func (b *bulkNoteBody) apply(repo domain.TrafficRepository, sel domain.Selection) (int, error) {
	switch {
	case b.Append != nil && *b.Append != "" && !b.Clear:
		return repo.AppendNote(sel, *b.Append)
	case b.Append == nil && b.Clear:
		return repo.ClearNotes(sel)
	default:
		return 0, errInvalidBulkRequest
	}
}

func addTrafficBulkNoteRoute(mux routeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("POST /traffic/bulk/note", func(w http.ResponseWriter, r *http.Request) {
		var body bulkNoteBody
		serveBulk(w, r, proxy, events, "note", &body, body.apply)
	})
}
