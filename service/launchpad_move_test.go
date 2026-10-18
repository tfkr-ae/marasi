package service

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// moveProject is a project with one launchpad holding entries in capture order.
func moveProject(t *testing.T, n int) (*bulkProject, uuid.UUID, []uuid.UUID) {
	t.Helper()
	p := setupBulkProject(t)
	launchpadID, err := p.repo.CreateLaunchpad("move", "")
	if err != nil {
		t.Fatalf("CreateLaunchpad() error = %v", err)
	}
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = p.insertPair(t, "GET")
		if err := p.repo.LinkRequestToLaunchpad(ids[i], launchpadID); err != nil {
			t.Fatalf("LinkRequestToLaunchpad() error = %v", err)
		}
	}
	return p, launchpadID, ids
}

func (p *bulkProject) assertEntryOrder(t *testing.T, launchpadID uuid.UUID, want ...uuid.UUID) {
	t.Helper()
	entries, _, err := p.repo.GetLaunchpadRequests(launchpadID, "", nil, "")
	if err != nil {
		t.Fatalf("GetLaunchpadRequests() error = %v", err)
	}
	got := make([]string, len(entries))
	for i, entry := range entries {
		got[i] = entry.ID.String()
	}
	wanted := make([]string, len(want))
	for i, id := range want {
		wanted[i] = id.String()
	}
	if strings.Join(got, ",") != strings.Join(wanted, ",") {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", wanted, got)
	}
}

