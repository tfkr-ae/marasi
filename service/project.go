package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	"github.com/tfkr-ae/marasi/internal/filelock"
	"github.com/tfkr-ae/marasi/report"
	"github.com/tfkr-ae/marasi/wordlist"
)

var (
	// ErrProjectAlreadyOpen means another service instance owns the target.
	ErrProjectAlreadyOpen = errors.New("project already open")
	// ErrProjectBusy means project-bound work prevents a safe handoff.
	ErrProjectBusy = errors.New("project busy")
	// ErrProjectCleanup means the target was published but old cleanup failed.
	ErrProjectCleanup = errors.New("project cleanup failed")
)

type openProject struct {
	path      string
	resources marasi.ProjectResources
	unlock    func() error
}

// ProjectLifecycle owns the one open project of a service instance.
type ProjectLifecycle struct {
	mu               sync.Mutex
	statusMu         sync.RWMutex
	proxy            *marasi.Proxy
	configDir        string
	wordlists        wordlist.Provider
	logger           *slog.Logger
	open             *openProject
	gate             *projectGate
	prepare          func(context.Context, string) (marasi.ProjectResources, error)
	lock             func(string) (func() error, error)
	flushOpenProject func() error
	opened           func(string)
	armoryRunUpdated func(*domain.ArmoryRun)
}

// NewProjectLifecycle creates a lifecycle with no open project. Open must
// succeed before the service instance starts accepting work.
func NewProjectLifecycle(proxy *marasi.Proxy, configDir string, wordlists wordlist.Provider, logger *slog.Logger) *ProjectLifecycle {
	lifecycle := &ProjectLifecycle{
		proxy:     proxy,
		configDir: configDir,
		wordlists: wordlists,
		logger:    logger,
		lock:      acquireProjectOwnership,
		gate:      newProjectGate(),
	}
	lifecycle.prepare = lifecycle.prepareProject
	lifecycle.flushOpenProject = proxy.CloseWebSocketsAndFlush
	_ = proxy.WithOptions(marasi.WithWorkAdmission(lifecycle.Admit))
	return lifecycle
}

// Path returns the canonical path of the open project.
func (lifecycle *ProjectLifecycle) Path() string {
	lifecycle.statusMu.RLock()
	defer lifecycle.statusMu.RUnlock()
	if lifecycle.open == nil {
		return ""
	}
	return lifecycle.open.path
}

// Open prepares target before publishing it as the open project.
func (lifecycle *ProjectLifecycle) Open(ctx context.Context, target string) error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()

	path, err := ResolveProjectPath(target)
	if err != nil {
		return err
	}
	if lifecycle.Path() == path {
		return nil
	}

	existed := true
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		existed = false
	} else if err != nil {
		return fmt.Errorf("checking project %s: %w", path, err)
	}

	unlock, err := lifecycle.lock(path)
	if err != nil {
		return err
	}
	resources, err := lifecycle.prepare(ctx, path)
	if err != nil {
		removeCreated := !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
		return errors.Join(err, cleanupPreparedProject(path, existed || !removeCreated, resources, unlock))
	}
	targetProject := &openProject{path: path, resources: resources, unlock: unlock}

	old := lifecycle.openProject()
	if lifecycle.projectBusy(old) {
		return errors.Join(ErrProjectBusy, cleanupPreparedProject(path, true, resources, unlock))
	}

	unblock, err := lifecycle.gate.block(ctx)
	if err != nil {
		return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
	}
	defer unblock()
	if lifecycle.projectBusy(old) {
		return errors.Join(ErrProjectBusy, cleanupPreparedProject(path, true, resources, unlock))
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
	}
	if err := lifecycle.flushOpenProject(); err != nil {
		return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
	}

	lifecycle.proxy.SetProjectResources(resources)
	lifecycle.setOpen(targetProject)
	if old == nil {
		return nil
	}
	if lifecycle.opened != nil {
		lifecycle.opened(path)
	}
	if err := closeProject(old); err != nil {
		if lifecycle.logger != nil {
			lifecycle.logger.Error("Failed to clean up previous project", "path", old.path, "error", err)
		}
		return errors.Join(ErrProjectCleanup, err)
	}
	return nil
}

