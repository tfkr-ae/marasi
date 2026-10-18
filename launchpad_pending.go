package marasi

import (
	"bytes"
	"slices"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// A pending launch's pair stays in its launchpad (ADR-0053). Only the launch
// registry knows that a launch is pending, so the operations that would take
// the pair out of its launchpad are guarded here rather than in the
// repositories. Each guarded method holds launchMu across the check and the
// repository call: no launch can start, and no launch's pair can be written
// (see writeRequest), between the two. Clients call these methods, not the
// repository methods underneath, which stay unguarded.
//
// A launch counts as pending until it leaves the registry, which is after
// its pair and its outcome are stored.

// DeleteTraffic deletes every pair in sel, as domain.TrafficRepository's
// DeleteTraffic does, unless sel holds a pending launch's pair. Then it
// deletes nothing and returns a *domain.LaunchPendingError naming those
// pairs. A pair in flight that is not a launch can still be deleted.
func (proxy *Proxy) DeleteTraffic(sel domain.Selection) (int, error) {
	repo, err := proxy.GetTrafficRepo()
	if err != nil {
		return 0, err
	}
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	if err := proxy.refusePendingInLocked(repo, sel, anyLaunch); err != nil {
		return 0, err
	}
	return repo.DeleteTraffic(sel)
}

// UnlinkTraffic removes the links between every pair in sel and target, as
// domain.TrafficRepository's UnlinkTraffic does. When target is a launchpad
// and sel holds the pair of a launch pending from that launchpad, it changes
// nothing and returns a *domain.LaunchPendingError naming those pairs.
func (proxy *Proxy) UnlinkTraffic(sel domain.Selection, target domain.LinkTarget) (int, error) {
	repo, err := proxy.GetTrafficRepo()
	if err != nil {
		return 0, err
	}
	if target.Kind != domain.LinkLaunchpad {
		return repo.UnlinkTraffic(sel, target)
	}
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	if err := proxy.refusePendingInLocked(repo, sel, launchedFrom(target.ID)); err != nil {
		return 0, err
	}
	return repo.UnlinkTraffic(sel, target)
}

// UnlinkRequestFromLaunchpad removes a pair from a launchpad, as
// domain.LaunchpadRepository's UnlinkRequestFromLaunchpad does, unless the
// pair is a launch pending from that launchpad. Then it returns a
// *domain.LaunchPendingError naming the pair.
func (proxy *Proxy) UnlinkRequestFromLaunchpad(requestID, launchpadID uuid.UUID) error {
	repo, err := proxy.GetLaunchpadRepo()
	if err != nil {
		return err
	}
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	if pending := proxy.pendingLaunchesLocked(launchedFrom(launchpadID)); slices.Contains(pending, requestID) {
		return &domain.LaunchPendingError{IDs: []uuid.UUID{requestID}}
	}
	return repo.UnlinkRequestFromLaunchpad(requestID, launchpadID)
}

// DeleteLaunchpad deletes a launchpad, as domain.LaunchpadRepository's
// DeleteLaunchpad does, unless a launch from it is pending, whether or not
// its pair is written yet. Then it returns a *domain.LaunchPendingError
// naming those launches.
func (proxy *Proxy) DeleteLaunchpad(launchpadID uuid.UUID) error {
	repo, err := proxy.GetLaunchpadRepo()
	if err != nil {
		return err
	}
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	if pending := proxy.pendingLaunchesLocked(launchedFrom(launchpadID)); len(pending) > 0 {
		return &domain.LaunchPendingError{IDs: pending}
	}
	return repo.DeleteLaunchpad(launchpadID)
}

// anyLaunch matches every launch.
func anyLaunch(domain.Launch) bool { return true }

// launchedFrom matches the launches sent from launchpadID.
func launchedFrom(launchpadID uuid.UUID) func(domain.Launch) bool {
	return func(launch domain.Launch) bool { return launch.LaunchpadID == launchpadID }
}

// pendingLaunchesLocked returns the ids of the launches in the registry that
// match, oldest first. It must be called with launchMu held.
func (proxy *Proxy) pendingLaunchesLocked(match func(domain.Launch) bool) []uuid.UUID {
	var ids []uuid.UUID
	for id, entry := range proxy.launches {
		if match(entry.launch) {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return ids
}

// refusePendingInLocked returns a *domain.LaunchPendingError naming the
// pairs in sel of the pending launches that match, or nil when there are
// none. A listed id is checked against the registry, so a launch whose pair
// is not written yet is found too; a query selection is resolved by repo.
// It must be called with launchMu held.
func (proxy *Proxy) refusePendingInLocked(repo domain.TrafficRepository, sel domain.Selection, match func(domain.Launch) bool) error {
	pending := proxy.pendingLaunchesLocked(match)
	if len(pending) == 0 {
		return nil
	}
	var selected []uuid.UUID
	if sel.Query == nil {
		selected = slices.DeleteFunc(pending, func(id uuid.UUID) bool { return !slices.Contains(sel.IDs, id) })
	} else {
		var err error
		if selected, err = repo.SelectionIncludes(sel, pending); err != nil {
			return err
		}
	}
	if len(selected) > 0 {
		return &domain.LaunchPendingError{IDs: selected}
	}
	return nil
}
