package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// errInvalidBulkRequest reports a bulk body that is not valid JSON, has an
// unknown or missing field, or names the wrong number of targets.
var errInvalidBulkRequest = errors.New("invalid bulk request")

// bulkChange is the kind of change a bulk route makes, as named in its
// traffic.changed event.
type bulkChange string

const (
	changeHighlight bulkChange = "highlight"
	changeMetadata  bulkChange = "metadata"
	changeNote      bulkChange = "note"
	changeLink      bulkChange = "link"
	changeUnlink    bulkChange = "unlink"
	changeDelete    bulkChange = "delete"
)

// trafficChanged is the JSON body of a traffic.changed SSE event, which every
// bulk route publishes once. Selection is the selection as received;
// encoding/json compacts its whitespace.
type trafficChanged struct {
	Change    bulkChange      `json:"change"`
	Count     int             `json:"count"` // number of pairs changed
	Selection json.RawMessage `json:"selection"`
}

// bulkResult is the JSON body of a successful bulk route.
type bulkResult struct {
	Count int `json:"count"`
}

// bulkSelection is the selection field every bulk body carries. Embed it in
// an operation's body struct so decodeBulkBody can read it.
type bulkSelection struct {
	Selection json.RawMessage `json:"selection"`
}

func (b *bulkSelection) rawSelection() *bulkSelection { return b }

// bulkBody is a decoded bulk body: an operation struct embedding
// bulkSelection.
type bulkBody interface {
	rawSelection() *bulkSelection
}

// decodeBulkBody decodes r's body strictly into body and parses its
// selection. A malformed body, an unknown field, or a missing selection
// returns errInvalidBulkRequest; an invalid selection wraps
// domain.ErrInvalidSelection.
func decodeBulkBody(r *http.Request, body bulkBody) (domain.Selection, error) {
	if r.Body == nil || !decodeStrictJSON(r.Body, body) {
		return domain.Selection{}, errInvalidBulkRequest
	}
	raw := body.rawSelection().Selection
	if raw == nil {
		return domain.Selection{}, errInvalidBulkRequest
	}
	var sel domain.Selection
	if err := json.Unmarshal(raw, &sel); err != nil {
		return domain.Selection{}, err
	}
	return sel, nil
}

// decodeStrictJSON decodes exactly one JSON value from body into v. It
// reports false for malformed JSON, an unknown struct field, or anything
// after the value.
func decodeStrictJSON(body io.Reader, v any) bool {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

// serveBulk runs one bulk operation: it decodes r into body, calls apply with
// the traffic repository and the selection, writes {"count": n}, and
// publishes one traffic.changed event with change. apply may return
// errInvalidBulkRequest for operation fields it rejects. Failures are written as
// JSON errors and publish nothing: a bad body or selection or query is 400,
// a missing target is 404, and a selection holding a pending launch's pair
// is 409 launch_pending. It reports whether the operation ran.
func serveBulk(w http.ResponseWriter, r *http.Request, proxy *marasi.Proxy, events *eventBroadcaster, change bulkChange, body bulkBody, apply func(repo domain.TrafficRepository, sel domain.Selection) (int, error)) bool {
	sel, err := decodeBulkBody(r, body)
	if err != nil {
		writeBulkError(w, r, err)
		return false
	}
	repo, err := proxy.GetTrafficRepo()
	if err != nil {
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
		return false
	}
	count, err := apply(repo, sel)
	if err != nil {
		writeBulkError(w, r, err)
		return false
	}
	events.publish("traffic.changed", trafficChanged{Change: change, Count: count, Selection: body.rawSelection().Selection})
	writeJSON(w, r, http.StatusOK, bulkResult{Count: count})
	return true
}

// writeBulkError writes the response for a failed bulk operation.
func writeBulkError(w http.ResponseWriter, r *http.Request, err error) {
	var queryErr *domain.QueryError
	var incomplete *domain.SelectionIncompleteError
	var pending *domain.LaunchPendingError
	switch {
	case errors.As(err, &pending):
		writeLaunchPending(w, r, pending)
	case errors.As(err, &incomplete):
		writeJSON(w, r, http.StatusConflict, selectionIncomplete{Error: "selection_incomplete", Message: incomplete.Error(), Jobs: incomplete.Jobs})
	case errors.Is(err, errInvalidBulkRequest):
		writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_request"})
	case errors.Is(err, domain.ErrEmptyNote), errors.Is(err, domain.ErrInvalidHighlight):
		writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_bulk_request", Message: err.Error()})
	case errors.Is(err, domain.ErrInvalidSelection):
		writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_selection", Message: err.Error()})
	case errors.As(err, &queryErr):
		writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: queryErr.Message, Position: queryErr.Position})
	case errors.Is(err, domain.ErrInvalidMetadataKey):
		writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_metadata_key", Message: err.Error()})
	case errors.Is(err, domain.ErrLinkTargetNotFound):
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
	default:
		writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
	}
}

// launchPending is the 409 body of an operation refused because it would
// take a pending launch's pair out of its launchpad, delete that pair, or
// delete the launchpad a launch is pending from (ADR-0053). RequestIDs are
// the pending launches' pair ids.
type launchPending struct {
	Error      string      `json:"error"`
	Message    string      `json:"message"`
	RequestIDs []uuid.UUID `json:"request_ids"`
}

