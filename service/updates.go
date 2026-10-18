package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/internal/filelock"
	"github.com/tfkr-ae/marasi/internal/update"
)

const (
	// DefaultUpdateCheckInterval is how often an instance checks its channel.
	DefaultUpdateCheckInterval = 6 * time.Hour
	// updateCheckTimeout bounds one automatic check.
	updateCheckTimeout = time.Minute
	// updateCheckFile caches the last check of each channel for every
	// instance using the config dir; updateCheckLockFile guards it.
	updateCheckFile     = "update_check.json"
	updateCheckLockFile = "update_check.lock"
	// recordedNightlies bounds how many nightly publish times the cache keeps.
	recordedNightlies = 16
)

// Clock is the time source of the automatic checks.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// UpdatesConfig is what an instance's Updates needs.
type UpdatesConfig struct {
	// ConfigDir holds marasi_config.yaml and the shared check cache.
	ConfigDir string
	// Version, BuildChannel and PublicKey are the build identity (main.version,
	// main.buildChannel, main.signingPublicKey). An empty BuildChannel, a
	// development version, or an empty key make a development build, which
	// never checks.
	Version      string
	BuildChannel string
	PublicKey    string
	// BaseURL and Client reach GitHub; update.Config defaults when empty.
	BaseURL string
	Client  *http.Client
	// Clock is the system clock when nil.
	Clock Clock
	// Interval is DefaultUpdateCheckInterval when zero.
	Interval time.Duration
	// OnCheck, when set, runs after every check this instance makes or
	// reads from the cache, with the status that followed it.
	OnCheck func(UpdateStatus)
	// Executable is the installed build this instance runs and an install
	// replaces; update.Executable() when empty.
	Executable string
	// Team reads the Apple team of a build on macOS; update.Codesign{} when
	// nil.
	Team update.TeamReader
}

// UpdateStatus is what an instance knows about updates for its installed
// build.
type UpdateStatus struct {
	// Installed is the installed build. A development build has only Version.
	Installed update.Build
	// Development is true for a build that never checks or installs.
	Development bool
	// Channel is the configured channel, or the build's own when none is
	// configured. Empty for a development build.
	Channel update.Channel
	// AutoCheck reports updates.auto_check.
	AutoCheck bool
	// LastCheck is the last check of Channel by any instance on this config
	// dir, or nil when there has been none.
	LastCheck *UpdateCheck
	// Available is the update on Channel from LastCheck, or nil. It is never
	// older than the installed build (ADR-0049), and nil once an install has
	// replaced the installed build: a restart runs that build instead.
	Available *update.Candidate
}

// UpdateCheck is one check of a channel.
type UpdateCheck struct {
	At time.Time `json:"at"`
	// Latest is the latest build on the channel, even if older than the
	// installed build; nil when the check failed.
	Latest *update.Candidate `json:"latest,omitempty"`
	// Error says why the check failed.
	Error string `json:"error,omitempty"`
}

// Updates owns an instance's update channel, the cached last check shared
// by every instance on the config dir, and the automatic checks.
type Updates struct {
	configDir    string
	version      string
	buildChannel update.Channel
	updater      *update.Updater // nil for a development build
	clock        Clock
	interval     time.Duration
	onCheck      func(UpdateStatus)
	wake         chan struct{}
	// executable is the installed build, symlinks resolved, and started the
	// file it was when this instance started; nil when it could not be read.
	executable string
	started    os.FileInfo
}

// NewUpdates returns the updates component of an instance. A development
// build gets one that never checks; other errors are a malformed version or
// key.
func NewUpdates(cfg UpdatesConfig) (*Updates, error) {
	u := &Updates{
		configDir: cfg.ConfigDir,
		version:   cfg.Version,
		clock:     cfg.Clock,
		interval:  cfg.Interval,
		onCheck:   cfg.OnCheck,
		wake:      make(chan struct{}, 1),
	}
	if u.clock == nil {
		u.clock = systemClock{}
	}
	if u.interval <= 0 {
		u.interval = DefaultUpdateCheckInterval
	}
	u.executable = cfg.Executable
	if u.executable == "" {
		u.executable, _ = update.Executable()
	} else if resolved, err := filepath.EvalSymlinks(u.executable); err == nil {
		u.executable = resolved
	}
	if u.executable != "" {
		u.started, _ = os.Stat(u.executable)
	}
	if cfg.BuildChannel == "" {
		return u, nil
	}
	channel, err := update.ParseChannel(cfg.BuildChannel)
	if err != nil {
		return nil, err
	}
	updater, err := update.New(update.Config{
		Version:   cfg.Version,
		PublicKey: cfg.PublicKey,
		BaseURL:   cfg.BaseURL,
		Client:    cfg.Client,
		Team:      cfg.Team,
	})
	if errors.Is(err, update.ErrDevBuild) {
		return u, nil
	}
	if err != nil {
		return nil, err
	}
	u.buildChannel = channel
	u.updater = updater
	return u, nil
}