func (lifecycle *ProjectLifecycle) projectBusy(project *openProject) bool {
	if project != nil && project.resources.Armory != nil && len(project.resources.Armory.ActiveRunIDs()) != 0 {
		return true
	}
	return lifecycle.proxy.HasPendingCheckpoint()
}

// Admit admits project-bound work and returns its matching release operation.
func (lifecycle *ProjectLifecycle) Admit(ctx context.Context) (func(), error) {
	return lifecycle.gate.admit(ctx)
}

// Shutdown flushes, closes, and releases the open project.
func (lifecycle *ProjectLifecycle) Shutdown() error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	project := lifecycle.openProject()
	if project == nil {
		return nil
	}
	unblock, err := lifecycle.gate.block(context.Background())
	if err != nil {
		return err
	}
	defer unblock()
	flushErr := lifecycle.proxy.CloseWebSocketsAndFlush()
	cleanupErr := closeProject(project)
	if cleanupErr == nil {
		lifecycle.setOpen(nil)
	}
	return errors.Join(flushErr, cleanupErr)
}

func (lifecycle *ProjectLifecycle) openProject() *openProject {
	lifecycle.statusMu.RLock()
	defer lifecycle.statusMu.RUnlock()
	return lifecycle.open
}

func (lifecycle *ProjectLifecycle) setOpen(project *openProject) {
	lifecycle.statusMu.Lock()
	lifecycle.open = project
	lifecycle.statusMu.Unlock()
}