func idList(ids ...uuid.UUID) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func TestLaunchpadMoveAPI(t *testing.T) {
	t.Run("should move entries and publish one launchpad.entries_moved event", func(t *testing.T) {
		p, pad, e := moveProject(t, 4)
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		body := fmt.Sprintf(`{"request_ids":%s,"target_launchpad_id":%q,"before_request_id":%q}`, idList(e[3], e[2], e[3]), pad, e[0])
		response := requestControlAPI(p.server, http.MethodPost, "/launchpad/"+pad.String()+"/move", body)

		want := fmt.Sprintf(`{"source_launchpad_id":%q,"target_launchpad_id":%q,"request_ids":%s}`, pad, pad, idList(e[2], e[3]))
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		events := drainEvents(subscriber)
		if len(events) != 1 || events[0].name != "launchpad.entries_moved" || string(events[0].data) != want {
			t.Fatalf("\nwanted:\nlaunchpad.entries_moved %s\ngot:\n%v", want, eventSummary(events))
		}
		p.assertEntryOrder(t, pad, e[2], e[3], e[0], e[1])
	})

	t.Run("should move entries to the end of their own launchpad by default", func(t *testing.T) {
		p, pad, e := moveProject(t, 3)

		response := requestControlAPI(p.server, http.MethodPost, "/launchpad/"+pad.String()+"/move", `{"request_ids":`+idList(e[0])+`}`)

		want := fmt.Sprintf(`{"source_launchpad_id":%q,"target_launchpad_id":%q,"request_ids":%s}`, pad, pad, idList(e[0]))
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		p.assertEntryOrder(t, pad, e[1], e[2], e[0])
	})

	t.Run("should refuse a move, publish nothing, and change nothing", func(t *testing.T) {
		p, pad, e := moveProject(t, 3)
		outside := p.insertPair(t, "GET")
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		path := "/launchpad/" + pad.String() + "/move"

		for _, test := range []struct {
			name, path, body string
			status           int
			want             string
		}{
			{"a pair that is not an entry", path, `{"request_ids":` + idList(e[1], outside) + `}`, http.StatusNotFound, `{"error":"not_found"}`},
			{"an anchor that is not an entry", path, `{"request_ids":` + idList(e[1]) + `,"before_request_id":"` + outside.String() + `"}`, http.StatusBadRequest, `{"error":"invalid_placement"}`},
			{"an anchor that is moved", path, `{"request_ids":` + idList(e[1], e[2]) + `,"before_request_id":"` + e[2].String() + `"}`, http.StatusBadRequest, `{"error":"invalid_placement"}`},
			{"a missing launchpad", "/launchpad/" + uuid.NewString() + "/move", `{"request_ids":` + idList(e[1]) + `}`, http.StatusNotFound, `{"error":"not_found"}`},
			{"a bad launchpad id", "/launchpad/junk/move", `{"request_ids":` + idList(e[1]) + `}`, http.StatusBadRequest, `{"error":"bad_request"}`},
			{"no ids", path, `{"request_ids":[]}`, http.StatusBadRequest, `{"error":"invalid_launchpad_request"}`},
			{"a bad id", path, `{"request_ids":["junk"]}`, http.StatusBadRequest, `{"error":"invalid_launchpad_request"}`},
			{"a bad anchor", path, `{"request_ids":` + idList(e[1]) + `,"before_request_id":"junk"}`, http.StatusBadRequest, `{"error":"invalid_launchpad_request"}`},
			{"an unknown field", path, `{"request_ids":` + idList(e[1]) + `,"extra":1}`, http.StatusBadRequest, `{"error":"invalid_launchpad_request"}`},
			{"no body", path, ``, http.StatusBadRequest, `{"error":"invalid_launchpad_request"}`},
		} {
			response := requestControlAPI(p.server, http.MethodPost, test.path, test.body)
			if response.Code != test.status || response.Body.String() != test.want+"\n" {
				t.Errorf("%s: got %d %s, want %d %s", test.name, response.Code, response.Body, test.status, test.want)
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
		p.assertEntryOrder(t, pad, e...)
	})

	t.Run("should move entries to another launchpad and name the source and the target in the event", func(t *testing.T) {
		p, pad, e := moveProject(t, 4)
		target, err := p.repo.CreateLaunchpad("target", "")
		if err != nil {
			t.Fatalf("CreateLaunchpad() error = %v", err)
		}
		held := p.insertPair(t, "GET")
		if err := p.repo.LinkRequestToLaunchpad(held, target); err != nil {
			t.Fatalf("LinkRequestToLaunchpad() error = %v", err)
		}
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		body := fmt.Sprintf(`{"request_ids":%s,"target_launchpad_id":%q,"before_request_id":%q}`, idList(e[3], e[1]), target, held)
		response := requestControlAPI(p.server, http.MethodPost, "/launchpad/"+pad.String()+"/move", body)

		want := fmt.Sprintf(`{"source_launchpad_id":%q,"target_launchpad_id":%q,"request_ids":%s}`, pad, target, idList(e[1], e[3]))
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		events := drainEvents(subscriber)
		if len(events) != 1 || events[0].name != "launchpad.entries_moved" || string(events[0].data) != want {
			t.Fatalf("\nwanted:\nlaunchpad.entries_moved %s\ngot:\n%v", want, eventSummary(events))
		}
		p.assertEntryOrder(t, pad, e[0], e[2])
		p.assertEntryOrder(t, target, e[1], e[3], held)
	})

	t.Run("should refuse a move to another launchpad, publish nothing, and change nothing", func(t *testing.T) {
		p, pad, e := moveProject(t, 2)
		target, err := p.repo.CreateLaunchpad("target", "")
		if err != nil {
			t.Fatalf("CreateLaunchpad() error = %v", err)
		}
		if err := p.repo.LinkRequestToLaunchpad(e[1], target); err != nil {
			t.Fatalf("LinkRequestToLaunchpad() error = %v", err)
		}
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		path := "/launchpad/" + pad.String() + "/move"

		for _, test := range []struct {
			name, body string
			status     int
			want       string
		}{
			{"a pair the target already holds", fmt.Sprintf(`{"request_ids":%s,"target_launchpad_id":%q}`, idList(e[0], e[1]), target), http.StatusConflict, `{"error":"already_linked"}`},
			{"a missing target", fmt.Sprintf(`{"request_ids":%s,"target_launchpad_id":%q}`, idList(e[0]), uuid.New()), http.StatusNotFound, `{"error":"not_found"}`},
			{"an anchor that is not an entry of the target", fmt.Sprintf(`{"request_ids":%s,"target_launchpad_id":%q,"before_request_id":%q}`, idList(e[0]), target, e[0]), http.StatusBadRequest, `{"error":"invalid_placement"}`},
		} {
			response := requestControlAPI(p.server, http.MethodPost, path, test.body)
			if response.Code != test.status || response.Body.String() != test.want+"\n" {
				t.Errorf("%s: got %d %s, want %d %s", test.name, response.Code, response.Body, test.status, test.want)
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
		p.assertEntryOrder(t, pad, e...)
		p.assertEntryOrder(t, target, e[1])
	})
}
