// Package armory manages reusable request templates and their concurrent runs.
package armory

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/wordlist"
)

// SendRequestFunc sends a rendered request for an Armory run.
type SendRequestFunc func(ctx context.Context, raw string, runID uuid.UUID, useHTTPS bool) error

// Manager holds Armory dependencies and tracks active runs.
type Manager struct {
	// repository persists Armory templates, runs, and request links.
	repository domain.ArmoryRepository
	// wordlists provides the payload wordlists used by runs.
	wordlists wordlist.Provider
	// sendFunc sends rendered requests through the configured transport.
	sendFunc SendRequestFunc

	// mu protects activeRuns.
	mu sync.Mutex
	// activeRuns contains the runs currently being executed.
	activeRuns map[uuid.UUID]*execution
}

// execution contains the runtime state of an active Armory run.
type execution struct {
	// run contains the persisted configuration and lifecycle state.
	run *domain.ArmoryRun
	// template is the compiled request template used by the run.
	template *armoryTemplate
	// requests queues rendered requests for workers.
	requests chan string
	// wg tracks the execution's active workers.
	wg sync.WaitGroup

	// ctx controls the lifetime of the execution.
	ctx context.Context
	// cancelFunc cancels the execution.
	cancelFunc context.CancelFunc
}

// NewManager creates a Manager with the required dependencies.
func NewManager(repo domain.ArmoryRepository, wordlists wordlist.Provider, sendFunc SendRequestFunc) (*Manager, error) {
	if repo == nil {
		return nil, errors.New("armory repository is required")
	}
	if wordlists == nil {
		return nil, errors.New("wordlist provider is required")
	}
	if sendFunc == nil {
		return nil, errors.New("request sender is required")
	}

	return &Manager{
		repository: repo,
		wordlists:  wordlists,
		sendFunc:   sendFunc,
		activeRuns: make(map[uuid.UUID]*execution),
	}, nil

}

// ActiveRunIDs returns the IDs of all currently active runs.
func (manager *Manager) ActiveRunIDs() []uuid.UUID {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	ids := make([]uuid.UUID, 0, len(manager.activeRuns))
	for id := range manager.activeRuns {
		ids = append(ids, id)
	}

	return ids
}

// CancelRun cancels an active run by ID.
func (manager *Manager) CancelRun(runID uuid.UUID) error {
	manager.mu.Lock()
	execution, exists := manager.activeRuns[runID]
	manager.mu.Unlock()

	if !exists {
		return fmt.Errorf("armory run %s is not active", runID)
	}

	execution.cancelFunc()
	return nil
}

// Repo returns the repository used by the Armory manager.
func (manager *Manager) Repo() domain.ArmoryRepository {
	return manager.repository
}