func (lifecycle *ProjectLifecycle) prepareProject(ctx context.Context, path string) (marasi.ProjectResources, error) {
	connection, err := db.New(path, lifecycle.logger)
	if err != nil {
		return marasi.ProjectResources{}, fmt.Errorf("opening project: %w", err)
	}
	repository := db.NewProxyRepo(connection)
	resources := marasi.ProjectResources{Repository: repository, Scope: compass.NewScope(true)}
	fail := func(err error) (marasi.ProjectResources, error) {
		return resources, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	waypoints, err := repository.GetWaypoints()
	if err != nil {
		return fail(fmt.Errorf("loading project waypoints: %w", err))
	}
	resources.Waypoints = make(map[string]string, len(waypoints))
	for _, waypoint := range waypoints {
		resources.Waypoints[waypoint.Hostname] = waypoint.Override
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	storedExtensions, err := repository.GetExtensions()
	if err != nil {
		return fail(fmt.Errorf("loading project extensions: %w", err))
	}
	extensionService := stagedExtensionService{
		configDir:  lifecycle.configDir,
		client:     lifecycle.proxy.Client,
		repository: repository,
		scope:      resources.Scope,
	}
	resources.Extensions = make([]*extensions.Runtime, 0, len(storedExtensions))
	for _, stored := range storedExtensions {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		runtime := &extensions.Runtime{Data: stored}
		if err := runtime.PrepareState(extensionService, nil); err != nil {
			return fail(fmt.Errorf("preparing project extension %s: %w", stored.Name, err))
		}
		resources.Extensions = append(resources.Extensions, runtime)
	}

	resources.ReportGenerator, err = report.NewGenerator(repository, report.WithConfigDir(lifecycle.configDir))
	if err != nil {
		return fail(fmt.Errorf("preparing report generator: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	armoryManager, err := armory.NewManager(repository, lifecycle.wordlists, lifecycle.proxy.SendArmoryRequest)
	if err != nil {
		return fail(fmt.Errorf("preparing armory manager: %w", err))
	}
	armoryManager.SetRunUpdated(lifecycle.armoryRunUpdated)
	resources.Armory = armoryManager
	return resources, nil
}

type stagedExtensionService struct {
	configDir  string
	client     *http.Client
	repository marasi.RepositoryProvider
	scope      *compass.Scope
}

func (service stagedExtensionService) GetConfigDir() (string, error)     { return service.configDir, nil }
func (service stagedExtensionService) GetScope() (*compass.Scope, error) { return service.scope, nil }
func (service stagedExtensionService) GetClient() (*http.Client, error)  { return service.client, nil }
func (service stagedExtensionService) GetExtensionRepo() (domain.ExtensionRepository, error) {
	return service.repository, nil
}
func (service stagedExtensionService) GetTrafficRepo() (domain.TrafficRepository, error) {
	return service.repository, nil
}
func (service stagedExtensionService) WriteLog(level, message string, options ...func(*domain.Log) error) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	entry := &domain.Log{ID: id, Level: level, Message: message, Timestamp: time.Now()}
	for _, option := range options {
		if err := option(entry); err != nil {
			return err
		}
	}
	return service.repository.InsertLog(entry)
}

func cleanupPreparedProject(path string, existed bool, resources marasi.ProjectResources, unlock func() error) error {
	var closeErr error
	if resources.Repository != nil {
		closeErr = resources.Repository.Close()
	}
	removeErr := error(nil)
	if !existed {
		removeErr = os.Remove(path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
	}
	return errors.Join(closeErr, unlock(), removeErr)
}

func closeProject(project *openProject) error {
	return errors.Join(project.resources.Repository.Close(), project.unlock())
}

func acquireProjectOwnership(path string) (func() error, error) {
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening project lock %s: %w", lockPath, err)
	}
	if err := filelock.TryLock(lock); err != nil {
		lock.Close()
		if filelock.IsUnavailable(err) {
			return nil, fmt.Errorf("%w: %s", ErrProjectAlreadyOpen, path)
		}
		return nil, fmt.Errorf("locking project %s: %w", path, err)
	}
	return func() error {
		return errors.Join(filelock.Unlock(lock), lock.Close())
	}, nil
}

// ResolveProjectPath returns the canonical absolute path for a project file.
func ResolveProjectPath(path string) (string, error) {
	if !strings.HasSuffix(path, ".marasi") {
		return "", errors.New("invalid project path: must end in .marasi")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving project path: %w", err)
	}
	if _, err := os.Lstat(absolutePath); err == nil {
		canonicalPath, err := filepath.EvalSymlinks(absolutePath)
		if err != nil {
			return "", fmt.Errorf("resolving project path: %w", err)
		}
		if !strings.HasSuffix(canonicalPath, ".marasi") {
			return "", errors.New("invalid project path: canonical path must end in .marasi")
		}
		return canonicalPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("checking project path: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return "", fmt.Errorf("resolving project parent directory: %w", err)
	}
	return filepath.Join(parent, filepath.Base(absolutePath)), nil
}

var _ extensions.ProxyService = stagedExtensionService{}

type projectGate struct {
	mu      sync.Mutex
	changed chan struct{}
	blocked bool
	active  int
}

func newProjectGate() *projectGate {
	return &projectGate{changed: make(chan struct{})}
}

func (gate *projectGate) signal() {
	close(gate.changed)
	gate.changed = make(chan struct{})
}

func (gate *projectGate) admit(ctx context.Context) (func(), error) {
	for {
		gate.mu.Lock()
		if !gate.blocked {
			gate.active++
			gate.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					gate.mu.Lock()
					gate.active--
					gate.signal()
					gate.mu.Unlock()
				})
			}, nil
		}
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (gate *projectGate) block(ctx context.Context) (func(), error) {
	gate.mu.Lock()
	gate.blocked = true
	gate.signal()
	for gate.active != 0 {
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-ctx.Done():
			gate.mu.Lock()
			gate.blocked = false
			gate.signal()
			gate.mu.Unlock()
			return nil, ctx.Err()
		case <-changed:
			gate.mu.Lock()
		}
	}
	gate.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			gate.blocked = false
			gate.signal()
			gate.mu.Unlock()
		})
	}, nil
}
