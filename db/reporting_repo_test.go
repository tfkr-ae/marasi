package db

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestReportingRepo_SaveTestCase(t *testing.T) {
	t.Run("should insert a new test case with the correct fields", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		want := &domain.TestCase{
			ID:          wantID,
			Title:       "Marasi Test Case",
			Description: "Marasi Test Case Description",
			Category:    "Test Category",
			Tags:        []string{"XSS", "Injection"},
			Requests:    []uuid.UUID{},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "Test Note",
		}

		err = repo.SaveTestCase(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(wantID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.CreatedAt.IsZero() {
			t.Errorf("\nexpected:\nCreatedAt to be populated by the database\ngot:\n%s", got.CreatedAt.String())
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should insert a new test case with the correct requests linked", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		reqID1 := testRequest(t, repo, nil)
		reqID2 := testRequest(t, repo, nil)

		want := &domain.TestCase{
			ID:          wantID,
			Title:       "Marasi Linked Requests Test",
			Description: "Testing junction table inserts with foreign keys",
			Category:    "Test Category",
			Tags:        []string{"API", "Fuzzing"},
			Requests:    []uuid.UUID{reqID1, reqID2},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "Test Note",
		}

		err = repo.SaveTestCase(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(wantID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.CreatedAt.IsZero() {
			t.Errorf("\nexpected:\nCreatedAt to be populated by the database\ngot:\n%v", got.CreatedAt)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should get a test case with artifacts", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:          tcID,
			Title:       "Marasi Artifact Test",
			Description: "Test Description",
			Category:    "Test Category",
			Tags:        []string{"XSS"},
			Requests:    []uuid.UUID{},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "",
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "proof.png",
				MimeType: "image/png",
				Size:     2048,
			},
			TestCaseID: &tcID,
			Data:       []byte("fake png data"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got.Artifacts) != 1 {
			t.Fatalf("\nwanted:\n1 artifact\ngot:\n%d artifacts", len(got.Artifacts))
		}

		if got.Artifacts[0].ID != artID || got.Artifacts[0].Filename != "proof.png" {
			t.Errorf("\nwanted:\nArtifact ID %s and Filename proof.png\ngot:\n%+v", artID, got.Artifacts[0])
		}
	})

	t.Run("should update an existing test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		initial := &domain.TestCase{
			ID:          tcID,
			Title:       "Original Title",
			Description: "Original Description",
			Category:    "Original Category",
			Tags:        []string{"Original"},
			Requests:    []uuid.UUID{},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "Original Note",
		}

		if err := repo.SaveTestCase(initial); err != nil {
			t.Fatalf("inserting initial test case: %v", err)
		}

		want := &domain.TestCase{
			ID:          tcID,
			Title:       "Updated Title",
			Description: "Updated Description",
			Category:    "Updated Category",
			Tags:        []string{"Updated", "Tags"},
			Requests:    []uuid.UUID{},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "Updated Note",
		}

		err = repo.SaveTestCase(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should update an existing test case and update the correct linked requests", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		reqID1 := testRequest(t, repo, nil)
		reqID2 := testRequest(t, repo, nil)
		reqID3 := testRequest(t, repo, nil)

		initial := &domain.TestCase{
			ID:          tcID,
			Title:       "Junction Update Test",
			Description: "Testing wipe and replace",
			Category:    "API",
			Tags:        []string{"SQLi"},
			Requests:    []uuid.UUID{reqID1, reqID2},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "",
		}

		if err := repo.SaveTestCase(initial); err != nil {
			t.Fatalf("inserting initial test case: %v", err)
		}

		want := &domain.TestCase{
			ID:          tcID,
			Title:       initial.Title,
			Description: initial.Description,
			Category:    initial.Category,
			Tags:        initial.Tags,
			Requests:    []uuid.UUID{reqID2, reqID3},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        initial.Note,
		}

		err = repo.SaveTestCase(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should update a test case without altering artifacts", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		initial := &domain.TestCase{
			ID:          tcID,
			Title:       "Initial Title",
			Description: "Initial Description",
			Category:    "API",
			Tags:        []string{},
			Requests:    []uuid.UUID{},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "",
		}

		err = repo.SaveTestCase(initial)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "test.txt",
				MimeType: "text/plain",
				Size:     100,
			},
			TestCaseID: &tcID,
			Data:       []byte("hello world"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := &domain.TestCase{
			ID:          tcID,
			Title:       "Updated Title",
			Description: "Updated Description",
			Category:    "API",
			Tags:        []string{},
			Requests:    []uuid.UUID{},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "",
		}

		err = repo.SaveTestCase(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.Title != want.Title {
			t.Errorf("\nwanted:\nTitle '%s'\ngot:\nTitle '%s'", want.Title, got.Title)
		}

		if len(got.Artifacts) != 1 {
			t.Fatalf("\nwanted:\n1 artifact\ngot:\n%d artifacts", len(got.Artifacts))
		}

		if got.Artifacts[0].ID != artID {
			t.Errorf("\nwanted:\nArtifact ID %s\ngot:\nArtifact ID %s", artID, got.Artifacts[0].ID)
		}
	})
}

func TestReportingRepo_SaveFinding(t *testing.T) {
	t.Run("should insert a new finding with the correct fields", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		want := &domain.Finding{
			ID:            fID,
			TestCaseID:    &tcID,
			Title:         "SQL Injection",
			CVSSVector:    "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
			CVSSScore:     9.8,
			Severity:      "Critical",
			WriteUp:       "Found SQLi in the login endpoint",
			TreatmentPlan: "Use parameterized queries.",
			Requests:      []uuid.UUID{},
			Artifacts:     []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.CreatedAt.IsZero() {
			t.Errorf("\nexpected:\nCreatedAt to be populated by the database\ngot:\n%s", got.CreatedAt.String())
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should insert a new finding with the correct requests linked", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		reqID1 := testRequest(t, repo, nil)
		reqID2 := testRequest(t, repo, nil)

		want := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "XSS",
			Severity:   "Medium",
			WriteUp:    "Reflected XSS",
			Requests:   []uuid.UUID{reqID1, reqID2},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should insert a new finding with the correct artifacts linked", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Artifact Test",
			Severity:   "Low",
			WriteUp:    "Has screenshot",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating artifact uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "finding_proof.png",
				MimeType: "image/png",
				Size:     1024,
			},
			TestCaseID: nil,
			FindingID:  &fID,
			Data:       []byte("png data"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got.Artifacts) != 1 {
			t.Fatalf("\nwanted:\n1 artifact\ngot:\n%d artifacts", len(got.Artifacts))
		}

		if got.Artifacts[0].ID != artID || got.Artifacts[0].Filename != "finding_proof.png" {
			t.Errorf("\nwanted:\nArtifact ID %s and Filename finding_proof.png\ngot:\n%+v", artID, got.Artifacts[0])
		}
	})

	t.Run("should update an existing finding", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		initial := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Initial Finding",
			Severity:   "Low",
			WriteUp:    "Initial writeup",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(initial)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Updated Finding",
			Severity:   "Critical",
			WriteUp:    "Updated writeup",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should update an existing finding and update the correct linked requests", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		reqID1 := testRequest(t, repo, nil)
		reqID2 := testRequest(t, repo, nil)
		reqID3 := testRequest(t, repo, nil)

		initial := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Finding with Reqs",
			Severity:   "Low",
			WriteUp:    "",
			Requests:   []uuid.UUID{reqID1, reqID2},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(initial)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      initial.Title,
			Severity:   initial.Severity,
			WriteUp:    initial.WriteUp,
			Requests:   []uuid.UUID{reqID2, reqID3},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should update an existing finding and update the correct linked artifacts", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		initial := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Initial Title",
			Severity:   "Medium",
			WriteUp:    "Initial WriteUp",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(initial)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating artifact uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "finding_test.txt",
				MimeType: "text/plain",
				Size:     100,
			},
			TestCaseID: nil,
			FindingID:  &fID,
			Data:       []byte("finding hello world"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Updated Title",
			Severity:   "Medium",
			WriteUp:    "Updated WriteUp",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.Title != want.Title {
			t.Errorf("\nwanted:\nTitle '%s'\ngot:\nTitle '%s'", want.Title, got.Title)
		}

		if len(got.Artifacts) != 1 {
			t.Fatalf("\nwanted:\n1 artifact\ngot:\n%d artifacts", len(got.Artifacts))
		}

		if got.Artifacts[0].ID != artID {
			t.Errorf("\nwanted:\nArtifact ID %s\ngot:\nArtifact ID %s", artID, got.Artifacts[0].ID)
		}
	})
	t.Run("should insert a new finding without a linked test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		want := &domain.Finding{
			ID:            fID,
			Title:         "Standalone Finding",
			CVSSVector:    "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
			CVSSScore:     9.8,
			Severity:      "Critical",
			WriteUp:       "Found exposed debug endpoint",
			TreatmentPlan: "Disable debug mode.",
			Requests:      []uuid.UUID{},
			Artifacts:     []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.TestCaseID != nil {
			t.Errorf("\nwanted:\nnil TestCaseID\ngot:\n%v", got.TestCaseID)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})
	t.Run("should update a finding to remove its linked test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		if err := repo.SaveTestCase(tc); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		initial := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Linked Finding",
			Severity:   "Low",
			WriteUp:    "Initial writeup",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		if err := repo.SaveFinding(initial); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := &domain.Finding{
			ID:        fID,
			Title:     "Linked Finding",
			Severity:  "Low",
			WriteUp:   "Initial writeup",
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		if err := repo.SaveFinding(want); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got.CreatedAt = want.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})
}

func TestReportingRepo_GetTestCase(t *testing.T) {
	t.Run("should not return a non-existant test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fakeID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		got, err := repo.GetTestCase(fakeID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror (sql.ErrNoRows)\ngot:\nnil")
		}

		if got != nil {
			t.Errorf("\nwanted:\nnil test case\ngot:\n%+v", got)
		}
	})
}

