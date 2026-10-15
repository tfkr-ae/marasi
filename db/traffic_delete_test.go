package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// ownedRows are the tables that hold rows belonging to a pair (ADR-0034),
// each with a query counting the rows that belong to the pair bound to ?.
var ownedRows = map[string]string{
	"request":               `SELECT count(*) FROM request WHERE id = ?`,
	"notes":                 `SELECT count(*) FROM notes WHERE request_id = ?`,
	"launchpad_request":     `SELECT count(*) FROM launchpad_request WHERE request_id = ?`,
	"finding_requests":      `SELECT count(*) FROM finding_requests WHERE request_id = ?`,
	"test_case_requests":    `SELECT count(*) FROM test_case_requests WHERE request_id = ?`,
	"armory_entry":          `SELECT count(*) FROM armory_entry WHERE request_id = ?`,
	"websocket_connections": `SELECT count(*) FROM websocket_connections WHERE request_id = ?`,
	"websocket_messages": `SELECT count(*) FROM websocket_messages m
		JOIN websocket_connections c ON c.id = m.connection_id WHERE c.request_id = ?`,
	"logs":              `SELECT count(*) FROM logs WHERE request_id = ?`,
	"pair_highlight":    `SELECT count(*) FROM pair_highlight WHERE request_id = ?`,
	"pair_prettified":   `SELECT count(*) FROM pair_prettified WHERE request_id = ?`,
	"pair_sitemap":      `SELECT count(*) FROM pair_sitemap WHERE request_id = ?`,
	"traffic_index_key": `SELECT count(*) FROM traffic_index_key WHERE request_id = ?`,
}

// pairOwners are the records a fully annotated pair is linked to.
type pairOwners struct {
	launchpad, finding, testCase, run uuid.UUID
}

