package db

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// launchpadMembers returns the ids linked to a launchpad, oldest first.
func launchpadMembers(t *testing.T, repo *Repository, launchpadID uuid.UUID) []uuid.UUID {
	t.Helper()
	items, err := repo.GetLaunchpadRequests(launchpadID, "", nil, "")
	if err != nil {
		t.Fatalf("GetLaunchpadRequests() error = %v", err)
	}
	ids := make([]uuid.UUID, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

func TestTrafficRepo_LinkTraffic_QuerySelectionStopsAtAsOf(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	launchpadID, err := repo.CreateLaunchpad("bulk", "")
	if err != nil {
		t.Fatalf("CreateLaunchpad() error = %v", err)
	}
	match1 := insertTraffic(t, repo, "POST", "a.example.com", "/one")
	insertTraffic(t, repo, "GET", "a.example.com", "/skip")
	match2 := insertTraffic(t, repo, "POST", "a.example.com", "/two")
	asOf := match2
	insertTraffic(t, repo, "POST", "a.example.com", "/late") // matches, but after as_of

	sel := domain.Selection{Query: &domain.SelectionQuery{Text: `method = "POST"`, AsOf: asOf}}
	target := domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: launchpadID}

	count, err := repo.LinkTraffic(sel, target)
	if err != nil {
		t.Fatalf("LinkTraffic() error = %v", err)
	}
	if count != 2 {
		t.Errorf("LinkTraffic() count = %d, want 2", count)
	}
	if got, want := launchpadMembers(t, repo, launchpadID), []uuid.UUID{match1, match2}; !slices.Equal(got, want) {
		t.Errorf("launchpad members = %v, want %v", got, want)
	}

	count, err = repo.LinkTraffic(sel, target)
	if err != nil {
		t.Fatalf("second LinkTraffic() error = %v", err)
	}
	if count != 0 {
		t.Errorf("second LinkTraffic() count = %d, want 0", count)
	}
}

func TestTrafficRepo_LinkTraffic_LargeIDSelection(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	finding := &domain.Finding{ID: uuid.New(), Title: "bulk", Severity: "Low"}
	if err := repo.SaveFinding(finding); err != nil {
		t.Fatalf("SaveFinding() error = %v", err)
	}
	known := []uuid.UUID{
		insertTraffic(t, repo, "GET", "a.example.com", "/1"),
		insertTraffic(t, repo, "GET", "a.example.com", "/2"),
		insertTraffic(t, repo, "GET", "a.example.com", "/3"),
	}
	// 50,000 ids is more than SQLite's parameter limit; all but three are
	// unknown and must be ignored, not counted.
	ids := make([]uuid.UUID, 0, 50_000)
	ids = append(ids, known...)
	for len(ids) < 50_000 {
		ids = append(ids, uuid.New())
	}

	count, err := repo.LinkTraffic(domain.Selection{IDs: ids}, domain.LinkTarget{Kind: domain.LinkFinding, ID: finding.ID})
	if err != nil {
		t.Fatalf("LinkTraffic() error = %v", err)
	}
	if count != 3 {
		t.Errorf("LinkTraffic() count = %d, want 3", count)
	}
	got, err := repo.GetFinding(finding.ID)
	if err != nil {
		t.Fatalf("GetFinding() error = %v", err)
	}
	if !sameIDs(got.Requests, known) {
		t.Errorf("finding requests = %v, want %v", got.Requests, known)
	}
}

func TestTrafficRepo_UnlinkTraffic_CountsOnlyLinkedPairs(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	testCase := &domain.TestCase{ID: uuid.New(), Title: "bulk"}
	if err := repo.SaveTestCase(testCase); err != nil {
		t.Fatalf("SaveTestCase() error = %v", err)
	}
	linked := insertTraffic(t, repo, "GET", "a.example.com", "/linked")
	unlinked := insertTraffic(t, repo, "GET", "a.example.com", "/unlinked")
	kept := insertTraffic(t, repo, "GET", "a.example.com", "/kept")
	for _, id := range []uuid.UUID{linked, kept} {
		if err := repo.LinkRequestToTestCase(testCase.ID, id); err != nil {
			t.Fatalf("LinkRequestToTestCase() error = %v", err)
		}
	}
	target := domain.LinkTarget{Kind: domain.LinkTestCase, ID: testCase.ID}

	count, err := repo.UnlinkTraffic(domain.Selection{IDs: []uuid.UUID{linked, unlinked}}, target)
	if err != nil {
		t.Fatalf("UnlinkTraffic() error = %v", err)
	}
	if count != 1 {
		t.Errorf("UnlinkTraffic() count = %d, want 1", count)
	}
	got, err := repo.GetTestCase(testCase.ID)
	if err != nil {
		t.Fatalf("GetTestCase() error = %v", err)
	}
	if !sameIDs(got.Requests, []uuid.UUID{kept}) {
		t.Errorf("test case requests = %v, want [%v]", got.Requests, kept)
	}
}

// sameIDs reports whether a and b hold the same ids in any order.
func sameIDs(a, b []uuid.UUID) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	compare := func(x, y uuid.UUID) int { return slices.Compare(x[:], y[:]) }
	slices.SortFunc(a, compare)
	slices.SortFunc(b, compare)
	return slices.Equal(a, b)
}