// Updater installs updates for the installed build; nil for a development
// build.
func (u *Updates) Updater() *update.Updater { return u.updater }

// InstallPath is the installed build an install replaces, symlinks
// resolved; "" when it could not be found.
func (u *Updates) InstallPath() string { return u.executable }

// RestartRequired reports whether the file at InstallPath is no longer the
// one this instance started from: an install, by any instance or by hand,
// replaced it (ADR-0050).
func (u *Updates) RestartRequired() bool {
	if u.started == nil {
		return false
	}
	now, err := os.Stat(u.executable)
	if err != nil {
		return true
	}
	return !os.SameFile(u.started, now) || !now.ModTime().Equal(u.started.ModTime()) || now.Size() != u.started.Size()
}

// Run checks on start and then once an interval until ctx is done, unless
// updates.auto_check is off. An instance reuses a check another instance on
// the config dir made within the interval. A development build returns at
// once.
func (u *Updates) Run(ctx context.Context) {
	if u.updater == nil {
		return
	}
	for {
		wait := u.interval
		if status, err := u.autoCheck(ctx); err == nil && status.LastCheck != nil {
			if next := status.LastCheck.At.Add(u.interval).Sub(u.clock.Now()); next > 0 && next < wait {
				wait = next
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-u.clock.After(wait):
		case <-u.wake:
		}
	}
}

// autoCheck checks the configured channel when auto check is on and the
// cached check is older than the interval.
func (u *Updates) autoCheck(ctx context.Context) (UpdateStatus, error) {
	settings, err := u.settings(ctx)
	if err != nil || !settings.AutoCheckEnabled() {
		return UpdateStatus{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	status, err := u.check(ctx, false)
	if err == nil && u.onCheck != nil {
		u.onCheck(status)
	}
	return status, err
}

// Check checks the configured channel on GitHub now, whatever the cache
// holds, and shares the result with the other instances.
func (u *Updates) Check(ctx context.Context) (UpdateStatus, error) {
	status, err := u.check(ctx, true)
	if err == nil && u.onCheck != nil {
		u.onCheck(status)
	}
	return status, err
}

// Latest fetches the latest build on the configured channel, even an older
// one (a reinstall), and shares it as that channel's last check.
func (u *Updates) Latest(ctx context.Context) (update.Candidate, error) {
	status, err := u.check(ctx, true)
	if err != nil {
		return update.Candidate{}, err
	}
	if status.LastCheck.Latest == nil {
		return update.Candidate{}, errors.New(status.LastCheck.Error)
	}
	return *status.LastCheck.Latest, nil
}

// Status reports the channel, the last check, and the available update
// without calling GitHub.
func (u *Updates) Status(ctx context.Context) (UpdateStatus, error) {
	settings, err := u.settings(ctx)
	if err != nil {
		return UpdateStatus{}, err
	}
	cache, err := readUpdateCache(u.configDir)
	if err != nil {
		return UpdateStatus{}, err
	}
	return u.status(settings, cache), nil
}

// SetChannel saves channel in marasi_config.yaml for every instance on the
// config dir. It never installs anything, and an older build on the new
// channel is never offered (ADR-0049). The automatic check runs again so the
// new channel's last check is current.
func (u *Updates) SetChannel(ctx context.Context, channel update.Channel) (UpdateStatus, error) {
	if u.updater == nil {
		return UpdateStatus{}, update.ErrDevBuild
	}
	if _, err := update.ParseChannel(string(channel)); err != nil {
		return UpdateStatus{}, err
	}
	err := u.withConfig(ctx, func(cfg *marasi.Config) error {
		return cfg.SetUpdateChannel(string(channel))
	})
	if err != nil {
		return UpdateStatus{}, err
	}
	select {
	case u.wake <- struct{}{}:
	default:
	}
	return u.Status(ctx)
}

// check returns the status after a check of the configured channel. Unless
// force is set, a check from within the interval is reused instead of
// calling GitHub. The cache lock is held across the GitHub call so that
// instances checking at once make one call.
func (u *Updates) check(ctx context.Context, force bool) (status UpdateStatus, resultErr error) {
	if u.updater == nil {
		return UpdateStatus{}, update.ErrDevBuild
	}
	settings, err := u.settings(ctx)
	if err != nil {
		return UpdateStatus{}, err
	}
	channel := u.channel(settings)

	lock, err := acquireFileLock(ctx, filepath.Join(u.configDir, updateCheckLockFile), "update check")
	if err != nil {
		return UpdateStatus{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, filelock.Unlock(lock), lock.Close())
	}()
	cache, err := readUpdateCache(u.configDir)
	if err != nil {
		return UpdateStatus{}, err
	}
	now := u.clock.Now()
	if last, ok := cache.Checks[channel]; ok && !force && !now.Before(last.At) && now.Sub(last.At) < u.interval {
		return u.status(settings, cache), nil
	}

	result := UpdateCheck{At: now}
	latest, err := u.updater.Latest(ctx, channel)
	switch {
	case err == nil:
		result.Latest = &latest
		if channel == update.Nightly {
			cache.recordNightly(latest.Build)
		}
	case ctx.Err() != nil:
		return UpdateStatus{}, err
	default:
		result.Error = err.Error()
	}
	cache.Checks[channel] = result
	if err := writeUpdateCache(u.configDir, cache); err != nil {
		return UpdateStatus{}, err
	}
	return u.status(settings, cache), nil
}

// status puts the settings and cache together for the installed build.
func (u *Updates) status(settings marasi.UpdatesConfig, cache updateCache) UpdateStatus {
	status := UpdateStatus{AutoCheck: settings.AutoCheckEnabled()}
	if u.updater == nil {
		status.Installed = update.Build{Version: u.version}
		status.Development = true
		return status
	}
	status.Installed = u.updater.Installed()
	status.Channel = u.channel(settings)
	last, ok := cache.Checks[status.Channel]
	if !ok {
		return status
	}
	status.LastCheck = &last
	if last.Latest == nil || !last.Latest.Signed() || u.RestartRequired() {
		return status
	}
	installed := status.Installed
	if installed.Nightly && installed.Published.IsZero() {
		installed.Published = cache.nightlyPublished(installed.Commit)
	}
	if update.IsUpdate(status.Channel, installed, last.Latest.Build) {
		status.Available = last.Latest
	}
	return status
}

// channel is the configured channel, or the build's own.
func (u *Updates) channel(settings marasi.UpdatesConfig) update.Channel {
	if settings.Channel != "" {
		return update.Channel(settings.Channel)
	}
	return u.buildChannel
}

// settings rereads the updates section of marasi_config.yaml.
func (u *Updates) settings(ctx context.Context) (marasi.UpdatesConfig, error) {
	var settings marasi.UpdatesConfig
	err := u.withConfig(ctx, func(cfg *marasi.Config) error {
		settings = cfg.Updates
		return nil
	})
	return settings, err
}

// withConfig rereads marasi_config.yaml under the config lock (ADR-0013).
func (u *Updates) withConfig(ctx context.Context, operation func(*marasi.Config) error) (resultErr error) {
	lock, err := acquireConfigLock(ctx, u.configDir)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, filelock.Unlock(lock), lock.Close())
	}()
	cfg, err := marasi.ReadConfig(u.configDir)
	if err != nil {
		return err
	}
	return operation(cfg)
}

// updateCache is update_check.json.
type updateCache struct {
	Checks map[update.Channel]UpdateCheck `json:"checks"`
	// Nightlies are the publish times of nightlies seen by checks, newest
	// last. An installed nightly does not know its own publish time; once
	// GitHub has replaced it, this is where an instance learns it.
	Nightlies []recordedNightly `json:"nightlies,omitempty"`
}

type recordedNightly struct {
	Commit    string    `json:"commit"`
	Published time.Time `json:"published"`
}

func (c *updateCache) recordNightly(build update.Build) {
	if build.Commit == "" || build.Published.IsZero() {
		return
	}
	kept := c.Nightlies[:0]
	for _, n := range c.Nightlies {
		if !update.SameCommit(n.Commit, build.Commit) {
			kept = append(kept, n)
		}
	}
	kept = append(kept, recordedNightly{Commit: build.Commit, Published: build.Published})
	if len(kept) > recordedNightlies {
		kept = kept[len(kept)-recordedNightlies:]
	}
	c.Nightlies = kept
}

// nightlyPublished is the recorded publish time of the nightly from commit,
// or zero when no check has seen it.
func (c updateCache) nightlyPublished(commit string) time.Time {
	for _, n := range c.Nightlies {
		if update.SameCommit(n.Commit, commit) {
			return n.Published
		}
	}
	return time.Time{}
}

// readUpdateCache reads the shared cache. A missing or unreadable cache is
// empty: the next check rewrites it.
func readUpdateCache(configDir string) (updateCache, error) {
	cache := updateCache{Checks: map[update.Channel]UpdateCheck{}}
	data, err := os.ReadFile(filepath.Join(configDir, updateCheckFile))
	if errors.Is(err, os.ErrNotExist) {
		return cache, nil
	}
	if err != nil {
		return cache, fmt.Errorf("reading the update check cache: %w", err)
	}
	if json.Unmarshal(data, &cache) != nil || cache.Checks == nil {
		return updateCache{Checks: map[update.Channel]UpdateCheck{}}, nil
	}
	return cache, nil
}

// writeUpdateCache replaces the shared cache through a temporary file so a
// reader never sees half of it.
func writeUpdateCache(configDir string, cache updateCache) (resultErr error) {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(configDir, ".marasi-update-check-*.tmp")
	if err != nil {
		return fmt.Errorf("writing the update check cache: %w", err)
	}
	defer func() {
		if resultErr != nil {
			os.Remove(file.Name())
		}
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("writing the update check cache: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("writing the update check cache: %w", err)
	}
	if err := os.Rename(file.Name(), filepath.Join(configDir, updateCheckFile)); err != nil {
		return fmt.Errorf("writing the update check cache: %w", err)
	}
	return nil
}
