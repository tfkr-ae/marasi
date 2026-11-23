// Package armory manages reusable request templates and their concurrent runs.
package armory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
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
	// runUpdated receives successful lifecycle writes.
	runUpdated func(*domain.ArmoryRun)
	// failedAttemptRecorded receives each failed attempt once it is stored.
	failedAttemptRecorded func(*domain.ArmoryFailedAttempt)

	// mu protects activeRuns.
	mu sync.Mutex
	// activeRuns contains executing runs and finished runs awaiting final persistence.
	activeRuns map[uuid.UUID]*execution
	closing    bool
	executions sync.WaitGroup
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

// Shutdown prevents new runs, cancels workers and producers, and waits for
// their final statuses to be persisted before project resources can close.
func (manager *Manager) Shutdown() {
	manager.mu.Lock()
	manager.closing = true
	for _, execution := range manager.activeRuns {
		execution.cancelFunc()
	}
	manager.mu.Unlock()
	manager.executions.Wait()
}

// Repo returns the repository used by the Armory manager.
func (manager *Manager) Repo() domain.ArmoryRepository {
	return manager.repository
}

// SetRunUpdated sets the callback for successful run lifecycle writes.
func (manager *Manager) SetRunUpdated(callback func(*domain.ArmoryRun)) {
	manager.mu.Lock()
	manager.runUpdated = callback
	manager.mu.Unlock()
}

// SetFailedAttemptRecorded sets the callback for each failed attempt a run
// stores.
func (manager *Manager) SetFailedAttemptRecorded(callback func(*domain.ArmoryFailedAttempt)) {
	manager.mu.Lock()
	manager.failedAttemptRecorded = callback
	manager.mu.Unlock()
}

// recordFailedAttempt stores a request the run built from payloads but
// will not send, because cause says its messages do not encode.
func (manager *Manager) recordFailedAttempt(execution *execution, payloads map[int]string, cause error) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("creating failed attempt id: %w", err)
	}
	attempt := &domain.ArmoryFailedAttempt{
		ID:        id,
		RunID:     execution.run.ID,
		Payloads:  make([]domain.ArmoryPayload, 0, len(payloads)),
		Error:     cause.Error(),
		CreatedAt: time.Now(),
	}
	for _, position := range slices.Sorted(maps.Keys(payloads)) {
		attempt.Payloads = append(attempt.Payloads, domain.ArmoryPayload{Position: position, Value: payloads[position]})
	}
	if err := manager.repository.CreateArmoryFailedAttempt(attempt); err != nil {
		return fmt.Errorf("recording a failed attempt for armory run %s: %w", execution.run.ID, err)
	}
	manager.mu.Lock()
	callback := manager.failedAttemptRecorded
	manager.mu.Unlock()
	if callback != nil {
		callback(attempt)
	}
	return nil
}

func (manager *Manager) updateRun(run *domain.ArmoryRun) error {
	if err := manager.repository.UpdateArmoryRun(run); err != nil {
		return err
	}
	manager.mu.Lock()
	callback := manager.runUpdated
	manager.mu.Unlock()
	if callback != nil {
		manager.readProgress(run)
		callback(run)
	}
	return nil
}

// readProgress reads the run's progress from the repository before a
// lifecycle write is announced, so the announcement counts the entries and
// failed attempts stored so far. A failed read keeps the progress it had.
func (manager *Manager) readProgress(run *domain.ArmoryRun) {
	stored, err := manager.repository.GetArmoryRun(run.ID)
	if err != nil || stored == nil {
		log.Printf("reading armory run %s progress: %v", run.ID, err)
		return
	}
	run.Progress = stored.Progress
}

// ValidateTemplate checks a template before it is saved: its framing is one
// Marasi sends, and for a template with a message-form body, the content
// type names that framing, the body starts with a separator, and every
// message encodes with the fallback payloads. A template with no framing
// is checked only as it always was, when a run is created.
func (manager *Manager) ValidateTemplate(template *domain.ArmoryTemplate) error {
	if template == nil {
		return errors.New("armory template is required")
	}
	tmpl, err := parseFramedTemplate(template.RawTemplate, template.Framing)
	if err != nil {
		return err
	}
	if tmpl.framing == stream.FramingNone {
		return nil
	}
	_, err = tmpl.render(nil)
	return err
}