func TestReportingRepo_GetFinding(t *testing.T) {
	t.Run("should not return a non-existant finding", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fakeID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		got, err := repo.GetFinding(fakeID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror (sql.ErrNoRows)\ngot:\nnil")
		}

		if got != nil {
			t.Errorf("\nwanted:\nnil finding\ngot:\n%+v", got)
		}
	})
	t.Run("should successfully get a finding without a linked test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		want := &domain.Finding{
			ID:        fID,
			Title:     "Standalone Retrieval Test",
			Severity:  "Medium",
			WriteUp:   "Writeup",
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.TestCaseID != nil {
			t.Errorf("\nwanted:\nnil TestCaseID\ngot:\n%v", got.TestCaseID)
		}
	})
}

func TestReportingRepo_ListTestCases(t *testing.T) {
	t.Run("should return an empty list when no test cases exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		got, err := repo.ListTestCases()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got == nil {
			t.Fatalf("\nexpected:\nallocated empty slice []\ngot:\nnil")
		}

		if len(got) != 0 {
			t.Errorf("\nwanted:\n0 items\ngot:\n%d items", len(got))
		}
	})

	t.Run("should return all saved test cases", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id1, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid 1: %v", err)
		}

		req1 := testRequest(t, repo, nil)

		tc1 := &domain.TestCase{
			ID:          id1,
			Title:       "Test Case 1",
			Description: "First test case",
			Category:    "API",
			Tags:        []string{"SQLi"},
			Requests:    []uuid.UUID{req1},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "Note 1",
		}

		err = repo.SaveTestCase(tc1)
		if err != nil {
			t.Fatalf("saving tc1: %v", err)
		}

		id2, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid 2: %v", err)
		}

		tc2 := &domain.TestCase{
			ID:          id2,
			Title:       "Test Case 2",
			Description: "Second test case",
			Category:    "Web",
			Tags:        []string{"XSS"},
			Requests:    []uuid.UUID{},
			Artifacts:   []*domain.ArtifactMetadata{},
			Note:        "Note 2",
		}

		err = repo.SaveTestCase(tc2)
		if err != nil {
			t.Fatalf("saving tc2: %v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating artifact uuid: %v", err)
		}

		artifact := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "screenshot.png",
				MimeType: "image/png",
				Size:     1024,
			},
			TestCaseID: &id2,
			Data:       []byte("fake image data"),
		}

		err = repo.SaveArtifact(artifact)
		if err != nil {
			t.Fatalf("saving artifact: %v", err)
		}

		tc2.Artifacts = append(tc2.Artifacts, &domain.ArtifactMetadata{
			ID:       artID,
			Filename: "screenshot.png",
			MimeType: "image/png",
			Size:     1024,
		})

		got, err := repo.ListTestCases()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 2 {
			t.Fatalf("\nwanted:\n2 test cases\ngot:\n%d", len(got))
		}

		gotMap := make(map[uuid.UUID]*domain.TestCase)
		for _, tc := range got {
			gotMap[tc.ID] = tc
		}

		got1, exists1 := gotMap[id1]
		if !exists1 {
			t.Fatalf("test case 1 missing from results")
		}

		got2, exists2 := gotMap[id2]
		if !exists2 {
			t.Fatalf("test case 2 missing from results")
		}

		tc1.CreatedAt = got1.CreatedAt
		tc2.CreatedAt = got2.CreatedAt
		tc2.Artifacts[0].CreatedAt = got2.Artifacts[0].CreatedAt

		if !reflect.DeepEqual(tc1, got1) {
			t.Errorf("\nTest Case 1 mismatch:\nwanted:\n%+v\ngot:\n%+v", tc1, got1)
		}

		if !reflect.DeepEqual(tc2, got2) {
			t.Errorf("\nTest Case 2 mismatch:\nwanted:\n%+v\ngot:\n%+v", tc2, got2)
		}
	})
}

