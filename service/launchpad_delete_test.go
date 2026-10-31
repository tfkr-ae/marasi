package service

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestLaunchpadDeleteAPI(t *testing.T) {
	t.Run("should delete the launchpad, keep its pairs, and publish one launchpad.deleted event", func(t *testing.T) {
		p, pad, e := moveProject(t, 2)
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		response := requestControlAPI(p.server, http.MethodDelete, "/launchpad/"+pad.String(), "")

		want := fmt.Sprintf(`{"id":%q}`, pad)
		assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
		events := drainEvents(subscriber)
		if len(events) != 1 || events[0].name != "launchpad.deleted" || string(events[0].data) != want {
			t.Fatalf("\nwanted:\nlaunchpad.deleted %s\ngot:\n%v", want, eventSummary(events))
		}
		if _, err := p.repo.GetLaunchpad(pad); !errors.Is(err, domain.ErrLaunchpadNotFound) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrLaunchpadNotFound, err)
		}
		for _, id := range e {
			if response := requestControlAPI(p.server, http.MethodGet, "/traffic/"+id.String(), ""); response.Code != http.StatusOK {
				t.Fatalf("\nwanted:\nGET /traffic/%s 200\ngot:\n%d %s", id, response.Code, response.Body)
			}
		}
	})

	t.Run("should refuse an unknown or bad launchpad id and publish nothing", func(t *testing.T) {
		p, pad, _ := moveProject(t, 1)
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		for _, test := range []struct {
			name, path string
			status     int
			want       string
		}{
			{"an unknown launchpad", "/launchpad/" + uuid.NewString(), http.StatusNotFound, `{"error":"not_found"}`},
			{"a bad launchpad id", "/launchpad/junk", http.StatusBadRequest, `{"error":"bad_request"}`},
		} {
			response := requestControlAPI(p.server, http.MethodDelete, test.path, "")
			if response.Code != test.status || response.Body.String() != test.want+"\n" {
				t.Errorf("%s:\nwanted:\n%d %s\ngot:\n%d %s", test.name, test.status, test.want, response.Code, response.Body)
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
		if _, err := p.repo.GetLaunchpad(pad); err != nil {
			t.Fatalf("\nwanted:\nthe launchpad kept\ngot:\n%v", err)
		}
	})
}
