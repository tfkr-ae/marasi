package armory

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/wordlist"
)

type testArmoryRepository struct {
	domain.ArmoryRepository
}

type testWordlistProvider struct {
	wordlist.Provider
}

func TestNewManager(t *testing.T) {
	t.Run("should create a manager with the configured dependencies", func(t *testing.T) {
		repository := &testArmoryRepository{}
		provider := &testWordlistProvider{}
		sendFunc := SendRequestFunc(func(context.Context, string, uuid.UUID, bool) error {
			return nil
		})

		manager, err := NewManager(repository, provider, sendFunc)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if manager.repository != repository {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", repository, manager.repository)
		}
		if manager.Repo() != repository {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", repository, manager.Repo())
		}
		if manager.wordlists != provider {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", provider, manager.wordlists)
		}
		if manager.sendFunc == nil {
			t.Fatal("\nwanted:\nconfigured request sender\ngot:\nnil")
		}
		if manager.activeRuns == nil {
			t.Fatal("\nwanted:\ninitialized active run map\ngot:\nnil")
		}
	})

	t.Run("should return an error if the repository is missing", func(t *testing.T) {
		provider := &testWordlistProvider{}
		sendFunc := SendRequestFunc(func(context.Context, string, uuid.UUID, bool) error {
			return nil
		})

		_, err := NewManager(nil, provider, sendFunc)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "armory repository is required") {
			t.Fatalf("\nwanted:\nerror containing 'armory repository is required'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the wordlist provider is missing", func(t *testing.T) {
		repository := &testArmoryRepository{}
		sendFunc := SendRequestFunc(func(context.Context, string, uuid.UUID, bool) error {
			return nil
		})

		_, err := NewManager(repository, nil, sendFunc)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "wordlist provider is required") {
			t.Fatalf("\nwanted:\nerror containing 'wordlist provider is required'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the request sender is missing", func(t *testing.T) {
		repository := &testArmoryRepository{}
		provider := &testWordlistProvider{}

		_, err := NewManager(repository, provider, nil)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "request sender is required") {
			t.Fatalf("\nwanted:\nerror containing 'request sender is required'\ngot:\n%v", err)
		}
	})
}

func TestManager_ActiveRunIDs(t *testing.T) {
	t.Run("should return an empty slice when there are no active runs", func(t *testing.T) {
		manager := &Manager{activeRuns: make(map[uuid.UUID]*execution)}

		got := manager.ActiveRunIDs()
		if len(got) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(got))
		}
	})

	t.Run("should return all active run IDs", func(t *testing.T) {
		manager := &Manager{activeRuns: make(map[uuid.UUID]*execution)}
		firstID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating first uuid: %v", err)
		}
		secondID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating second uuid: %v", err)
		}
		manager.activeRuns[firstID] = &execution{}
		manager.activeRuns[secondID] = &execution{}

		got := manager.ActiveRunIDs()
		if len(got) != 2 {
			t.Fatalf("\nwanted:\n2\ngot:\n%d", len(got))
		}

		seen := make(map[uuid.UUID]bool, len(got))
		for _, id := range got {
			seen[id] = true
		}
		if !seen[firstID] || !seen[secondID] {
			t.Fatalf("\nwanted:\n%v and %v\ngot:\n%v", firstID, secondID, got)
		}
	})
}

func TestManager_CancelRun(t *testing.T) {
	t.Run("should cancel an active run", func(t *testing.T) {
		manager := &Manager{activeRuns: make(map[uuid.UUID]*execution)}
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		manager.activeRuns[runID] = &execution{ctx: ctx, cancelFunc: cancel}

		err = manager.CancelRun(runID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if ctx.Err() != context.Canceled {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, ctx.Err())
		}
	})

	t.Run("should return an error if the run is not active", func(t *testing.T) {
		manager := &Manager{activeRuns: make(map[uuid.UUID]*execution)}
		runID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		err = manager.CancelRun(runID)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "is not active") {
			t.Fatalf("\nwanted:\nerror containing 'is not active'\ngot:\n%v", err)
		}
	})
}