func TestTrafficRepo_LinkTraffic_InvalidQuery(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	launchpadID, err := repo.CreateLaunchpad("bulk", "")
	if err != nil {
		t.Fatalf("CreateLaunchpad() error = %v", err)
	}
	asOf := insertTraffic(t, repo, "GET", "a.example.com", "/")

	sel := domain.Selection{Query: &domain.SelectionQuery{Text: `method = `, AsOf: asOf}}
	_, err = repo.LinkTraffic(sel, domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: launchpadID})
	var queryErr *domain.QueryError
	if !errors.As(err, &queryErr) {
		t.Errorf("LinkTraffic(invalid query) error = %v, want *domain.QueryError", err)
	}
}

func TestLaunchpadRepo_UnlinkRequestFromLaunchpad(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	launchpadID, err := repo.CreateLaunchpad("unlink", "")
	if err != nil {
		t.Fatalf("CreateLaunchpad() error = %v", err)
	}
	removed := insertTraffic(t, repo, "GET", "a.example.com", "/removed")
	kept := insertTraffic(t, repo, "GET", "a.example.com", "/kept")
	for _, id := range []uuid.UUID{removed, kept} {
		if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
			t.Fatalf("LinkRequestToLaunchpad() error = %v", err)
		}
	}

	if err := repo.UnlinkRequestFromLaunchpad(removed, launchpadID); err != nil {
		t.Fatalf("UnlinkRequestFromLaunchpad() error = %v", err)
	}
	if got, want := launchpadMembers(t, repo, launchpadID), []uuid.UUID{kept}; !slices.Equal(got, want) {
		t.Errorf("launchpad members = %v, want %v", got, want)
	}
	if err := repo.UnlinkRequestFromLaunchpad(removed, launchpadID); !errors.Is(err, domain.ErrLaunchpadNotLinked) {
		t.Errorf("second UnlinkRequestFromLaunchpad() error = %v, want ErrLaunchpadNotLinked", err)
	}
}

func TestTrafficRepo_LinkTraffic_MissingTarget(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	id := insertTraffic(t, repo, "GET", "a.example.com", "/")
	sel := domain.Selection{IDs: []uuid.UUID{id}}

	for _, kind := range []domain.LinkKind{domain.LinkLaunchpad, domain.LinkFinding, domain.LinkTestCase} {
		target := domain.LinkTarget{Kind: kind, ID: uuid.New()}
		if _, err := repo.LinkTraffic(sel, target); !errors.Is(err, domain.ErrLinkTargetNotFound) {
			t.Errorf("LinkTraffic(missing %s) error = %v, want ErrLinkTargetNotFound", kind, err)
		}
		if _, err := repo.UnlinkTraffic(sel, target); !errors.Is(err, domain.ErrLinkTargetNotFound) {
			t.Errorf("UnlinkTraffic(missing %s) error = %v, want ErrLinkTargetNotFound", kind, err)
		}
	}
}
