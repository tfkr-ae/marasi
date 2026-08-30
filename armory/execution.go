package armory

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
)

const MaxConcurrent = 100

// newExecution validates a run and creates its runtime state.
func newExecution(run *domain.ArmoryRun, tmpl *armoryTemplate) (*execution, error) {
	if err := validateRun(run, tmpl); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &execution{
		run:        run,
		template:   tmpl,
		requests:   make(chan string, run.MaxConcurrent),
		ctx:        ctx,
		cancelFunc: cancel,
	}, nil
}

func validateRun(run *domain.ArmoryRun, tmpl *armoryTemplate) error {
	if run == nil {
		return errors.New("armory run is required")
	}

	if tmpl == nil {
		return errors.New("armory template is required")
	}

	if run.Status != domain.ArmoryRunDraft {
		return fmt.Errorf("armory run %s is not a draft", run.ID)
	}

	if run.MaxConcurrent < 1 {
		return errors.New("max concurrent must be positive")
	}
	if run.MaxConcurrent > MaxConcurrent {
		return fmt.Errorf("max concurrent cannot exceed %d", MaxConcurrent)
	}

	if tmpl.positionCount < 1 {
		return errors.New("armory template requires at least one payload position")
	}

	switch run.AttackType {
	case domain.ArmoryAttackHarpoon, domain.ArmoryAttackBroadside:
		if len(run.Wordlists) != 1 {
			return fmt.Errorf("%s requires exactly one wordlist", run.AttackType)
		}
	case domain.ArmoryAttackTandem, domain.ArmoryAttackMaelstrom:
		if len(run.Wordlists) != tmpl.positionCount {
			return fmt.Errorf("%s requires one wordlist per template position", run.AttackType)
		}
	default:
		return fmt.Errorf("unsupported armory attack type %q", run.AttackType)
	}

	raw, err := tmpl.render(nil)
	if err != nil {
		return err
	}
	updated, err := rawhttp.RecalculateContentLength([]byte(raw))
	if err != nil {
		return fmt.Errorf("invalid HTTP request: %w", err)
	}
	request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(updated)))
	if err != nil {
		return fmt.Errorf("invalid HTTP request: %w", err)
	}
	request.Body.Close()
	if request.Host == "" {
		return errors.New("host header not found or is empty")
	}
	return nil
}

// ValidateRun validates a draft without persisting or starting it.
func (manager *Manager) ValidateRun(run *domain.ArmoryRun) error {
	if run == nil {
		return errors.New("armory run is required")
	}
	tmpl, err := parseTemplate(run.TemplateSnapshot)
	if err != nil {
		return err
	}
	return validateRun(run, tmpl)
}

// StartRun starts a persisted draft Armory run.
func (manager *Manager) StartRun(runID uuid.UUID) error {
	run, err := manager.repository.GetArmoryRun(runID)
	if err != nil {
		return err
	}

	tmpl, err := parseTemplate(run.TemplateSnapshot)
	if err != nil {
		return err
	}

	execution, err := newExecution(run, tmpl)
	if err != nil {
		return err
	}

	manager.mu.Lock()
	if _, exists := manager.activeRuns[runID]; exists {
		manager.mu.Unlock()
		execution.cancelFunc()
		return fmt.Errorf("armory run %s is already active", runID)
	}
	manager.activeRuns[runID] = execution
	manager.mu.Unlock()

	startedAt := time.Now()
	run.Status = domain.ArmoryRunInProgress
	run.StartedAt = &startedAt
	if err := manager.repository.UpdateArmoryRun(run); err != nil {
		manager.removeExecution(execution)
		execution.cancelFunc()
		return err
	}

	go manager.execute(execution)
	return nil
}

// execute produces and sends all requests for an Armory run.
func (manager *Manager) execute(execution *execution) {
	for worker := 0; worker < execution.run.MaxConcurrent; worker++ {
		execution.wg.Add(1)
		go manager.executeWorker(execution)
	}

	err := manager.produce(execution)
	if err != nil {
		execution.cancelFunc()
	}

	close(execution.requests)
	execution.wg.Wait()

	finishedAt := time.Now()
	execution.run.FinishedAt = &finishedAt
	switch {
	case errors.Is(err, context.Canceled):
		execution.run.Status = domain.ArmoryRunCancelled
	case err != nil:
		execution.run.Status = domain.ArmoryRunFailed
	case execution.ctx.Err() != nil:
		execution.run.Status = domain.ArmoryRunCancelled
	default:
		execution.run.Status = domain.ArmoryRunCompleted
	}

	manager.repository.UpdateArmoryRun(execution.run)
	manager.removeExecution(execution)
	execution.cancelFunc()
}

// produce selects the configured attack producer for an execution.
func (manager *Manager) produce(execution *execution) error {
	switch execution.run.AttackType {
	case domain.ArmoryAttackHarpoon:
		return manager.produceHarpoon(execution, execution.template, execution.run.Wordlists[0])
	case domain.ArmoryAttackBroadside:
		return manager.produceBroadside(execution, execution.template, execution.run.Wordlists[0])
	case domain.ArmoryAttackTandem:
		return manager.produceTandem(execution, execution.template, execution.run.Wordlists)
	case domain.ArmoryAttackMaelstrom:
		return manager.produceMaelstrom(execution, execution.template, execution.run.Wordlists)
	default:
		return fmt.Errorf("unsupported armory attack type %q", execution.run.AttackType)
	}
}

// executeWorker sends rendered requests until the queue closes or the execution stops.
func (manager *Manager) executeWorker(execution *execution) {
	defer execution.wg.Done()

	for {
		select {
		case raw, exists := <-execution.requests:
			if !exists {
				return
			}
			if err := manager.sendFunc(execution.ctx, raw, execution.run.ID, execution.run.UseHTTPS); err != nil {
				if execution.ctx.Err() != nil {
					return
				}
				log.Printf("sending armory request for run %s: %v", execution.run.ID, err)
			}
		case <-execution.ctx.Done():
			return
		}
	}
}

// removeExecution removes an execution if it is still the active instance for its run.
func (manager *Manager) removeExecution(execution *execution) {
	manager.mu.Lock()
	if manager.activeRuns[execution.run.ID] == execution {
		delete(manager.activeRuns, execution.run.ID)
	}
	manager.mu.Unlock()
}
