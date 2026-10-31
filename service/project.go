package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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

const (
	// waitReportDelay is how long a switch waits on project work before it
	// reports what it is waiting for.
	waitReportDelay = 500 * time.Millisecond
	// waitReportInterval is how often a waiting switch checks for a change.
	waitReportInterval = 100 * time.Millisecond
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
	// cancelJobs stops the project's background jobs, and jobsDone closes
	// when they have returned. They are set and used only with
	// ProjectLifecycle.mu held.
	cancelJobs context.CancelFunc
	jobsDone   <-chan struct{}
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
	// opening, upgrading and openingFailed announce a switch from its start:
	// every opening ends in opened or openingFailed.
	opening       func(target string)
	upgrading     func(target string, step db.MigrationStep)
	openingFailed func(target string, err error)
	opened        func(string)
	// waiting receives what a switch to the target project is still waiting
	// for. waitReportDelay is how long a switch waits before it reports.
	waiting          func(target string, waits []ProjectWait)
	waitReportDelay  time.Duration
	logAdded         func(*domain.Log)
	armoryRunUpdated func(*domain.ArmoryRun)
	extensionOptions []func(*extensions.Runtime) error
	jobEvents        jobEvents
	jobBatch         func(ctx context.Context, path string, runner jobRunner, name domain.JobName) (int, bool, error)
	// jobProgressInterval is the shortest time between two progress
	// reports for one job.
	jobProgressInterval time.Duration
	executionContext    context.Context
	cancelExecution     context.CancelFunc
}

// NewProjectLifecycle creates a lifecycle with no open project. Open must
// succeed before the service instance starts accepting work. Every project it
// opens applies extensionOptions to each extension before that extension's
// code runs, so an embedder's hooks are in place for top-level code and startup.
func NewProjectLifecycle(proxy *marasi.Proxy, configDir string, wordlists wordlist.Provider, logger *slog.Logger, extensionOptions ...func(*extensions.Runtime) error) *ProjectLifecycle {
	executionContext, cancelExecution := context.WithCancel(context.Background())
	lifecycle := &ProjectLifecycle{
		executionContext: executionContext,
		cancelExecution:  cancelExecution,
		proxy:            proxy,
		configDir:        configDir,
		wordlists:        wordlists,
		logger:           logger,
		extensionOptions: extensionOptions,
		lock:             acquireProjectOwnership,
		gate:             newProjectGate(),
	}
	lifecycle.prepare = lifecycle.prepareProject
	lifecycle.jobBatch = runJobBatch
	lifecycle.jobProgressInterval = jobProgressInterval
	lifecycle.waitReportDelay = waitReportDelay
	lifecycle.flushOpenProject = proxy.CloseWebSocketsAndFlush
	_ = proxy.WithOptions(marasi.WithWorkAdmission(lifecycle.gate.admit))
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
func (lifecycle *ProjectLifecycle) Open(ctx context.Context, target string) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lifecycle.executionContext, cancel)
	defer stop()
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()

	path, err := ResolveProjectPath(target)
	if err != nil {
		return err
	}
	if lifecycle.Path() == path {
		return nil
	}
	if lifecycle.opening != nil {
		lifecycle.opening(path)
	}
	defer func() {
		// A cleanup failure comes after the target was published as opened.
		if err != nil && !errors.Is(err, ErrProjectCleanup) && lifecycle.openingFailed != nil {
			lifecycle.openingFailed(path, err)
		}
	}()

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

	// A launch held at Checkpoint keeps the project busy. Interrupt pending
	// launches first, so they end in the project they were launched in.
	lifecycle.proxy.InterruptLaunches()
	old := lifecycle.openProject()
	stopReporting := lifecycle.reportWaits(path)
	defer stopReporting()
	unblock, err := lifecycle.waitForHandoff(ctx, old)
	if err != nil {
		return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
	}
	defer unblock()
	if err := ctx.Err(); err != nil {
		return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
	}
	flushed := make(chan error, 1)
	flush := lifecycle.flushOpenProject
	go func() { flushed <- flush() }()
	select {
	case err := <-flushed:
		if err != nil {
			return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
		}
	case <-ctx.Done():
		return errors.Join(ctx.Err(), cleanupPreparedProject(path, true, resources, unlock))
	}
	// Closing a WebSocket can run Lua on its peer's final frame. Drain that
	// work, its nested requests, and any Checkpoint item it held before
	// replacing repositories.
	if lifecycle.armoryBusy(old) {
		return errors.Join(ErrProjectBusy, cleanupPreparedProject(path, true, resources, unlock))
	}
	if err := lifecycle.gate.seal(ctx); err != nil {
		return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
	}
	stopReporting()
	// The late nested requests may have queued persistence after the first
	// flush. The seal keeps anything else from queuing more.
	if err := lifecycle.flushOpenProject(); err != nil {
		return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
	}

	lifecycle.proxy.SetProjectResources(resources)
	lifecycle.gate.publish()
	lifecycle.setOpen(targetProject)
	if old != nil && lifecycle.opened != nil {
		lifecycle.opened(path)
	}
	if repository, ok := resources.Repository.(*eventLogRepository); ok {
		repository.startPublishing()
	}
	lifecycle.startJobs(targetProject)
	if old == nil {
		return nil
	}
	if err := closeProject(old); err != nil {
		lifecycle.logError("Failed to clean up previous project", old.path, err)
		return errors.Join(ErrProjectCleanup, err)
	}
	return nil
}

