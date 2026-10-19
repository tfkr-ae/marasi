package marasi

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestProxy_MoveLaunchpadEntries(t *testing.T) {
	t.Run("should reorder a pending launch's pair within its launchpad", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		first := uuid.Must(uuid.NewV7())
		err := h.repository.InsertRequest(&domain.ProxyRequest{
			ID: first, Scheme: "http", Method: "GET", Host: "move.example.com", Path: "/first",
			Raw: []byte("GET /first HTTP/1.1\r\nHost: move.example.com\r\n\r\n"), Metadata: map[string]any{},
		})
		if err != nil {
			t.Fatalf("inserting pair: %v", err)
		}
		if err := h.repository.LinkRequestToLaunchpad(first, h.launchpadID); err != nil {
			t.Fatalf("linking pair: %v", err)
		}
		id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		origin.waitPath(t, "/slow")
		h.waitRequest(t, "/slow")
		if err := h.proxy.flushDBWrites(); err != nil {
			t.Fatalf("flushing database writes: %v", err)
		}
		if got := h.proxy.Launches(); len(got) != 1 || got[0].ID != id {
			t.Fatalf("\nwanted:\nlaunch %s pending\ngot:\n%v", id, got)
		}

		moved, err := h.proxy.MoveLaunchpadEntries(domain.LaunchpadMove{
			SourceID: h.launchpadID, TargetID: h.launchpadID, RequestIDs: []uuid.UUID{id}, BeforeID: first,
		})

		if err != nil {
			t.Fatalf("MoveLaunchpadEntries() returned unexpected error: %v", err)
		}
		if want := []uuid.UUID{id}; !slices.Equal(moved, want) {
			t.Fatalf("\nwanted:\nmoved %v\ngot:\n%v", want, moved)
		}
		if got, want := h.launchpadMembers(t), []uuid.UUID{id, first}; !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\nentries %v\ngot:\n%v", want, got)
		}
		origin.unblock()
		h.waitFinished(t)
	})

	t.Run("should refuse to move a pending launch's pair to another launchpad until the launch finishes", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		other := h.storedPair(t)
		if err := h.repository.LinkRequestToLaunchpad(other, h.launchpadID); err != nil {
			t.Fatalf("linking pair: %v", err)
		}
		target, err := h.repository.CreateLaunchpad("Target", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		id := h.pendingLaunch(t, origin)
		move := domain.LaunchpadMove{SourceID: h.launchpadID, TargetID: target, RequestIDs: []uuid.UUID{other, id}}

		_, err = h.proxy.MoveLaunchpadEntries(move)

		assertLaunchPending(t, err, id)
		if got, want := h.launchpadMembers(t), []uuid.UUID{other, id}; !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\nentries %v\ngot:\n%v", want, got)
		}

		h.finishLaunch(t, origin)
		moved, err := h.proxy.MoveLaunchpadEntries(move)
		if err != nil {
			t.Fatalf("MoveLaunchpadEntries() after the launch finished returned unexpected error: %v", err)
		}
		if want := []uuid.UUID{other, id}; !slices.Equal(moved, want) {
			t.Fatalf("\nwanted:\nmoved %v\ngot:\n%v", want, moved)
		}
		if got := h.launchpadMembers(t); len(got) != 0 {
			t.Fatalf("\nwanted:\nno entries left in the source\ngot:\n%v", got)
		}
	})
}