func TestReportingRepo_ListFindings(t *testing.T) {
	t.Run("should return all saved findings", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID1, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid 1: %v", err)
		}

		f1 := &domain.Finding{
			ID:         fID1,
			TestCaseID: nil,
			Title:      "Standalone Finding 1",
			Severity:   "Low",
			WriteUp:    "WriteUp 1",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(f1)
		if err != nil {
			t.Fatalf("saving finding 1: %v", err)
		}

		fID2, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid 2: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		f2 := &domain.Finding{
			ID:         fID2,
			TestCaseID: &tcID,
			Title:      "Linked Finding 2",
			Severity:   "High",
			WriteUp:    "WriteUp 2",
			Requests:   []uuid.UUID{reqID},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(f2)
		if err != nil {
			t.Fatalf("saving finding 2: %v", err)
		}

		got, err := repo.ListFindings()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 2 {
			t.Fatalf("\nwanted:\n2 findings\ngot:\n%d", len(got))
		}

		gotMap := make(map[uuid.UUID]*domain.Finding)
		for _, f := range got {
			gotMap[f.ID] = f
		}

		got1, exists1 := gotMap[fID1]
		if !exists1 {
			t.Fatalf("finding 1 missing from results")
		}

		got2, exists2 := gotMap[fID2]
		if !exists2 {
			t.Fatalf("finding 2 missing from results")
		}

		f1.CreatedAt = got1.CreatedAt
		f2.CreatedAt = got2.CreatedAt

		if !reflect.DeepEqual(f1, got1) {
			t.Errorf("\nFinding 1 mismatch:\nwanted:\n%+v\ngot:\n%+v", f1, got1)
		}

		if !reflect.DeepEqual(f2, got2) {
			t.Errorf("\nFinding 2 mismatch:\nwanted:\n%+v\ngot:\n%+v", f2, got2)
		}
	})
	t.Run("should return an empty list when no findings exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		got, err := repo.ListFindings()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got == nil {
			t.Fatalf("\nexpected:\nallocated empty slice []\ngot:\nnil")
		}

		if len(got) != 0 {
			t.Errorf("\nwanted:\n0 items\ngot:\n%d items", len(got))
		}
	})
}