// eventLogRepository is the repository the proxy holds for an open project.
// It wraps the project's *db.Repository to publish log.added once a log is
// stored, holding events until startPublishing. It embeds the interface, so
// only RepositoryProvider methods pass through: a type assertion on it to
// *db.Repository, or to an interface with any other method, fails. Service
// code that needs such a method unwraps it, as projectJobRunner does.
type eventLogRepository struct {
	marasi.RepositoryProvider
	mu         sync.Mutex
	onStored   func(*domain.Log)
	publishing bool
	pending    []*domain.Log
}

func (repository *eventLogRepository) InsertLog(entry *domain.Log) error {
	if err := repository.RepositoryProvider.InsertLog(entry); err != nil {
		return err
	}
	repository.mu.Lock()
	if !repository.publishing {
		repository.pending = append(repository.pending, entry)
		repository.mu.Unlock()
		return nil
	}
	repository.mu.Unlock()
	repository.onStored(entry)
	return nil
}

func (repository *eventLogRepository) startPublishing() {
	repository.mu.Lock()
	repository.publishing = true
	pending := repository.pending
	repository.pending = nil
	repository.mu.Unlock()
	for _, entry := range pending {
		repository.onStored(entry)
	}
}

// waitForHandoff blocks admission until in-flight work finishes, including
// held Checkpoint items. Checkpoint routes are not admitted, so a held item
// can still be forwarded or dropped while admission is blocked (docs/adr/2026-10-17-project-switch-waits-for-held-checkpoint-items.md).
func (lifecycle *ProjectLifecycle) waitForHandoff(ctx context.Context, project *openProject) (func(), error) {
	if lifecycle.armoryBusy(project) {
		return nil, ErrProjectBusy
	}
	// A held WebSocket message is not admitted work, so it is waited on here.
	unblock, err := lifecycle.gate.block(ctx, func() bool { return !lifecycle.proxy.HasPendingCheckpoint() })
	if err != nil {
		return nil, err
	}
	if lifecycle.armoryBusy(project) {
		unblock()
		return nil, ErrProjectBusy
	}
	if err := ctx.Err(); err != nil {
		unblock()
		return nil, err
	}
	return unblock, nil
}

// armoryBusy reports an active Armory run, which a switch refuses rather than
// waits for: the run's client owns it.
func (lifecycle *ProjectLifecycle) armoryBusy(project *openProject) bool {
	return project != nil && project.resources.Armory != nil && len(project.resources.Armory.ActiveRunIDs()) != 0
}

// ProjectWait is one thing a project switch is still waiting for.
type ProjectWait struct {
	// Kind is checkpoint, extension, or work.
	Kind string `json:"kind"`
	// ID is the Checkpoint item's id.
	ID string `json:"id,omitempty"`
	// Type is the Checkpoint item's type: request, response, or websocket.
	Type string `json:"type,omitempty"`
	// Summary is the first line of a held HTTP item, or a WebSocket message's direction.
	Summary string `json:"summary,omitempty"`
	// Name is the name of an extension whose Lua is running.
	Name string `json:"name,omitempty"`
	// Count is the number of other admitted requests and control calls.
	Count int `json:"count,omitempty"`
}

// checkpointSummary names a held Checkpoint item: a WebSocket frame's
// direction, or the first line of an HTTP request or response.
func checkpointSummary(item domain.CheckpointItem) string {
	if item.Type == domain.CheckpointTypeWebSocket {
		return item.Direction
	}
	line, _, _ := strings.Cut(string(item.Raw), "\n")
	return strings.TrimSuffix(line, "\r")
}

