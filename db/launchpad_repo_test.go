package db

import (
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

		err = repo.UpdateLaunchpad(id, wantName, wantDesc)
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

		err = repo.UpdateLaunchpad(id, wantName, "") // Empty description
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

		err = repo.UpdateLaunchpad(id, "", wantDesc) // Empty name
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
		err := repo.UpdateLaunchpad(nonExistentID, "Test", "Test")

		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "no launchpad found") {
			t.Fatalf("\nwanted:\nerror containing 'no launchpad found'\ngot:\n%v", err)
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

		reqID1 := testRequest(t, repo, nil)
		reqID2 := testRequest(t, repo, nil)
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

		req1Row, err := repo.GetRequestResponseRow(reqID1)
		if err != nil {
			t.Fatalf("getting row for req1: %v", err)
		}

		req2Row, err := repo.GetRequestResponseRow(reqID2)
		if err != nil {
			t.Fatalf("getting row for req2: %v", err)
		}

		want := []*domain.ProxyRequest{&req1Row.Request, &req2Row.Request}

		got, err := repo.GetLaunchpadRequests(launchpadID)
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
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Fatalf("\nwanted:\nerror containing 'UNIQUE constraint failed'\ngot:\n%v", err)
		}
	})
}
