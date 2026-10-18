package marasi

import (
	"slices"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// MoveLaunchpadEntries moves entries to a new place, as
// domain.LaunchpadRepository's MoveLaunchpadEntries does, and returns the
// moved pair ids in their new entry order. Clients call this method rather
// than the repository method. A pending launch's pair can be reordered
// within its launchpad, but not moved to another one (ADR-0049): then it
// moves nothing and returns a *domain.LaunchPendingError naming the pending
// launches' pairs. It holds launchMu across the check and the move, as the
// guards in launchpad_pending.go do.
func (proxy *Proxy) MoveLaunchpadEntries(move domain.LaunchpadMove) ([]uuid.UUID, error) {
	repo, err := proxy.GetLaunchpadRepo()
	if err != nil {
		return nil, err
	}
	if move.TargetID == move.SourceID {
		return repo.MoveLaunchpadEntries(move)
	}
	proxy.launchMu.Lock()
	defer proxy.launchMu.Unlock()
	pending := slices.DeleteFunc(proxy.pendingLaunchesLocked(launchedFrom(move.SourceID)), func(id uuid.UUID) bool {
		return !slices.Contains(move.RequestIDs, id)
	})
	if len(pending) > 0 {
		return nil, &domain.LaunchPendingError{IDs: pending}
	}
	return repo.MoveLaunchpadEntries(move)
}