// projectWaits lists the project work a switch is waiting for: held
// Checkpoint items, running extensions, then other admitted work.
func (lifecycle *ProjectLifecycle) projectWaits() []ProjectWait {
	var waits []ProjectWait
	heldHTTP := 0
	for _, item := range lifecycle.proxy.CheckpointItems() {
		if item.Type != domain.CheckpointTypeWebSocket {
			heldHTTP++
		}
		waits = append(waits, ProjectWait{Kind: "checkpoint", ID: item.ID.String(), Type: item.Type, Summary: checkpointSummary(item)})
	}
	active, running := lifecycle.gate.snapshot()
	for _, name := range running {
		waits = append(waits, ProjectWait{Kind: "extension", Name: name})
	}
	// A held HTTP item is admitted work, so it is counted once, as the item.
	if other := active - heldHTTP; other > 0 {
		waits = append(waits, ProjectWait{Kind: "work", Count: other})
	}
	return waits
}

// reportWaits reports what a switch to target waits for once it has waited
// waitReportDelay, then again each time that changes. The returned stop is
// idempotent and returns once reporting has ended.
func (lifecycle *ProjectLifecycle) reportWaits(target string) func() {
	if lifecycle.waiting == nil {
		return func() {}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	delay := lifecycle.waitReportDelay
	go func() {
		defer close(finished)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
		}
		ticker := time.NewTicker(waitReportInterval)
		defer ticker.Stop()
		var last []ProjectWait
		for {
			if waits := lifecycle.projectWaits(); len(waits) != 0 && !slices.Equal(waits, last) {
				lifecycle.waiting(target, waits)
				last = waits
			}
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	return sync.OnceFunc(func() {
		close(done)
		<-finished
	})
}

// Admit admits project-bound work and returns its matching release operation.
func (lifecycle *ProjectLifecycle) Admit(ctx context.Context) (func(), error) {
	release, _, err := lifecycle.gate.admit(ctx, false)
	return release, err
}

// Shutdown flushes, closes, and releases the open project.
func (lifecycle *ProjectLifecycle) Shutdown() error {
	// Interrupt before taking mu: an open may be waiting for running Lua.
	lifecycle.cancelExecution()
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	project := lifecycle.openProject()
	if project == nil {
		return nil
	}
	lifecycle.proxy.InterruptLaunches()
	unblock, err := lifecycle.gate.block(context.Background(), nil)
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

// projectRelease is an open project given up for a restart. Until reopen or
// abandon, no project can open and no project work is admitted.
type projectRelease struct {
	path string
	// reopen takes the project back and admits work again. It is nil when
	// nothing was released.
	reopen func(context.Context) error
	// abandon leaves the project closed, for an instance about to exit.
	abandon func()
}

// release ends launches and Armory runs, waits for admitted work, then
// flushes, closes, and unlocks the open project so a successor can own it
// (docs/adr/2026-10-18-restart-hands-over-and-rolls-back.md). dropHeld is called while waiting, so an item held during the
// wait does not keep the project busy; when it fails instead, the release
// stops and returns its error with nothing released.
func (lifecycle *ProjectLifecycle) release(ctx context.Context, dropHeld func() error) (projectRelease, error) {
	lifecycle.mu.Lock()
	project := lifecycle.openProject()
	if project == nil {
		lifecycle.mu.Unlock()
		return projectRelease{}, errors.New("no open project")
	}
	lifecycle.proxy.InterruptLaunches()
	if project.resources.Armory != nil {
		project.resources.Armory.Shutdown()
	}
	blockCtx, cancelBlock := context.WithCancelCause(ctx)
	defer cancelBlock(nil)
	unblock, err := lifecycle.gate.block(blockCtx, func() bool {
		if err := dropHeld(); err != nil {
			cancelBlock(err)
			return false
		}
		return !lifecycle.proxy.HasPendingCheckpoint()
	})
	if err != nil {
		lifecycle.mu.Unlock()
		if cause := context.Cause(blockCtx); cause != nil && ctx.Err() == nil {
			err = cause
		}
		return projectRelease{}, err
	}
	finished := false
	finish := func() {
		if !finished {
			finished = true
			lifecycle.mu.Unlock()
		}
	}
	path := project.path
	reopen := func(ctx context.Context) error {
		defer finish()
		defer unblock()
		unlock, err := lifecycle.lock(path)
		if err != nil {
			return err
		}
		resources, err := lifecycle.prepare(ctx, path)
		if err != nil {
			return errors.Join(err, cleanupPreparedProject(path, true, resources, unlock))
		}
		target := &openProject{path: path, resources: resources, unlock: unlock}
		lifecycle.proxy.SetProjectResources(resources)
		lifecycle.gate.publish()
		lifecycle.setOpen(target)
		if repository, ok := resources.Repository.(*eventLogRepository); ok {
			repository.startPublishing()
		}
		lifecycle.startJobs(target)
		return nil
	}
	// An abandoned project is never reopened here: a switch that was waiting
	// for the restart fails rather than open a project in an exiting instance.
	abandon := func() {
		lifecycle.cancelExecution()
		lifecycle.gate.close()
		finish()
	}
	released := projectRelease{path: path, reopen: reopen, abandon: abandon}
	flushErr := lifecycle.proxy.CloseWebSocketsAndFlush()
	closeErr := closeProject(project)
	lifecycle.setOpen(nil)
	return released, errors.Join(flushErr, closeErr)
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
	connection, err := db.New(path, lifecycle.logger, db.WithMigrationProgress(func(step db.MigrationStep) {
		if lifecycle.upgrading != nil {
			lifecycle.upgrading(path, step)
		}
	}))
	if err != nil {
		return marasi.ProjectResources{}, fmt.Errorf("opening project: %w", err)
	}
	repository := db.NewProxyRepo(connection)
	logRepository := &eventLogRepository{
		RepositoryProvider: repository,
		onStored:           lifecycle.publishLogAdded,
	}
	resources := marasi.ProjectResources{Repository: logRepository, Scope: compass.NewScope(true)}
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

	// A local read: opening a project never waits on the network, and a
	// selection this machine lacks still opens and fails its traffic closed.
	selection, err := marasi.LoadUpstreamProxySelection(repository)
	if errors.Is(err, marasi.ErrInvalidUpstreamProxy) {
		// The selection is the raw value, which no list entry matches, so
		// the project opens and its traffic fails closed.
		err = nil
	}
	if err != nil {
		return fail(fmt.Errorf("loading the upstream proxy selection: %w", err))
	}
	resources.UpstreamProxy = selection

	storedExtensions, err := repository.GetExtensions()
	if err != nil {
		return fail(fmt.Errorf("loading project extensions: %w", err))
	}
	extensionService := stagedExtensionService{
		configDir:  lifecycle.configDir,
		client:     lifecycle.proxy.Client,
		repository: logRepository,
		scope:      resources.Scope,
	}
	resources.Extensions = make([]*extensions.Runtime, 0, len(storedExtensions))
	for _, stored := range storedExtensions {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		executionContext, cancel := context.WithCancel(lifecycle.executionContext)
		runtime := &extensions.Runtime{Data: stored, ExecutionContext: executionContext, CancelExecution: cancel, TrackExecution: func() func() { return lifecycle.gate.trackExtension(stored.Name) }}
		resources.Extensions = append(resources.Extensions, runtime)
		stop := context.AfterFunc(ctx, cancel)
		if err := runtime.PrepareState(extensionService, lifecycle.extensionOptions); err != nil {
			stop()
			return fail(fmt.Errorf("preparing project extension %s: %w", stored.Name, err))
		}
		stop()
	}

	resources.ReportGenerator, err = report.NewGenerator(repository, report.WithConfigDir(lifecycle.configDir))
	if err != nil {
		return fail(fmt.Errorf("preparing report generator: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	// No run of this project executes yet: the lock is held and the Armory
	// manager is not created, so an in-progress run outlived an instance that
	// stopped mid-run.
	if err := repository.CancelInterruptedArmoryRuns(); err != nil {
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

func (lifecycle *ProjectLifecycle) publishLogAdded(entry *domain.Log) {
	if lifecycle.logAdded != nil {
		lifecycle.logAdded(entry)
	}
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
	for _, runtime := range resources.Extensions {
		if runtime.CancelExecution != nil {
			runtime.CancelExecution()
		}
		runtime.WaitExecution()
	}
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
	stopJobs(project)
	for _, runtime := range project.resources.Extensions {
		if runtime.CancelExecution != nil {
			runtime.CancelExecution()
		}
		runtime.WaitExecution()
	}
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
	// sealed refuses all admission, nested Lua work included, from the
	// moment a handoff sees no project work until it publishes or gives up.
	sealed bool
	// closed refuses all admission for good, once the project is abandoned
	// to a restart's successor and nothing will ever unblock the gate.
	closed bool
	active int
	// extensions counts in-flight extension executions by extension name.
	extensions          map[string]int
	extensionExecutions int
	// generation counts published open projects. Work records the generation
	// it was admitted in, and work admitted again in another generation
	// belongs to a project that is no longer open (docs/adr/2026-10-17-project-switch-never-waits-on-the-network.md).
	generation uint64
}

func newProjectGate() *projectGate {
	return &projectGate{changed: make(chan struct{}), extensions: make(map[string]int)}
}

func (gate *projectGate) signal() {
	close(gate.changed)
	gate.changed = make(chan struct{})
}

// trackExtension counts an in-flight execution of the named extension as
// project-owned work. The returned release is idempotent.
func (gate *projectGate) trackExtension(name string) func() {
	gate.mu.Lock()
	gate.extensionExecutions++
	gate.extensions[name]++
	gate.signal()
	gate.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			gate.extensionExecutions--
			if gate.extensions[name]--; gate.extensions[name] == 0 {
				delete(gate.extensions, name)
			}
			gate.signal()
			gate.mu.Unlock()
		})
	}
}

// snapshot returns the admitted work count and the sorted names of the
// extensions whose Lua is running.
func (gate *projectGate) snapshot() (int, []string) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	names := make([]string, 0, len(gate.extensions))
	for name := range gate.extensions {
		names = append(names, name)
	}
	slices.Sort(names)
	return gate.active, names
}

// errProjectGateClosed refuses work in an instance that handed its project
// to a restart's successor.
var errProjectGateClosed = errors.New("project handed over to a restart's successor")

// admit waits until project-bound work may run and returns its release and
// the generation of the open project it runs in.
func (gate *projectGate) admit(ctx context.Context, internal bool) (func(), uint64, error) {
	for {
		gate.mu.Lock()
		if gate.closed {
			gate.mu.Unlock()
			return nil, 0, errProjectGateClosed
		}
		// Only the self-client can finish nested Lua requests during a handoff.
		if !gate.sealed && (!gate.blocked || internal && gate.extensionExecutions > 0) {
			gate.active++
			generation := gate.generation
			gate.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					gate.mu.Lock()
					gate.active--
					gate.signal()
					gate.mu.Unlock()
				})
			}, generation, nil
		}
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-changed:
		}
	}
}

