package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestLaunchpadRepo_GetLaunchpads(t *testing.T) {
	t.Run("should return 0 launchpads if none are configured", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := 0
		got, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 0 {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want, len(got))
		}
	})
	t.Run("should return the correct launchpad counts if there are ones configured", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadIDOne, err := repo.CreateLaunchpad("Test Launchpad 1", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad 1: %v", err)
		}

		launchpadIDTwo, err := repo.CreateLaunchpad("Test Launchpad 2", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad 2: %v", err)
		}

		want := []*domain.Launchpad{
			{ID: launchpadIDOne, Name: "Test Launchpad 1", Description: "Test Description"},
			{ID: launchpadIDTwo, Name: "Test Launchpad 2", Description: "Test Description"},
		}

		got, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 2 {
			t.Fatalf("\nwanted:\n2\ngot:\n%d", len(got))
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
}

func TestLaunchpadRepo_GetLaunchpad(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	id, err := repo.CreateLaunchpad("Test Launchpad", "Test Description")
	if err != nil {
		t.Fatalf("creating launchpad: %v", err)
	}

	want := &domain.Launchpad{ID: id, Name: "Test Launchpad", Description: "Test Description"}
	got, err := repo.GetLaunchpad(id)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
	}

	missing := uuid.MustParse("01937f48-a14a-74b8-8c50-3d5f8f80ea0c")
	if _, err := repo.GetLaunchpad(missing); err == nil {
		t.Fatal("\nwanted:\nerror\ngot:\nnil")
	}
}

func TestLaunchpadRepo_CreateLaunchpad(t *testing.T) {
	t.Run("should create a launchpad", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantName := "Marasi Test Launchpad"
		wantDesc := "Test Description"

		id, err := repo.CreateLaunchpad(wantName, wantDesc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if id == uuid.Nil {
			t.Fatalf("\nwanted:\nnon-nil uuid\ngot:\n%v", id)
		}

		got, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", len(got))
		}

		if got[0].ID != id {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", id, got[0].ID)
		}
		if got[0].Name != wantName {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantName, got[0].Name)
		}
		if got[0].Description != wantDesc {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantDesc, got[0].Description)
		}
	})
}

func TestLaunchpadRepo_UpdateLaunchpad(t *testing.T) {
	t.Run("should update an existing launchpad", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		initialName := "Initial Name"
		initialDesc := "Initial Description"

		id, err := repo.CreateLaunchpad(initialName, initialDesc)
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		wantName := "Updated Name"
		wantDesc := "Updated Desc"

		err = repo.UpdateLaunchpad(id, &wantName, &wantDesc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", len(got))
		}

		if got[0].Name != wantName {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantName, got[0].Name)
		}

		if got[0].Description != wantDesc {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantDesc, got[0].Description)
		}
	})

	t.Run("should only update name if description is empty", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		initialName := "Initial Name"
		wantDesc := "Initial Desc"

		id, err := repo.CreateLaunchpad(initialName, wantDesc)
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		wantName := "Updated Name"

		err = repo.UpdateLaunchpad(id, &wantName, nil)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got[0].Name != wantName {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantName, got[0].Name)
		}

		if got[0].Description != wantDesc {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantDesc, got[0].Description)
		}
	})

	t.Run("should only update description if name is empty", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantName := "Initial Name"
		initialDesc := "Initial Desc"

		id, err := repo.CreateLaunchpad(wantName, initialDesc)
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		wantDesc := "Updated Desc"

		err = repo.UpdateLaunchpad(id, nil, &wantDesc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got[0].Name != wantName {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantName, got[0].Name)
		}

		if got[0].Description != wantDesc {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantDesc, got[0].Description)
		}
	})

	t.Run("should return an error when updating a launchpad that doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("01937f48-a14a-74b8-8c50-3d5f8f80ea0c")
		name, description := "Test", "Test"
		err := repo.UpdateLaunchpad(nonExistentID, &name, &description)

		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !errors.Is(err, domain.ErrLaunchpadNotFound) {
			t.Fatalf("\nwanted:\nlaunchpad not found\ngot:\n%v", err)
		}
	})

	t.Run("should clear the description when an empty description is supplied", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id, err := repo.CreateLaunchpad("Test", "Initial Description")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		description := ""
		if err := repo.UpdateLaunchpad(id, nil, &description); err != nil {
			t.Fatalf("updating launchpad: %v", err)
		}
		got, err := repo.GetLaunchpad(id)
		if err != nil {
			t.Fatalf("getting launchpad: %v", err)
		}
		if got.Description != "" {
			t.Fatalf("\nwanted:\nempty description\ngot:\n%q", got.Description)
		}
	})
}
func TestLaunchpadRepo_DeleteLaunchpad(t *testing.T) {
	t.Run("should delete an existing launchpad", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id, err := repo.CreateLaunchpad("Test LP", "Test Desc")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		err = repo.DeleteLaunchpad(id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		launchpads, err := repo.GetLaunchpads()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(launchpads) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(launchpads))
		}
	})

	t.Run("should return an error when deleting a launchpad that doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("01937f4c-1d9c-719a-9e38-4e96e05391e6")
		err := repo.DeleteLaunchpad(nonExistentID)

		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "no launchpad with id") {
			t.Fatalf("\nwanted:\nerror containing 'no launchpad with id'\ngot:\n%v", err)
		}
	})
}