func TestReportingRepo_LinkRequestToTestCase(t *testing.T) {
	t.Run("should successfully link a request to an existing test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Link Test",
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToTestCase(tcID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got.Requests) != 1 || got.Requests[0] != reqID {
			t.Errorf("\nwanted:\n1 request with ID %s\ngot:\n%v", reqID, got.Requests)
		}
	})

	t.Run("should ignore duplicate links without returning an error", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Duplicate Link Test",
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToTestCase(tcID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.LinkRequestToTestCase(tcID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		var count int
		err = repo.dbConn.Get(&count, "SELECT COUNT(*) FROM test_case_requests WHERE test_case_id = ? AND request_id = ?", tcID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if count != 1 {
			t.Errorf("\nwanted:\n1 junction record\ngot:\n%d junction records", count)
		}
	})

	t.Run("should return a foreign key constraint error if the test case does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fakeTCID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating fake tc uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToTestCase(fakeTCID, reqID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestReportingRepo_UnlinkRequestFromTestCase(t *testing.T) {
	t.Run("should successfully remove an existing request link", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Unlink Test",
			Requests:  []uuid.UUID{reqID},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.UnlinkRequestFromTestCase(tcID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got.Requests) != 0 {
			t.Errorf("\nwanted:\n0 requests\ngot:\n%v", got.Requests)
		}
	})

	t.Run("should not return an error when unlinking a request that is not linked", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.UnlinkRequestFromTestCase(tcID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}

func TestReportingRepo_LinkRequestToFinding(t *testing.T) {
	t.Run("should successfully link a request to an existing finding", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Link Finding Test",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToFinding(fID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got.Requests) != 1 || got.Requests[0] != reqID {
			t.Errorf("\nwanted:\n1 request with ID %s\ngot:\n%v", reqID, got.Requests)
		}
	})

	t.Run("should ignore duplicate links without returning an error", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Duplicate Link Finding Test",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToFinding(fID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.LinkRequestToFinding(fID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		var count int
		err = repo.dbConn.Get(&count, "SELECT COUNT(*) FROM finding_requests WHERE finding_id = ? AND request_id = ?", fID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if count != 1 {
			t.Errorf("\nwanted:\n1 junction record\ngot:\n%d junction records", count)
		}
	})

	t.Run("should return a foreign key constraint error if the finding does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fakeFID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating fake finding uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToFinding(fakeFID, reqID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestReportingRepo_UnlinkRequestFromFinding(t *testing.T) {
	t.Run("should successfully remove an existing request link", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Unlink Finding Test",
			Requests:   []uuid.UUID{reqID},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.UnlinkRequestFromFinding(fID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got.Requests) != 0 {
			t.Errorf("\nwanted:\n0 requests\ngot:\n%v", got.Requests)
		}
	})

	t.Run("should not return an error when unlinking a request that is not linked", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.UnlinkRequestFromFinding(fID, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}

func TestReportingRepo_SaveArtifact(t *testing.T) {
	t.Run("should successfully insert an artifact linked to a test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Test Case Title",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		want := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "test.png",
				MimeType: "image/png",
				Size:     100,
			},
			TestCaseID: &tcID,
			FindingID:  nil,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetArtifact(artID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want.CreatedAt = got.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should successfully insert an artifact linked to a finding", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Parent Finding",
			Severity:   "Low",
			WriteUp:    "",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		want := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "finding_artifact.png",
				MimeType: "image/png",
				Size:     100,
			},
			TestCaseID: nil,
			FindingID:  &fID,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetArtifact(artID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want.CreatedAt = got.CreatedAt

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should fail constraint check if both test_case_id and finding_id are populated", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Parent Finding",
			Severity:   "Low",
			WriteUp:    "",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "fail.png",
				MimeType: "image/png",
				Size:     100,
			},
			TestCaseID: &tcID,
			FindingID:  &fID,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(art)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should fail constraint check if neither test_case_id nor finding_id are populated", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "fail.png",
				MimeType: "image/png",
				Size:     100,
			},
			TestCaseID: nil,
			FindingID:  nil,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(art)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestReportingRepo_DeleteTestCase(t *testing.T) {
	t.Run("should successfully delete an existing test case", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "To Be Deleted",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.DeleteTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = repo.GetTestCase(tcID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should cascade delete linked records in the test_case_requests junction table", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Junction Cascade Parent",
			Tags:      []string{},
			Requests:  []uuid.UUID{reqID},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.DeleteTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		var count int
		err = repo.dbConn.Get(&count, "SELECT COUNT(*) FROM test_case_requests WHERE test_case_id = ?", tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if count != 0 {
			t.Errorf("\nwanted:\n0 junction records\ngot:\n%d junction records", count)
		}
	})

	t.Run("should cascade delete linked artifacts", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Artifact Cascade Parent",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "cascade.png",
				MimeType: "image/png",
				Size:     100,
			},
			TestCaseID: &tcID,
			FindingID:  nil,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.DeleteTestCase(tcID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = repo.GetArtifact(artID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
	t.Run("should orphan (not delete) findings when their parent test case is deleted", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC to be deleted",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		if err := repo.SaveTestCase(tc); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:            fID,
			TestCaseID:    &tcID,
			Title:         "Finding to be orphaned",
			CVSSVector:    "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
			CVSSScore:     9.8,
			Severity:      "Critical",
			WriteUp:       "",
			TreatmentPlan: "",
			Requests:      []uuid.UUID{},
			Artifacts:     []*domain.ArtifactMetadata{},
		}

		if err := repo.SaveFinding(finding); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if err := repo.DeleteTestCase(tcID); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nfinding to exist\ngot:\n%v", err)
		}

		if got.TestCaseID != nil {
			t.Errorf("\nwanted:\nnil TestCaseID\ngot:\n%v", got.TestCaseID)
		}
	})
}
func TestReportingRepo_DeleteFinding(t *testing.T) {
	t.Run("should successfully delete an existing finding", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "To Be Deleted",
			Severity:   "Low",
			WriteUp:    "",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.DeleteFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = repo.GetFinding(fID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should cascade delete linked records in the finding_requests junction table", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Junction Cascade Parent",
			Severity:   "Medium",
			WriteUp:    "",
			Requests:   []uuid.UUID{reqID},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.DeleteFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		var count int
		err = repo.dbConn.Get(&count, "SELECT COUNT(*) FROM finding_requests WHERE finding_id = ?", fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if count != 0 {
			t.Errorf("\nwanted:\n0 junction records\ngot:\n%d junction records", count)
		}
	})

	t.Run("should cascade delete linked artifacts", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		tcID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating tc uuid: %v", err)
		}

		tc := &domain.TestCase{
			ID:        tcID,
			Title:     "Parent TC",
			Tags:      []string{},
			Requests:  []uuid.UUID{},
			Artifacts: []*domain.ArtifactMetadata{},
		}

		err = repo.SaveTestCase(tc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating finding uuid: %v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: &tcID,
			Title:      "Artifact Cascade Parent",
			Severity:   "High",
			WriteUp:    "",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating artifact uuid: %v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:       artID,
				Filename: "cascade_finding.png",
				MimeType: "image/png",
				Size:     100,
			},
			TestCaseID: nil,
			FindingID:  &fID,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.DeleteFinding(fID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = repo.GetArtifact(artID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
}