// block stops new admission and waits for admitted work to finish and, when
// idle is non-nil, for idle to report true.
func (gate *projectGate) block(ctx context.Context, idle func() bool) (func(), error) {
	gate.mu.Lock()
	gate.blocked = true
	gate.signal()
	gate.mu.Unlock()
	if err := gate.drain(ctx, idle, false); err != nil {
		gate.mu.Lock()
		gate.blocked = false
		gate.signal()
		gate.mu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			gate.blocked = false
			gate.sealed = false
			gate.signal()
			gate.mu.Unlock()
		})
	}, nil
}

// close refuses all admission for good, so work waiting to be admitted
// ends instead of holding up the exit of an instance that handed over.
func (gate *projectGate) close() {
	gate.mu.Lock()
	gate.closed = true
	gate.signal()
	gate.mu.Unlock()
}

// seal waits for project work to finish, then refuses all admission until
// publish or unblock. Work cannot start between the wait and the seal, so a
// flush after seal is the last write to the open project.
func (gate *projectGate) seal(ctx context.Context) error {
	return gate.drain(ctx, nil, true)
}

// publish starts the next generation and lifts the seal. Admission stays
// blocked until unblock.
func (gate *projectGate) publish() {
	gate.mu.Lock()
	gate.generation++
	gate.sealed = false
	gate.signal()
	gate.mu.Unlock()
}

// idlePoll is how often drain rechecks an idle condition the gate is not
// signalled about.
const idlePoll = 10 * time.Millisecond

func (gate *projectGate) drain(ctx context.Context, idle func() bool, seal bool) error {
	var poll <-chan time.Time
	if idle != nil {
		ticker := time.NewTicker(idlePoll)
		defer ticker.Stop()
		poll = ticker.C
	}
	for {
		// idle is checked outside the lock: it reads Checkpoint, not the gate.
		idleNow := idle == nil || idle()
		gate.mu.Lock()
		if idleNow && gate.active == 0 && gate.extensionExecutions == 0 {
			if seal {
				gate.sealed = true
			}
			gate.mu.Unlock()
			return nil
		}
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		case <-poll:
		}
	}
}