// newPairOwners creates a launchpad, a finding, a test case and an Armory run
// for pairs to be linked to.
func newPairOwners(t *testing.T, repo *Repository) pairOwners {
	t.Helper()
	var owners pairOwners
	var err error
	if owners.launchpad, err = repo.CreateLaunchpad("delete", ""); err != nil {
		t.Fatal(err)
	}
	owners.finding = uuid.New()
	if err := repo.SaveFinding(&domain.Finding{ID: owners.finding, Title: "delete", Severity: "Low"}); err != nil {
		t.Fatal(err)
	}
	owners.testCase = uuid.New()
	if err := repo.SaveTestCase(&domain.TestCase{ID: owners.testCase, Title: "delete"}); err != nil {
		t.Fatal(err)
	}
	template := &domain.ArmoryTemplate{ID: uuid.New(), Name: "delete", RawTemplate: "GET / HTTP/1.1\r\n\r\n"}
	if err := repo.CreateArmoryTemplate(template); err != nil {
		t.Fatal(err)
	}
	owners.run = uuid.New()
	err = repo.CreateArmoryRun(&domain.ArmoryRun{
		ID: owners.run, TemplateID: template.ID, TemplateSnapshot: template.RawTemplate,
		Status: domain.ArmoryRunCompleted, AttackType: domain.ArmoryAttackHarpoon, MaxConcurrent: 1, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return owners
}

// captureOwnedPair captures a pair whose request and response bodies hold
// text, and gives it a row in every table that can hold one of its rows.
func captureOwnedPair(t *testing.T, repo *Repository, owners pairOwners, text string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: "https", Method: "POST", Host: "a.example.com", Path: "/" + text,
		Raw: []byte("POST /" + text + " HTTP/1.1\r\n\r\nrequest-" + text), Prettified: "pretty-request",
		Metadata: map[string]any{}, RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = repo.InsertResponse(&domain.ProxyResponse{
		ID: id, Status: "500", StatusCode: 500, ContentType: "application/json",
		Raw: []byte("HTTP/1.1 500 Internal Server Error\r\n\r\nresponse-" + text), Prettified: "pretty-response",
		Metadata: map[string]any{}, RespondedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := []func() error{
		func() error { return repo.UpdateNote(id, "note-"+text) },
		func() error { return repo.UpdateMetadata(map[string]any{"tag": "metadata-" + text}, id) },
		func() error { _, err := repo.SetHighlight(domain.Selection{IDs: []uuid.UUID{id}}, 3); return err },
		func() error { return repo.LinkRequestToLaunchpad(id, owners.launchpad) },
		func() error { return repo.LinkRequestToFinding(owners.finding, id) },
		func() error { return repo.LinkRequestToTestCase(owners.testCase, id) },
		func() error { return repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: owners.run, RequestID: id}) },
		func() error {
			return repo.InsertLog(&domain.Log{ID: uuid.New(), Timestamp: time.Now(), Level: "INFO", Message: "log", RequestID: &id})
		},
		func() error {
			conn := &domain.WebSocketConnection{ID: uuid.New(), RequestID: id, State: "open", Transport: "wss", StartedAt: time.Now()}
			if err := repo.InsertConnection(conn); err != nil {
				return err
			}
			return repo.InsertMessage(&domain.WebSocketMessage{
				ID: uuid.New(), ConnectionID: conn.ID, RequestID: id, Direction: "client", Opcode: 1,
				Payload: []byte("hello"), CreatedAt: time.Now(), Metadata: map[string]any{},
			})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("annotating pair %s: %v", text, err)
		}
	}
	return id
}

// ownedRowCounts reports, per table, how many rows belong to the pair id.
func ownedRowCounts(t *testing.T, repo *Repository, id uuid.UUID) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for table, query := range ownedRows {
		var n int
		if err := repo.dbConn.Get(&n, query, id); err != nil {
			t.Fatalf("counting %s rows: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// indexRows reports how many rows each search index holds.
func indexRows(t *testing.T, repo *Repository) string {
	t.Helper()
	var body, annotation int
	if err := repo.dbConn.Get(&body, `SELECT count(*) FROM traffic_index_docsize`); err != nil {
		t.Fatal(err)
	}
	if err := repo.dbConn.Get(&annotation, `SELECT count(*) FROM traffic_annotation_index_docsize`); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("body %d, annotation %d", body, annotation)
}

func TestTrafficRepo_DeleteTraffic(t *testing.T) {
	t.Run("should delete every row the pairs own and nothing of the pairs kept", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		owners := newPairOwners(t, repo)
		gone := captureOwnedPair(t, repo, owners, "gone")
		kept := captureOwnedPair(t, repo, owners, "kept")

		count, err := repo.DeleteTraffic(domain.Selection{IDs: []uuid.UUID{gone, uuid.New()}})
		if err != nil {
			t.Fatalf("DeleteTraffic: %v", err)
		}
		if count != 1 {
			t.Fatalf("\nwanted:\n1 pair deleted\ngot:\n%d", count)
		}
		for table, n := range ownedRowCounts(t, repo, gone) {
			if n != 0 {
				t.Errorf("%s holds %d rows of the deleted pair", table, n)
			}
		}
		for table, n := range ownedRowCounts(t, repo, kept) {
			if n == 0 {
				t.Errorf("%s holds no row of the kept pair", table)
			}
		}
		if got, want := indexRows(t, repo), "body 1, annotation 1"; got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should leave no text of the deleted pairs to search", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		owners := newPairOwners(t, repo)
		ids := map[string]uuid.UUID{
			"gone": captureOwnedPair(t, repo, owners, "gone"),
			"kept": captureOwnedPair(t, repo, owners, "kept"),
		}
		if _, err := repo.DeleteTraffic(domain.Selection{IDs: []uuid.UUID{ids["gone"]}}); err != nil {
			t.Fatalf("DeleteTraffic: %v", err)
		}
		for _, query := range []string{`"response-gone"`, `"request-gone"`, `note:"note-gone"`, `"metadata-gone"`} {
			assertListNames(t, repo, ids, "after the delete", query, "")
		}
		assertListNames(t, repo, ids, "after the delete", `"response-kept"`, "kept")
	})
}

// Ledger case 25: with FTS5's default deletemerge of 10%, a delete of a
// tenth of the pairs rewrote every segment of the body index inside the
// delete's transaction. Both indexes must leave tombstones to the automerge.
func TestTrafficRepo_DeleteTrafficLeavesIndexMergesToAutomerge(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	for _, index := range []string{"traffic_index", "traffic_annotation_index"} {
		var deletemerge string
		err := repo.dbConn.Get(&deletemerge, `SELECT v FROM `+index+`_config WHERE k = 'deletemerge'`)
		if err != nil || deletemerge != "0" {
			t.Errorf("\nwanted:\n%s deletemerge 0\ngot:\n%q, %v", index, deletemerge, err)
		}
	}
}

func formatTypeCounts(counts domain.ContentTypeCounts) string {
	var s string
	for _, count := range counts.Counts {
		s += fmt.Sprintf("%q %d; ", count.ContentType, count.Count)
	}
	return s
}

func TestTrafficRepo_DeleteTrafficSitemap(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	var ids []uuid.UUID
	for range 2 {
		for _, pair := range sitemapPairs {
			ids = append(ids, capturePair(t, repo, pair.url, pair.contentType, pair.status))
		}
	}
	// Every pair of the first round, and the second round's server errors
	// and in-flight pairs, leaving some nodes with no pairs at all.
	selected := ids[:len(sitemapPairs):len(sitemapPairs)]
	for i, pair := range sitemapPairs {
		if pair.status == 0 || pair.status >= 500 {
			selected = append(selected, ids[len(sitemapPairs)+i])
		}
	}
	if _, err := repo.DeleteTraffic(domain.Selection{IDs: selected}); err != nil {
		t.Fatalf("DeleteTraffic: %v", err)
	}
	exclusion := []string{"application/json"}
	got := sitemapTree(t, repo) + "\n--\n" + sitemapTree(t, repo, exclusion...)
	gotTypes, err := repo.CountContentTypes()
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.RebuildJob(domain.JobSitemap); err != nil {
		t.Fatalf("RebuildJob: %v", err)
	}
	runSitemapJob(t, repo, 100)
	want := sitemapTree(t, repo) + "\n--\n" + sitemapTree(t, repo, exclusion...)
	if got != want {
		t.Fatalf("\nwanted the rebuilt tree:\n%s\ngot:\n%s", want, got)
	}
	wantTypes, err := repo.CountContentTypes()
	if err != nil {
		t.Fatal(err)
	}
	if formatTypeCounts(gotTypes) != formatTypeCounts(wantTypes) {
		t.Fatalf("\nwanted the rebuilt content-type counts:\n%s\ngot:\n%s", formatTypeCounts(wantTypes), formatTypeCounts(gotTypes))
	}
}
