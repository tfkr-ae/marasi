package armory

import (
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

type testExecutionRepository struct {
	domain.ArmoryRepository
	run       *domain.ArmoryRun
	getErr    error
	updateErr error
	updates   chan domain.ArmoryRun
	mu        sync.Mutex
}

type testSentRequest struct {
	raw      string
	runID    uuid.UUID
	useHTTPS bool
}

func (repository *testExecutionRepository) GetArmoryRun(uuid.UUID) (*domain.ArmoryRun, error) {
	if repository.getErr != nil {
		return nil, repository.getErr
	}
	return repository.run, nil
}

func (repository *testExecutionRepository) UpdateArmoryRun(run *domain.ArmoryRun) error {
	if repository.updateErr != nil {
		return repository.updateErr
	}

	repository.mu.Lock()
	updated := *run
	repository.mu.Unlock()
	if repository.updates != nil {
		repository.updates <- updated
	}
	return nil
}

func newTestArmoryRun(t *testing.T) *domain.ArmoryRun {
	t.Helper()

	runID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating run uuid: %v", err)
	}

	return &domain.ArmoryRun{
		ID:               runID,
		TemplateSnapshot: "GET /?value=@@fallback@@ HTTP/1.1\r\nHost: example.com\r\n\r\n",
		UseHTTPS:         true,
		Wordlists:        []string{"words.txt"},
		Status:           domain.ArmoryRunDraft,
		AttackType:       domain.ArmoryAttackHarpoon,
		MaxConcurrent:    1,
	}
}

func waitForRunUpdate(t *testing.T, updates <-chan domain.ArmoryRun) domain.ArmoryRun {
	t.Helper()

	select {
	case run := <-updates:
		return run
	case <-time.After(5 * time.Second):
		t.Fatal("\nwanted:\narmory run update\ngot:\ntimeout")
		return domain.ArmoryRun{}
	}
}

