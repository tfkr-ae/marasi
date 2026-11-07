package service

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

func TestArmoryProjectRunList(t *testing.T) {
	loginID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367801")
	searchID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367802")
	createdAt := time.Date(2026, time.September, 17, 10, 30, 0, 0, time.UTC)
	draftID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645901")
	runningID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645902")
	completeID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645903")
	otherRunningID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")

	newServer := func(active []uuid.UUID, beforeSnapshot func(*stubArmoryRepository)) (*Server, *stubArmoryService) {
		repo := &stubArmoryRepository{
			templates: map[uuid.UUID]*domain.ArmoryTemplate{
				loginID:  {ID: loginID, Name: "Login"},
				searchID: {ID: searchID, Name: "Search"},
			},
			runs: map[uuid.UUID]*domain.ArmoryRun{},
		}
		add := func(id, templateID uuid.UUID, offset time.Duration, status domain.ArmoryRunStatus) {
			run := testServiceArmoryRun(id, templateID, createdAt.Add(offset))
			run.Status = status
			repo.runs[id] = run
		}
		add(draftID, loginID, 0, domain.ArmoryRunDraft)
		add(runningID, searchID, time.Minute, domain.ArmoryRunInProgress)
		add(completeID, loginID, 2*time.Minute, domain.ArmoryRunCompleted)
		add(otherRunningID, loginID, 3*time.Minute, domain.ArmoryRunInProgress)
		armoryService := &stubArmoryService{repo: repo, active: active}
		if beforeSnapshot != nil {
			armoryService.beforeActive = func() { beforeSnapshot(repo) }
		}
		return newTestServer(&marasi.Proxy{Armory: armoryService, TrafficRepo: &stubTrafficRepository{}}, func() {}), armoryService
	}

	decode := func(t *testing.T, server *Server, path string) []armoryRun {
		t.Helper()
		response := requestControlAPI(server, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		var list armoryRunList
		if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
			t.Fatalf("decoding run list: %v", err)
		}
		return list.Items
	}
	ids := func(runs []armoryRun) []uuid.UUID {
		result := make([]uuid.UUID, 0, len(runs))
		for _, run := range runs {
			result = append(result, run.ID)
		}
		return result
	}
	sorted := func(values []uuid.UUID) []uuid.UUID {
		copy := slices.Clone(values)
		slices.SortFunc(copy, func(a, b uuid.UUID) int { return compareUUID(a, b) })
		return copy
	}

	t.Run("should list runs from every template oldest first in the per-template shape", func(t *testing.T) {
		server, _ := newServer([]uuid.UUID{runningID, otherRunningID}, nil)
		runs := decode(t, server, "/armory/run")
		want := []uuid.UUID{draftID, runningID, completeID, otherRunningID}
		if !slices.Equal(ids(runs), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, ids(runs))
		}
		if runs[0].TemplateID != loginID || runs[1].TemplateID != searchID {
			t.Fatalf("\nwanted:\nruns from both templates\ngot:\n%+v", runs)
		}
		perTemplate := decode(t, server, "/armory/template/"+loginID.String()+"/run")
		if !reflect.DeepEqual(perTemplate[0], runs[0]) {
			t.Fatalf("\nwanted:\nsame element as the per-template list\n%+v\ngot:\n%+v", perTemplate[0], runs[0])
		}
	})

	t.Run("should return exactly the active runs for status in_progress", func(t *testing.T) {
		active := []uuid.UUID{otherRunningID, runningID}
		server, armoryService := newServer(active, nil)
		runs := decode(t, server, "/armory/run?status=in_progress")
		if !slices.Equal(sorted(ids(runs)), sorted(armoryService.ActiveRunIDs())) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", armoryService.ActiveRunIDs(), ids(runs))
		}
		for _, run := range runs {
			if run.Status != domain.ArmoryRunInProgress {
				t.Fatalf("\nwanted:\nin_progress\ngot:\n%+v", run)
			}
		}
	})

	t.Run("should match the active set while a run starts or finishes", func(t *testing.T) {
		// draftID is active but its row still says draft: the start write has not landed.
		// otherRunningID read as in_progress, then finished before the active snapshot.
		server, armoryService := newServer([]uuid.UUID{runningID, draftID}, func(repo *stubArmoryRepository) {
			repo.runs[otherRunningID].Status = domain.ArmoryRunCompleted
		})
		runs := decode(t, server, "/armory/run?status=in_progress")
		if !slices.Equal(sorted(ids(runs)), sorted(armoryService.ActiveRunIDs())) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", armoryService.ActiveRunIDs(), ids(runs))
		}
		all := decode(t, server, "/armory/run")
		for _, run := range all {
			if run.ID == otherRunningID && run.Status != domain.ArmoryRunCompleted {
				t.Fatalf("\nwanted:\nfinished run reported as complete\ngot:\n%+v", run)
			}
		}
	})

	t.Run("should include an active run created after the list was read", func(t *testing.T) {
		lateID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645905")
		server, armoryService := newServer([]uuid.UUID{runningID, otherRunningID, lateID}, func(repo *stubArmoryRepository) {
			run := testServiceArmoryRun(lateID, searchID, createdAt.Add(time.Hour))
			run.Status = domain.ArmoryRunInProgress
			repo.runs[lateID] = run
		})
		runs := decode(t, server, "/armory/run?status=in_progress")
		if !slices.Equal(sorted(ids(runs)), sorted(armoryService.ActiveRunIDs())) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", armoryService.ActiveRunIDs(), ids(runs))
		}
	})

	t.Run("should accept repeated and comma-separated statuses", func(t *testing.T) {
		server, _ := newServer([]uuid.UUID{runningID, otherRunningID}, nil)
		want := []uuid.UUID{draftID, completeID}
		for _, query := range []string{"status=draft&status=complete", "status=draft,complete", "status=complete,draft&status=draft"} {
			if got := ids(decode(t, server, "/armory/run?"+query)); !slices.Equal(got, want) {
				t.Fatalf("%s\nwanted:\n%v\ngot:\n%v", query, want, got)
			}
		}
	})

	t.Run("should return an empty list when nothing matches", func(t *testing.T) {
		server, _ := newServer(nil, nil)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/armory/run?status=failed", ""), http.StatusOK, "{\"items\":[]}\n")
	})

	t.Run("should refuse an unknown status", func(t *testing.T) {
		server, _ := newServer(nil, nil)
		for _, query := range []string{"status=running", "status=", "status=draft,"} {
			response := requestControlAPI(server, http.MethodGet, "/armory/run?"+query, "")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s\nwanted:\n400\ngot:\n%d %s", query, response.Code, response.Body.String())
			}
		}
	})

	t.Run("should return not found without an open project", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{}, func() {})
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/armory/run", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	})
}

func compareUUID(a, b uuid.UUID) int {
	switch {
	case a.String() < b.String():
		return -1
	case a.String() > b.String():
		return 1
	}
	return 0
}