// writeLaunchPending writes the 409 launch_pending response for err.
func writeLaunchPending(w http.ResponseWriter, r *http.Request, err *domain.LaunchPendingError) {
	writeJSON(w, r, http.StatusConflict, launchPending{Error: "launch_pending", Message: err.Error(), RequestIDs: err.IDs})
}

// selectionIncomplete is the 409 body of a bulk operation that refused a
// query selection because a background job its query depends on has not
// finished. Jobs names those jobs; GET /jobs shows their progress.
type selectionIncomplete struct {
	Error   string           `json:"error"`
	Message string           `json:"message"`
	Jobs    []domain.JobName `json:"jobs"`
}

// bulkLinkBody is the body of POST /traffic/bulk/link and /unlink: a
// selection and exactly one target id.
type bulkLinkBody struct {
	bulkSelection
	LaunchpadID *uuid.UUID `json:"launchpad_id"`
	FindingID   *uuid.UUID `json:"finding_id"`
	TestCaseID  *uuid.UUID `json:"test_case_id"`
}

// target returns the one target the body names, or errInvalidBulkRequest.
func (b *bulkLinkBody) target() (domain.LinkTarget, error) {
	var targets []domain.LinkTarget
	for kind, id := range map[domain.LinkKind]*uuid.UUID{
		domain.LinkLaunchpad: b.LaunchpadID,
		domain.LinkFinding:   b.FindingID,
		domain.LinkTestCase:  b.TestCaseID,
	} {
		if id != nil {
			targets = append(targets, domain.LinkTarget{Kind: kind, ID: *id})
		}
	}
	if len(targets) != 1 {
		return domain.LinkTarget{}, errInvalidBulkRequest
	}
	return targets[0], nil
}

// bulkHighlightBody is the body of POST /traffic/bulk/highlight: a selection
// and a highlight from 1 to 8, or null to clear it. The highlight key is
// required, so it is kept raw to tell null from absent.
type bulkHighlightBody struct {
	bulkSelection
	Highlight json.RawMessage `json:"highlight"`
}

// apply sets or clears the body's highlight on sel. The repository checks
// the highlight's range, so an out-of-range one returns
// domain.ErrInvalidHighlight with a message.
func (b *bulkHighlightBody) apply(repo domain.TrafficRepository, sel domain.Selection) (int, error) {
	if b.Highlight == nil {
		return 0, errInvalidBulkRequest
	}
	if string(b.Highlight) == "null" {
		return repo.ClearHighlight(sel)
	}
	var h int
	if err := json.Unmarshal(b.Highlight, &h); err != nil {
		return 0, errInvalidBulkRequest
	}
	return repo.SetHighlight(sel, h)
}

// bulkMetadataBody is the body of POST /traffic/bulk/metadata: a selection,
// a dotted key, and either a JSON value to set or clear: true.
type bulkMetadataBody struct {
	bulkSelection
	Key   *string         `json:"key"`
	Value json.RawMessage `json:"value"` // a JSON null arrives as "null", not nil
	Clear bool            `json:"clear"`
}

// apply sets or clears the body's key on sel. A missing key, both or neither
// of value and clear, or clear: false without a value returns
// errInvalidBulkRequest.
func (b *bulkMetadataBody) apply(repo domain.TrafficRepository, sel domain.Selection) (int, error) {
	if b.Key == nil || b.Clear == (b.Value != nil) {
		return 0, errInvalidBulkRequest
	}
	if b.Clear {
		return repo.ClearMetadataKey(sel, *b.Key)
	}
	return repo.SetMetadataKey(sel, *b.Key, b.Value)
}

func addTrafficBulkRoutes(mux routeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("POST /traffic/bulk/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body bulkMetadataBody
		serveBulk(w, r, proxy, events, changeMetadata, &body, body.apply)
	})
	addTrafficBulkNoteRoute(mux, proxy, events)
	mux.HandleFunc("POST /traffic/bulk/highlight", func(w http.ResponseWriter, r *http.Request) {
		var body bulkHighlightBody
		serveBulk(w, r, proxy, events, changeHighlight, &body, body.apply)
	})
	mux.HandleFunc("POST /traffic/bulk/delete", func(w http.ResponseWriter, r *http.Request) {
		// Deleted pairs leave the sitemap counts, so clients re-read the
		// nodes they have expanded.
		deleteTraffic := func(_ domain.TrafficRepository, sel domain.Selection) (int, error) { return proxy.DeleteTraffic(sel) }
		if serveBulk(w, r, proxy, events, changeDelete, &bulkSelection{}, deleteTraffic) {
			events.publishSitemapChanged("delete")
		}
	})
	// Delete and unlink go through the proxy, which refuses to take a
	// pending launch's pair out of its launchpad (ADR-0053).
	unlinkTraffic := func(_ domain.TrafficRepository, sel domain.Selection, target domain.LinkTarget) (int, error) {
		return proxy.UnlinkTraffic(sel, target)
	}
	for change, run := range map[bulkChange]func(domain.TrafficRepository, domain.Selection, domain.LinkTarget) (int, error){
		changeLink:   domain.TrafficRepository.LinkTraffic,
		changeUnlink: unlinkTraffic,
	} {
		mux.HandleFunc("POST /traffic/bulk/"+string(change), func(w http.ResponseWriter, r *http.Request) {
			var body bulkLinkBody
			serveBulk(w, r, proxy, events, change, &body, func(repo domain.TrafficRepository, sel domain.Selection) (int, error) {
				target, err := body.target()
				if err != nil {
					return 0, err
				}
				return run(repo, sel, target)
			})
		})
	}
}