func TestNewExecution(t *testing.T) {
	t.Run("should create an execution with initialized runtime state", func(t *testing.T) {
		run := newTestArmoryRun(t)
		tmpl, err := parseTemplate(run.TemplateSnapshot)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		execution, err := newExecution(run, tmpl)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer execution.cancelFunc()

		if execution.run != run {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", run, execution.run)
		}
		if execution.template != tmpl {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", tmpl, execution.template)
		}
		if cap(execution.requests) != run.MaxConcurrent {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", run.MaxConcurrent, cap(execution.requests))
		}
		if execution.ctx == nil {
			t.Fatal("\nwanted:\nexecution context\ngot:\nnil")
		}
		if execution.cancelFunc == nil {
			t.Fatal("\nwanted:\nexecution cancel function\ngot:\nnil")
		}
	})

	t.Run("should return an error if the run is missing", func(t *testing.T) {
		_, err := newExecution(nil, &armoryTemplate{positionCount: 1})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "armory run is required") {
			t.Fatalf("\nwanted:\nerror containing 'armory run is required'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the template is missing", func(t *testing.T) {
		_, err := newExecution(newTestArmoryRun(t), nil)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "armory template is required") {
			t.Fatalf("\nwanted:\nerror containing 'armory template is required'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the run is not a draft", func(t *testing.T) {
		run := newTestArmoryRun(t)
		run.Status = domain.ArmoryRunCompleted

		_, err := newExecution(run, &armoryTemplate{positionCount: 1})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "is not a draft") {
			t.Fatalf("\nwanted:\nerror containing 'is not a draft'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if max concurrent is not positive", func(t *testing.T) {
		run := newTestArmoryRun(t)
		run.MaxConcurrent = 0

		_, err := newExecution(run, &armoryTemplate{positionCount: 1})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "max concurrent must be positive") {
			t.Fatalf("\nwanted:\nerror containing 'max concurrent must be positive'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if max concurrent exceeds the limit", func(t *testing.T) {
		run := newTestArmoryRun(t)
		run.MaxConcurrent = MaxConcurrent + 1

		_, err := newExecution(run, &armoryTemplate{positionCount: 1})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "max concurrent cannot exceed") {
			t.Fatalf("\nwanted:\nerror containing 'max concurrent cannot exceed'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the template has no payload positions", func(t *testing.T) {
		_, err := newExecution(newTestArmoryRun(t), &armoryTemplate{})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "requires at least one payload position") {
			t.Fatalf("\nwanted:\nerror containing 'requires at least one payload position'\ngot:\n%v", err)
		}
	})

	t.Run("should require one wordlist for harpoon and broadside", func(t *testing.T) {
		for _, attackType := range []domain.ArmoryAttackType{domain.ArmoryAttackHarpoon, domain.ArmoryAttackBroadside} {
			t.Run(string(attackType), func(t *testing.T) {
				run := newTestArmoryRun(t)
				run.AttackType = attackType
				run.Wordlists = nil

				_, err := newExecution(run, &armoryTemplate{positionCount: 2})
				if err == nil {
					t.Fatal("\nwanted:\nerror\ngot:\nnil")
				}
				if !strings.Contains(err.Error(), "requires exactly one wordlist") {
					t.Fatalf("\nwanted:\nerror containing 'requires exactly one wordlist'\ngot:\n%v", err)
				}
			})
		}
	})

	t.Run("should require one wordlist per position for tandem and maelstrom", func(t *testing.T) {
		for _, attackType := range []domain.ArmoryAttackType{domain.ArmoryAttackTandem, domain.ArmoryAttackMaelstrom} {
			t.Run(string(attackType), func(t *testing.T) {
				run := newTestArmoryRun(t)
				run.AttackType = attackType

				_, err := newExecution(run, &armoryTemplate{positionCount: 2})
				if err == nil {
					t.Fatal("\nwanted:\nerror\ngot:\nnil")
				}
				if !strings.Contains(err.Error(), "requires one wordlist per template position") {
					t.Fatalf("\nwanted:\nerror containing 'requires one wordlist per template position'\ngot:\n%v", err)
				}
			})
		}
	})

	t.Run("should return an error for an unsupported attack type", func(t *testing.T) {
		run := newTestArmoryRun(t)
		run.AttackType = "unsupported"

		_, err := newExecution(run, &armoryTemplate{positionCount: 1})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "unsupported armory attack type") {
			t.Fatalf("\nwanted:\nerror containing 'unsupported armory attack type'\ngot:\n%v", err)
		}
	})
}

func TestManager_ValidateRun(t *testing.T) {
	manager := &Manager{}

	t.Run("should accept a valid request template and run configuration", func(t *testing.T) {
		if err := manager.ValidateRun(newTestArmoryRun(t)); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	tests := []struct {
		name string
		edit func(*domain.ArmoryRun)
		want string
	}{
		{
			name: "invalid template syntax",
			edit: func(run *domain.ArmoryRun) { run.TemplateSnapshot = "{{if}}" },
			want: "parsing armory template",
		},
		{
			name: "wrong wordlist count",
			edit: func(run *domain.ArmoryRun) { run.Wordlists = nil },
			want: "requires exactly one wordlist",
		},
		{
			name: "invalid HTTP request",
			edit: func(run *domain.ArmoryRun) { run.TemplateSnapshot = "value=@@fallback@@" },
			want: "invalid HTTP request",
		},
		{
			name: "missing host",
			edit: func(run *domain.ArmoryRun) { run.TemplateSnapshot = "GET /@@path@@ HTTP/1.1\r\n\r\n" },
			want: "host header not found or is empty",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := newTestArmoryRun(t)
			test.edit(run)
			err := manager.ValidateRun(run)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%v", test.want, err)
			}
		})
	}
}

func TestManager_StartRun(t *testing.T) {
	t.Run("should execute and complete a persisted draft run", func(t *testing.T) {
		run := newTestArmoryRun(t)
		updates := make(chan domain.ArmoryRun, 2)
		repository := &testExecutionRepository{run: run, updates: updates}
		provider := &testAttackWordlistProvider{entries: []string{"one", "two"}}
		sent := make(chan testSentRequest, 2)
		manager := &Manager{
			repository: repository,
			wordlists:  provider,
			sendFunc: func(ctx context.Context, raw string, runID uuid.UUID, useHTTPS bool) error {
				sent <- testSentRequest{raw: raw, runID: runID, useHTTPS: useHTTPS}
				return nil
			},
			activeRuns: make(map[uuid.UUID]*execution),
		}

		err := manager.StartRun(run.ID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		started := waitForRunUpdate(t, updates)
		if started.Status != domain.ArmoryRunInProgress {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", domain.ArmoryRunInProgress, started.Status)
		}
		if started.StartedAt == nil {
			t.Fatal("\nwanted:\nstart time\ngot:\nnil")
		}

		completed := waitForRunUpdate(t, updates)
		if completed.Status != domain.ArmoryRunCompleted {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", domain.ArmoryRunCompleted, completed.Status)
		}
		if completed.FinishedAt == nil {
			t.Fatal("\nwanted:\nfinish time\ngot:\nnil")
		}

		got := []testSentRequest{<-sent, <-sent}
		want := []testSentRequest{
			{raw: "GET /?value=one HTTP/1.1\r\nHost: example.com\r\n\r\n", runID: run.ID, useHTTPS: true},
			{raw: "GET /?value=two HTTP/1.1\r\nHost: example.com\r\n\r\n", runID: run.ID, useHTTPS: true},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
		if len(manager.ActiveRunIDs()) != 0 {
			t.Fatalf("\nwanted:\n0 active runs\ngot:\n%d", len(manager.ActiveRunIDs()))
		}
	})

	t.Run("should cancel an active run", func(t *testing.T) {
		run := newTestArmoryRun(t)
		updates := make(chan domain.ArmoryRun, 2)
		repository := &testExecutionRepository{run: run, updates: updates}
		provider := &testAttackWordlistProvider{entries: []string{"one", "two", "three"}}
		startedSending := make(chan struct{})
		manager := &Manager{
			repository: repository,
			wordlists:  provider,
			sendFunc: func(ctx context.Context, raw string, runID uuid.UUID, useHTTPS bool) error {
				close(startedSending)
				<-ctx.Done()
				return ctx.Err()
			},
			activeRuns: make(map[uuid.UUID]*execution),
		}

		err := manager.StartRun(run.ID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		waitForRunUpdate(t, updates)

		select {
		case <-startedSending:
		case <-time.After(5 * time.Second):
			t.Fatal("\nwanted:\nrequest sending to start\ngot:\ntimeout")
		}

		err = manager.CancelRun(run.ID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		cancelled := waitForRunUpdate(t, updates)
		if cancelled.Status != domain.ArmoryRunCancelled {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", domain.ArmoryRunCancelled, cancelled.Status)
		}
		if len(manager.ActiveRunIDs()) != 0 {
			t.Fatalf("\nwanted:\n0 active runs\ngot:\n%d", len(manager.ActiveRunIDs()))
		}
	})

	t.Run("should fail a run when its producer fails", func(t *testing.T) {
		run := newTestArmoryRun(t)
		updates := make(chan domain.ArmoryRun, 2)
		repository := &testExecutionRepository{run: run, updates: updates}
		provider := &testAttackWordlistProvider{readErr: errors.New("read failed")}
		manager := &Manager{
			repository: repository,
			wordlists:  provider,
			sendFunc: func(context.Context, string, uuid.UUID, bool) error {
				return nil
			},
			activeRuns: make(map[uuid.UUID]*execution),
		}

		err := manager.StartRun(run.ID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		waitForRunUpdate(t, updates)

		failed := waitForRunUpdate(t, updates)
		if failed.Status != domain.ArmoryRunFailed {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", domain.ArmoryRunFailed, failed.Status)
		}
		if len(manager.ActiveRunIDs()) != 0 {
			t.Fatalf("\nwanted:\n0 active runs\ngot:\n%d", len(manager.ActiveRunIDs()))
		}
	})

	t.Run("should continue after an individual request fails", func(t *testing.T) {
		run := newTestArmoryRun(t)
		updates := make(chan domain.ArmoryRun, 2)
		repository := &testExecutionRepository{run: run, updates: updates}
		provider := &testAttackWordlistProvider{entries: []string{"one", "two"}}
		sent := make(chan string, 2)
		var logs strings.Builder
		previousOutput := log.Writer()
		log.SetOutput(&logs)
		defer log.SetOutput(previousOutput)
		manager := &Manager{
			repository: repository,
			wordlists:  provider,
			sendFunc: func(ctx context.Context, raw string, runID uuid.UUID, useHTTPS bool) error {
				sent <- raw
				if strings.Contains(raw, "value=one") {
					return errors.New("send failed")
				}
				return nil
			},
			activeRuns: make(map[uuid.UUID]*execution),
		}

		err := manager.StartRun(run.ID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		waitForRunUpdate(t, updates)

		completed := waitForRunUpdate(t, updates)
		if completed.Status != domain.ArmoryRunCompleted {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", domain.ArmoryRunCompleted, completed.Status)
		}

		got := []string{<-sent, <-sent}
		want := []string{
			"GET /?value=one HTTP/1.1\r\nHost: example.com\r\n\r\n",
			"GET /?value=two HTTP/1.1\r\nHost: example.com\r\n\r\n",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
		if !strings.Contains(logs.String(), "sending armory request") {
			t.Fatalf("\nwanted:\nlog containing 'sending armory request'\ngot:\n%s", logs.String())
		}
	})

	t.Run("should remove the execution if the initial run update fails", func(t *testing.T) {
		run := newTestArmoryRun(t)
		repository := &testExecutionRepository{run: run, updateErr: errors.New("update failed")}
		manager := &Manager{
			repository: repository,
			wordlists:  &testAttackWordlistProvider{},
			sendFunc: func(context.Context, string, uuid.UUID, bool) error {
				return nil
			},
			activeRuns: make(map[uuid.UUID]*execution),
		}

		err := manager.StartRun(run.ID)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "update failed") {
			t.Fatalf("\nwanted:\nerror containing 'update failed'\ngot:\n%v", err)
		}
		if len(manager.ActiveRunIDs()) != 0 {
			t.Fatalf("\nwanted:\n0 active runs\ngot:\n%d", len(manager.ActiveRunIDs()))
		}
	})
}
