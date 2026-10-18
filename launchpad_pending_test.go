package marasi

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// pendingLaunch starts a launch to origin's /slow path and waits until its
// pair is stored, so it is pending with its pair in the harness launchpad.
func (h *launchHarness) pendingLaunch(t *testing.T, origin *launchOrigin) uuid.UUID {
	t.Helper()
	id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
	if err != nil {
		t.Fatalf("Launch() returned unexpected error: %v", err)
	}
	origin.waitPath(t, "/slow")
	h.waitRequest(t, "/slow")
	if err := h.proxy.flushDBWrites(); err != nil {
		t.Fatalf("flushing database writes: %v", err)
	}
	return id
}

// finishLaunch lets the pending /slow launch complete and waits for it.
func (h *launchHarness) finishLaunch(t *testing.T, origin *launchOrigin) {
	t.Helper()
	origin.unblock()
	if got := h.waitFinished(t); got.Status != domain.LaunchCompleted {
		t.Fatalf("\nwanted:\na completed launch\ngot:\n%+v", got)
	}
}

// storedPair inserts an ordinary pair, not a launch, and returns its id.
func (h *launchHarness) storedPair(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	err := h.repository.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: "http", Method: "GET", Host: "pending.example.com", Path: "/ordinary",
		Raw: []byte("GET /ordinary HTTP/1.1\r\nHost: pending.example.com\r\n\r\n"), Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("inserting pair: %v", err)
	}
	return id
}

func assertLaunchPending(t *testing.T, err error, ids ...uuid.UUID) {
	t.Helper()
	var pending *domain.LaunchPendingError
	if !errors.Is(err, domain.ErrLaunchPending) || !errors.As(err, &pending) || !reflect.DeepEqual(pending.IDs, ids) {
		t.Fatalf("\nwanted:\n%v naming %v\ngot:\n%v", domain.ErrLaunchPending, ids, err)
	}
}

func TestProxy_DeleteTrafficRefusesPendingLaunch(t *testing.T) {
	t.Run("should refuse a listed id selection holding a pending launch's pair and delete nothing", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		other := h.storedPair(t)
		id := h.pendingLaunch(t, origin)

		count, err := h.proxy.DeleteTraffic(domain.Selection{IDs: []uuid.UUID{other, id}})

		assertLaunchPending(t, err, id)
		if count != 0 {
			t.Fatalf("\nwanted:\ncount 0\ngot:\n%d", count)
		}
		for _, kept := range []uuid.UUID{other, id} {
			if _, err := h.repository.GetRequestResponseRow(kept); err != nil {
				t.Fatalf("\nwanted:\npair %s kept\ngot:\n%v", kept, err)
			}
		}
	})

	t.Run("should refuse a query selection matching a pending launch's pair", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id := h.pendingLaunch(t, origin)

		count, err := h.proxy.DeleteTraffic(domain.Selection{Query: &domain.SelectionQuery{Text: `path = "/slow"`, AsOf: id}})

		assertLaunchPending(t, err, id)
		if _, err := h.repository.GetRequestResponseRow(id); err != nil || count != 0 {
			t.Fatalf("\nwanted:\ncount 0 and the pair kept\ngot:\ncount %d, %v", count, err)
		}
	})

	t.Run("should delete a selection that leaves out the pending launch's pair", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id := h.pendingLaunch(t, origin)
		other := h.storedPair(t)

		for _, sel := range []domain.Selection{
			{Query: &domain.SelectionQuery{Text: `path = "/ordinary"`, AsOf: other}},
			{IDs: []uuid.UUID{h.storedPair(t)}},
		} {
			if count, err := h.proxy.DeleteTraffic(sel); err != nil || count != 1 {
				t.Fatalf("\nwanted:\n1 pair deleted\ngot:\n%d, %v", count, err)
			}
		}
		if _, err := h.repository.GetRequestResponseRow(id); err != nil {
			t.Fatalf("\nwanted:\nthe launch's pair kept\ngot:\n%v", err)
		}
	})

	t.Run("should delete the pair once its launch has finished", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id := h.pendingLaunch(t, origin)
		h.finishLaunch(t, origin)

		if count, err := h.proxy.DeleteTraffic(domain.Selection{IDs: []uuid.UUID{id}}); err != nil || count != 1 {
			t.Fatalf("\nwanted:\n1 pair deleted\ngot:\n%d, %v", count, err)
		}
	})
}

func TestProxy_UnlinkRefusesPendingLaunch(t *testing.T) {
	t.Run("should refuse to unlink a pending launch's pair from its launchpad", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id := h.pendingLaunch(t, origin)

		assertLaunchPending(t, h.proxy.UnlinkRequestFromLaunchpad(id, h.launchpadID), id)
		for _, sel := range []domain.Selection{
			{IDs: []uuid.UUID{id}},
			{Query: &domain.SelectionQuery{AsOf: id}},
		} {
			count, err := h.proxy.UnlinkTraffic(sel, domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: h.launchpadID})
			assertLaunchPending(t, err, id)
			if count != 0 {
				t.Fatalf("\nwanted:\ncount 0\ngot:\n%d", count)
			}
		}
		if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{id}) {
			t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", id, got)
		}
	})

	t.Run("should unlink the pending launch's pair from another launchpad", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id := h.pendingLaunch(t, origin)
		other, err := h.repository.CreateLaunchpad("other", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		target := domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: other}
		if _, err := h.repository.LinkTraffic(domain.Selection{IDs: []uuid.UUID{id}}, target); err != nil {
			t.Fatalf("linking pair: %v", err)
		}

		if count, err := h.proxy.UnlinkTraffic(domain.Selection{IDs: []uuid.UUID{id}}, target); err != nil || count != 1 {
			t.Fatalf("\nwanted:\n1 pair unlinked\ngot:\n%d, %v", count, err)
		}
	})

	t.Run("should unlink the pair once its launch has finished", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id := h.pendingLaunch(t, origin)
		h.finishLaunch(t, origin)

		if err := h.proxy.UnlinkRequestFromLaunchpad(id, h.launchpadID); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got := h.launchpadMembers(t); len(got) != 0 {
			t.Fatalf("\nwanted:\nno launchpad members\ngot:\n%v", got)
		}
	})
}

func TestProxy_DeleteLaunchpadRefusesPendingLaunch(t *testing.T) {
	t.Run("should refuse while a launch whose pair is not written yet is pending from it", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		h.blockingWorkshop(t, `function processRequest(request) while true do end end`)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/hooked"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		if _, err := h.repository.GetRequestResponseRow(id); err == nil {
			t.Fatal("wanted the launch's pair not written yet")
		}

		assertLaunchPending(t, h.proxy.DeleteLaunchpad(h.launchpadID), id)
		if _, err := h.repository.GetLaunchpad(h.launchpadID); err != nil {
			t.Fatalf("\nwanted:\nthe launchpad kept\ngot:\n%v", err)
		}

		if _, err := h.proxy.CancelLaunch(id); err != nil {
			t.Fatalf("CancelLaunch() returned unexpected error: %v", err)
		}
		if got := h.waitFinished(t); got.Status != domain.LaunchCancelled {
			t.Fatalf("\nwanted:\na cancelled launch\ngot:\n%+v", got)
		}
		if err := h.proxy.DeleteLaunchpad(h.launchpadID); err != nil {
			t.Fatalf("\nwanted:\nnil once the launch is cancelled\ngot:\n%v", err)
		}
	})

	t.Run("should delete another launchpad while a launch is pending", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		h.pendingLaunch(t, origin)
		other, err := h.repository.CreateLaunchpad("other", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		if err := h.proxy.DeleteLaunchpad(other); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}
