package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/wordlist"
)

func newTestProjectLifecycle(t *testing.T) (*ProjectLifecycle, *marasi.Proxy, string) {
	t.Helper()
	configDir := t.TempDir()
	manager, err := wordlist.NewManager(configDir)
	if err != nil {
		t.Fatalf("creating wordlist manager: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy, err := marasi.New(marasi.WithLogger(logger), marasi.WithConfigDir(configDir))
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	lifecycle := NewProjectLifecycle(proxy, configDir, manager, logger)
	t.Cleanup(func() { _ = lifecycle.Shutdown() })
	return lifecycle, proxy, configDir
}

func canonicalProjectPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := ResolveProjectPath(path)
	if err != nil {
		t.Fatalf("resolving project path: %v", err)
	}
	return canonical
}

func TestProjectLifecycle(t *testing.T) {
	t.Run("should publish all target resources and release the previous ownership", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		first := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		second := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		if err := lifecycle.Open(context.Background(), first); err != nil {
			t.Fatalf("opening first project: %v", err)
		}
		oldRepository := proxy.TrafficRepo
		if _, err := acquireProjectOwnership(first); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nowned first project\ngot:\n%v", err)
		}

		if err := lifecycle.Open(context.Background(), second); err != nil {
			t.Fatalf("opening second project: %v", err)
		}
		if lifecycle.Path() != second {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", second, lifecycle.Path())
		}
		if proxy.TrafficRepo == oldRepository || any(proxy.TrafficRepo) != any(proxy.ConfigRepo) || any(proxy.TrafficRepo) != any(proxy.WaypointRepo) || any(proxy.TrafficRepo) != any(proxy.ReportingRepo) {
			t.Fatal("\nwanted:\nall repositories replaced together\ngot:\nmixed project repositories")
		}
		if proxy.Scope == nil || proxy.Waypoints == nil || proxy.Armory == nil || proxy.ReportGenerator == nil {
			t.Fatal("\nwanted:\nprepared scope, waypoints, armory, and reports\ngot:\nmissing project dependency")
		}
		unlock, err := acquireProjectOwnership(first)
		if err != nil {
			t.Fatalf("acquiring released first project: %v", err)
		}
		_ = unlock()
		if _, err := acquireProjectOwnership(second); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nowned second project\ngot:\n%v", err)
		}
	})

	t.Run("should make the same canonical path a successful no-op", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		path := canonicalProjectPath(t, filepath.Join(dir, "project.marasi"))
		lockCalls := 0
		realLock := lifecycle.lock
		lifecycle.lock = func(path string) (func() error, error) {
			lockCalls++
			return realLock(path)
		}
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("opening project: %v", err)
		}
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("reopening project: %v", err)
		}
		if lockCalls != 1 {
			t.Fatalf("\nwanted:\n1 ownership acquisition\ngot:\n%d", lockCalls)
		}
	})

	t.Run("should retain the old project when finalization fails", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		lifecycle.flushOpenProject = func() error { return errors.New("flush failed") }
		if err := lifecycle.Open(context.Background(), target); err == nil || err.Error() != "flush failed" {
			t.Fatalf("\nwanted:\nflush failed\ngot:\n%v", err)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("\nwanted:\nprepared target preserved\ngot:\n%v", err)
		}
	})

	t.Run("should remove a newly created failed target and preserve an existing target", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		lifecycle.prepare = func(_ context.Context, path string) (marasi.ProjectResources, error) {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				_, err = file.WriteString("attempt")
				_ = file.Close()
			}
			if err != nil && !errors.Is(err, os.ErrExist) {
				t.Fatalf("creating attempted target: %v", err)
			}
			return marasi.ProjectResources{}, errors.New("prepare failed")
		}
		created := canonicalProjectPath(t, filepath.Join(dir, "created.marasi"))
		if err := lifecycle.Open(context.Background(), created); err == nil {
			t.Fatal("\nwanted:\npreparation failure\ngot:\nnil")
		}
		if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\ncreated target removed\ngot:\n%v", err)
		}
		existing := canonicalProjectPath(t, filepath.Join(dir, "existing.marasi"))
		if err := os.WriteFile(existing, []byte("original"), 0600); err != nil {
			t.Fatalf("creating existing target: %v", err)
		}
		if err := lifecycle.Open(context.Background(), existing); err == nil {
			t.Fatal("\nwanted:\npreparation failure\ngot:\nnil")
		}
		contents, err := os.ReadFile(existing)
		if err != nil || string(contents) != "original" {
			t.Fatalf("\nwanted:\nexisting target preserved\ngot:\n%q %v", contents, err)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
	})

	t.Run("should release ownership during shutdown", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		path := canonicalProjectPath(t, filepath.Join(dir, "project.marasi"))
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("opening project: %v", err)
		}
		if err := lifecycle.Shutdown(); err != nil {
			t.Fatalf("shutting down: %v", err)
		}
		if lifecycle.Path() != "" {
			t.Fatalf("\nwanted:\nno project after shutdown\ngot:\n%s", lifecycle.Path())
		}
		unlock, err := acquireProjectOwnership(path)
		if err != nil {
			t.Fatalf("acquiring project after shutdown: %v", err)
		}
		_ = unlock()
	})

	t.Run("should cancel before publication and retain the old project", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		release, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting work: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- lifecycle.Open(ctx, target) }()
		deadline := time.Now().Add(time.Second)
		for {
			if _, err := os.Stat(target); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("target was not prepared")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("\nwanted:\ncontext canceled\ngot:\n%v", err)
		}
		release()
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("\nwanted:\ncanceled prepared target preserved\ngot:\n%v", err)
		}
	})

	t.Run("should wait for admitted work and hold new work until publication", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		releaseOld, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting old work: %v", err)
		}
		result := make(chan error, 1)
		go func() { result <- lifecycle.Open(context.Background(), target) }()
		for {
			lifecycle.gate.mu.Lock()
			blocked := lifecycle.gate.blocked
			lifecycle.gate.mu.Unlock()
			if blocked {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\nold path before publication\ngot:\n%s", lifecycle.Path())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := lifecycle.Admit(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("\nwanted:\nnew work blocked\ngot:\n%v", err)
		}
		releaseOld()
		if err := <-result; err != nil {
			t.Fatalf("opening target: %v", err)
		}
		if lifecycle.Path() != target {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", target, lifecycle.Path())
		}
		releaseNew, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting new work: %v", err)
		}
		releaseNew()
	})

	t.Run("should serialize simultaneous handoffs", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		initial := canonicalProjectPath(t, filepath.Join(dir, "initial.marasi"))
		first := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		second := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		if err := lifecycle.Open(context.Background(), initial); err != nil {
			t.Fatalf("opening initial project: %v", err)
		}
		realPrepare := lifecycle.prepare
		entered := make(chan string, 2)
		continueFirst := make(chan struct{})
		lifecycle.prepare = func(ctx context.Context, path string) (marasi.ProjectResources, error) {
			entered <- path
			if path == first {
				<-continueFirst
			}
			return realPrepare(ctx, path)
		}
		firstResult := make(chan error, 1)
		secondResult := make(chan error, 1)
		go func() { firstResult <- lifecycle.Open(context.Background(), first) }()
		if got := <-entered; got != first {
			t.Fatalf("\nwanted:\n%s first\ngot:\n%s", first, got)
		}
		go func() { secondResult <- lifecycle.Open(context.Background(), second) }()
		select {
		case got := <-entered:
			t.Fatalf("\nwanted:\nsecond handoff waiting\ngot:\nprepared %s", got)
		case <-time.After(20 * time.Millisecond):
		}
		close(continueFirst)
		if err := <-firstResult; err != nil {
			t.Fatalf("opening first target: %v", err)
		}
		if got := <-entered; got != second {
			t.Fatalf("\nwanted:\n%s second\ngot:\n%s", second, got)
		}
		if err := <-secondResult; err != nil {
			t.Fatalf("opening second target: %v", err)
		}
		if lifecycle.Path() != second {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", second, lifecycle.Path())
		}
	})

	t.Run("should retain the published target when old cleanup fails", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		realUnlock := lifecycle.open.unlock
		lifecycle.open.unlock = func() error {
			return errors.Join(realUnlock(), errors.New("release failed"))
		}
		err := lifecycle.Open(context.Background(), target)
		if !errors.Is(err, ErrProjectCleanup) {
			t.Fatalf("\nwanted:\nproject cleanup failure\ngot:\n%v", err)
		}
		if lifecycle.Path() != target {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", target, lifecycle.Path())
		}
		if _, err := acquireProjectOwnership(target); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\npublished target still owned\ngot:\n%v", err)
		}
	})
}