func TestLaunchpadRepo_GetLaunchpadRequests(t *testing.T) {
	t.Run("should return an empty slice if no requests are linked", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Test Launchpad", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		_ = testRequest(t, repo, nil)

		requests, err := repo.GetLaunchpadRequests(launchpadID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(requests) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(requests))
		}
	})

	t.Run("should return all linked requests of a launchpad", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Test Launchpad", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad 1: %v", err)
		}

		launchpadID2, err := repo.CreateLaunchpad("Test Launchpad 2", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad 2: %v", err)
		}

		reqID1 := testRequest(t, repo, map[string]any{"prettified-request": "omit", "source": "seed"})
		reqID2 := testRequest(t, repo, nil)
		response2 := insertTestResponseAndGet(t, repo, reqID2, nil)
		_ = testRequest(t, repo, nil)
		reqID4_other_launchpad := testRequest(t, repo, nil)

		err = repo.LinkRequestToLaunchpad(reqID1, launchpadID)
		if err != nil {
			t.Fatalf("linking req1 to launchpad 1: %v", err)
		}

		err = repo.LinkRequestToLaunchpad(reqID2, launchpadID)
		if err != nil {
			t.Fatalf("linking req2 to launchpad 1: %v", err)
		}

		err = repo.LinkRequestToLaunchpad(reqID4_other_launchpad, launchpadID2)
		if err != nil {
			t.Fatalf("linking req4 to launchpad 2: %v", err)
		}

		got, err := repo.GetLaunchpadRequests(launchpadID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 2 {
			t.Fatalf("\nwanted:\n2\ngot:\n%d", len(got))
		}

		if got[0].ID != reqID1 || got[1].ID != reqID2 {
			t.Fatalf("\nwanted oldest-first:\n%s, %s\ngot:\n%s, %s", reqID1, reqID2, got[0].ID, got[1].ID)
		}
		if got[0].StatusCode != -1 || got[0].Status != "N/A" || !got[0].RespondedAt.IsZero() {
			t.Fatalf("\nwanted:\nin-flight response defaults\ngot:\n%+v", got[0])
		}
		if got[0].Metadata["source"] != "seed" {
			t.Fatalf("\nwanted:\nseed metadata\ngot:\n%v", got[0].Metadata)
		}
		if _, ok := got[0].Metadata["prettified-request"]; ok {
			t.Fatalf("\nwanted:\nprettified metadata omitted\ngot:\n%v", got[0].Metadata)
		}
		if got[1].StatusCode != response2.StatusCode || got[1].Status != response2.Status || !got[1].RespondedAt.Equal(response2.RespondedAt) {
			t.Fatalf("\nwanted:\nresponse fields from %+v\ngot:\n%+v", response2, got[1])
		}
	})

	t.Run("should return an empty slice for a non-existent launchpad ID", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("01937f5d-351a-7e68-936d-61a7a25661a3")

		requests, err := repo.GetLaunchpadRequests(nonExistentID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(requests) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(requests))
		}
	})
}

func TestLaunchpadRepo_LinkRequestToLaunchpad(t *testing.T) {
	t.Run("should link a request to launchpad", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Test Launchpad", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToLaunchpad(reqID, launchpadID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		requests, err := repo.GetLaunchpadRequests(launchpadID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(requests) != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", len(requests))
		}
		if requests[0].ID != reqID {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", reqID, requests[0].ID)
		}
	})

	t.Run("should return an error if request ID doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Test Launchpad", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		nonExistentReqID := uuid.MustParse("01937f54-a5e2-7e04-8b63-71a2e7c3e803")

		err = repo.LinkRequestToLaunchpad(nonExistentReqID, launchpadID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			t.Fatalf("\nwanted:\nerror containing 'FOREIGN KEY constraint failed'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if launchpad ID doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentLpID := uuid.MustParse("01937f56-2a78-7568-a477-5060d4b68452")
		reqID := testRequest(t, repo, nil)

		err := repo.LinkRequestToLaunchpad(reqID, nonExistentLpID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			t.Fatalf("\nwanted:\nerror containing 'FOREIGN KEY constraint failed'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if link already exists", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Test Launchpad", "Test Description")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		reqID := testRequest(t, repo, nil)

		err = repo.LinkRequestToLaunchpad(reqID, launchpadID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.LinkRequestToLaunchpad(reqID, launchpadID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
		if !errors.Is(err, domain.ErrLaunchpadAlreadyLinked) {
			t.Fatalf("\nwanted:\nrequest already linked\ngot:\n%v", err)
		}
	})

	t.Run("should link one request to two launchpads", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		first, err := repo.CreateLaunchpad("First", "")
		if err != nil {
			t.Fatalf("creating first launchpad: %v", err)
		}
		second, err := repo.CreateLaunchpad("Second", "")
		if err != nil {
			t.Fatalf("creating second launchpad: %v", err)
		}
		requestID := testRequest(t, repo, nil)
		if err := repo.LinkRequestToLaunchpad(requestID, first); err != nil {
			t.Fatalf("linking request to first launchpad: %v", err)
		}
		if err := repo.LinkRequestToLaunchpad(requestID, second); err != nil {
			t.Fatalf("linking request to second launchpad: %v", err)
		}

		for _, launchpadID := range []uuid.UUID{first, second} {
			members, err := repo.GetLaunchpadRequests(launchpadID)
			if err != nil {
				t.Fatalf("getting members for %s: %v", launchpadID, err)
			}
			if len(members) != 1 || members[0].ID != requestID {
				t.Fatalf("\nwanted:\n%s linked to %s\ngot:\n%v", requestID, launchpadID, members)
			}
		}
	})
}
