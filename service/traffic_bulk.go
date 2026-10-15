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

// trafficChanged is the JSON body of a traffic.changed SSE event, which every
// bulk route publishes once. Selection is the selection as received;
// encoding/json compacts its whitespace.
type trafficChanged struct {
	Change    string          `json:"change"` // highlight, metadata, note, link, unlink or delete
	Count     int             `json:"count"`  // number of pairs changed
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
	if r.Body == nil {
		return domain.Selection{}, errInvalidBulkRequest
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		return domain.Selection{}, errInvalidBulkRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
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

// serveBulk runs one bulk operation: it decodes r into body, calls apply with
// the traffic repository and the selection, writes {"count": n}, and
// publishes one traffic.changed event with change. apply may return
// errInvalidBulkRequest for operation fields it rejects. Failures are written as
// JSON errors and publish nothing: a bad body or selection or query is 400,
// a missing target is 404.
func serveBulk(w http.ResponseWriter, r *http.Request, proxy *marasi.Proxy, events *eventBroadcaster, change string, body bulkBody, apply func(repo domain.TrafficRepository, sel domain.Selection) (int, error)) {
	sel, err := decodeBulkBody(r, body)
	if err != nil {
		writeBulkError(w, r, err)
		return
	}
	repo, err := proxy.GetTrafficRepo()
	if err != nil {
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	count, err := apply(repo, sel)
	if err != nil {
		writeBulkError(w, r, err)
		return
	}
	events.publish("traffic.changed", trafficChanged{Change: change, Count: count, Selection: body.rawSelection().Selection})
	writeJSON(w, r, http.StatusOK, bulkResult{Count: count})
}

// writeBulkError writes the response for a failed bulk operation.
func writeBulkError(w http.ResponseWriter, r *http.Request, err error) {
	var queryErr *domain.QueryError
	switch {
	case errors.Is(err, errInvalidBulkRequest), errors.Is(err, domain.ErrEmptyNote):
		writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_request"})
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
		serveBulk(w, r, proxy, events, "metadata", &body, body.apply)
	})
	addTrafficBulkNoteRoute(mux, proxy, events)
	for change, run := range map[string]func(domain.TrafficRepository, domain.Selection, domain.LinkTarget) (int, error){
		"link":   domain.TrafficRepository.LinkTraffic,
		"unlink": domain.TrafficRepository.UnlinkTraffic,
	} {
		mux.HandleFunc("POST /traffic/bulk/"+change, func(w http.ResponseWriter, r *http.Request) {
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
