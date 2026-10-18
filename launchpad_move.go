package marasi

import (
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// MoveLaunchpadEntries moves entries to a new place, as
// domain.LaunchpadRepository's MoveLaunchpadEntries does, and returns the
// moved pair ids in their new entry order. Clients call this method rather
// than the repository method. A pending launch's pair can be reordered
// within its launchpad (ADR-0049).
func (proxy *Proxy) MoveLaunchpadEntries(move domain.LaunchpadMove) ([]uuid.UUID, error) {
	repo, err := proxy.GetLaunchpadRepo()
	if err != nil {
		return nil, err
	}
	return repo.MoveLaunchpadEntries(move)
}
