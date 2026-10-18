package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aead.dev/minisign"
	"github.com/tfkr-ae/marasi/internal/update"
)

// updateGitHub is a fake GitHub releases API that counts the checks it
// answers.
type updateGitHub struct {
	server *httptest.Server

	mu      sync.Mutex
	release map[string]any
	nightly map[string]any
	calls   int
}

func newUpdateGitHub(t *testing.T) *updateGitHub {
	t.Helper()
	g := &updateGitHub{}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.calls++
		var rel map[string]any
		switch r.URL.Path {
		case "/releases/latest":
			rel = g.release
		case "/releases/tags/nightly":
			rel = g.nightly
		}
		if rel == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(rel)
	}))
	t.Cleanup(g.server.Close)
	return g
}

func signedAssets() []map[string]any {
	return []map[string]any{
		{"name": "checksums.txt", "browser_download_url": "https://example.invalid/checksums.txt", "size": 10},
		{"name": "checksums.txt.minisig", "browser_download_url": "https://example.invalid/checksums.txt.minisig", "size": 10},
	}
}

func (g *updateGitHub) setRelease(tag string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.release = map[string]any{
		"tag_name": tag, "name": tag, "body": "notes for " + tag,
		"html_url":     "https://github.com/tfkr-ae/marasi/releases/tag/" + tag,
		"published_at": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"assets":       signedAssets(),
	}
}

func (g *updateGitHub) setNightly(version string, published time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nightly = map[string]any{
		"tag_name": "nightly", "name": version, "body": "- a change\n\nRef: refs/heads/main\nCommit: 0000000",
		"html_url":     "https://github.com/tfkr-ae/marasi/releases/tag/nightly",
		"published_at": published,
		"assets":       signedAssets(),
	}
}

func (g *updateGitHub) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// fakeClock is a Clock whose time moves only when a test advances it.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []clockWaiter
}

type clockWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, clockWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock on by d and fires every waiter that is due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	pending := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		pending = append(pending, w)
	}
	c.waiters = pending
}

func (c *fakeClock) waiterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// waitForWaiters waits until n loops are sleeping on the clock.
func (c *fakeClock) waitForWaiters(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.waiterCount() != n {
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %d clock waiters, have %d", n, c.waiterCount())
		}
		time.Sleep(time.Millisecond)
	}
}

func updatePublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := public.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

type updatesBuild struct {
	version string
	channel string
}

func newTestUpdates(t *testing.T, configDir string, github *updateGitHub, clock Clock, build updatesBuild) *Updates {
	t.Helper()
	configFile := filepath.Join(configDir, "marasi_config.yaml")
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		if err := os.WriteFile(configFile, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	updates, err := NewUpdates(UpdatesConfig{
		ConfigDir:    configDir,
		Version:      build.version,
		BuildChannel: build.channel,
		PublicKey:    updatePublicKey(t),
		BaseURL:      github.server.URL,
		Clock:        clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return updates
}

func runUpdates(t *testing.T, updates ...*Updates) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, u := range updates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u.Run(ctx)
		}()
	}
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

func availableVersion(t *testing.T, updates *Updates) string {
	t.Helper()
	status, err := updates.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Available == nil {
		return ""
	}
	return status.Available.Build.Version
}

func TestUpdatesInstancesOnOneConfigDirShareOneCheckPerInterval(t *testing.T) {
	github := newUpdateGitHub(t)
	github.setRelease("10.10.2026")
	clock := newFakeClock()
	configDir := t.TempDir()
	build := updatesBuild{version: "01.10.2026", channel: "release"}
	first := newTestUpdates(t, configDir, github, clock, build)
	second := newTestUpdates(t, configDir, github, clock, build)

	runUpdates(t, first, second)
	clock.waitForWaiters(t, 2)
	if got := github.callCount(); got != 1 {
		t.Fatalf("GitHub calls after both instances started = %d, want 1", got)
	}
	for _, u := range []*Updates{first, second} {
		if got := availableVersion(t, u); got != "10.10.2026" {
			t.Fatalf("available = %q, want 10.10.2026", got)
		}
	}

	clock.Advance(5 * time.Hour)
	clock.waitForWaiters(t, 2)
	if got := github.callCount(); got != 1 {
		t.Fatalf("GitHub calls within the interval = %d, want 1", got)
	}

	github.setRelease("15.10.2026")
	clock.Advance(time.Hour)
	clock.waitForWaiters(t, 2)
	if got := github.callCount(); got != 2 {
		t.Fatalf("GitHub calls after one interval = %d, want 2", got)
	}
	for _, u := range []*Updates{first, second} {
		if got := availableVersion(t, u); got != "15.10.2026" {
			t.Fatalf("available after the interval = %q, want 15.10.2026", got)
		}
	}
}

func TestUpdatesSwitchingChannelNeverOffersAnOlderBuild(t *testing.T) {
	ctx := context.Background()
	github := newUpdateGitHub(t)
	github.setNightly("10.10.2026 (nightly abc1234)", time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC))
	clock := newFakeClock()
	configDir := t.TempDir()
	build := updatesBuild{version: "10.10.2026 (nightly abc1234)", channel: "nightly"}
	updates := newTestUpdates(t, configDir, github, clock, build)

	for _, tag := range []string{"05.10.2026", "10.10.2026"} {
		github.setRelease(tag)
		status, err := updates.SetChannel(ctx, update.Release)
		if err != nil {
			t.Fatal(err)
		}
		if status.Channel != update.Release {
			t.Fatalf("channel = %q, want release", status.Channel)
		}
		status, err = updates.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.LastCheck == nil || status.LastCheck.Latest == nil || status.LastCheck.Latest.Build.Version != tag {
			t.Fatalf("last check = %+v, want release %s", status.LastCheck, tag)
		}
		if status.Available != nil {
			t.Fatalf("release %s offered as an update to nightly 10.10.2026", tag)
		}
	}

	github.setRelease("11.10.2026")
	status, err := updates.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Available == nil || status.Available.Build.Version != "11.10.2026" {
		t.Fatalf("available = %+v, want release 11.10.2026", status.Available)
	}

	other := newTestUpdates(t, configDir, github, clock, build)
	status, err = other.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Channel != update.Release || status.Available == nil {
		t.Fatalf("another instance on the config dir: channel %q available %v, want release with an update", status.Channel, status.Available)
	}
	config, err := os.ReadFile(filepath.Join(configDir, "marasi_config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "channel: release") {
		t.Fatalf("marasi_config.yaml = %q, want updates.channel release", config)
	}
}

func TestUpdatesReleaseBuildOnNightlyChannelIsNotOfferedAnOlderNightly(t *testing.T) {
	ctx := context.Background()
	github := newUpdateGitHub(t)
	github.setNightly("09.10.2026 (nightly abc1234)", time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC))
	updates := newTestUpdates(t, t.TempDir(), github, newFakeClock(), updatesBuild{version: "10.10.2026", channel: "release"})

	if _, err := updates.SetChannel(ctx, update.Nightly); err != nil {
		t.Fatal(err)
	}
	status, err := updates.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Available != nil {
		t.Fatalf("nightly 09.10.2026 offered as an update to release 10.10.2026")
	}
}

func TestUpdatesLearnsTheInstalledNightlysPublishTimeFromACheck(t *testing.T) {
	ctx := context.Background()
	github := newUpdateGitHub(t)
	installedAt := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	github.setNightly("10.10.2026 (nightly abc1234)", installedAt)
	configDir := t.TempDir()
	build := updatesBuild{version: "10.10.2026 (nightly abc1234)", channel: "nightly"}
	if _, err := newTestUpdates(t, configDir, github, newFakeClock(), build).Check(ctx); err != nil {
		t.Fatal(err)
	}

	// A later instance of the same build, after GitHub replaced its nightly
	// with one from another commit published earlier the same day.
	updates := newTestUpdates(t, configDir, github, newFakeClock(), build)
	github.setNightly("10.10.2026 (nightly def5678)", installedAt.Add(-time.Hour))
	status, err := updates.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Available != nil {
		t.Fatalf("nightly published before the installed one offered as an update")
	}

	github.setNightly("10.10.2026 (nightly def5678)", installedAt.Add(time.Hour))
	status, err = updates.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Available == nil || status.Available.Build.Commit != "def5678" {
		t.Fatalf("available = %+v, want nightly def5678 published after the installed one", status.Available)
	}
}

func TestUpdatesAutoCheckOffMakesNoGitHubCalls(t *testing.T) {
	github := newUpdateGitHub(t)
	github.setRelease("10.10.2026")
	clock := newFakeClock()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "marasi_config.yaml"), []byte("updates:\n  auto_check: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updates := newTestUpdates(t, configDir, github, clock, updatesBuild{version: "01.10.2026", channel: "release"})

	runUpdates(t, updates)
	clock.waitForWaiters(t, 1)
	clock.Advance(DefaultUpdateCheckInterval)
	clock.waitForWaiters(t, 1)
	if got := github.callCount(); got != 0 {
		t.Fatalf("GitHub calls with auto_check off = %d, want 0", got)
	}
	status, err := updates.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.AutoCheck {
		t.Fatal("status reports auto check on")
	}
}

func TestUpdatesAutoCheckTurnedBackOnChecksWithinMinutes(t *testing.T) {
	github := newUpdateGitHub(t)
	github.setRelease("10.10.2026")
	clock := newFakeClock()
	configDir := t.TempDir()
	configFile := filepath.Join(configDir, "marasi_config.yaml")
	if err := os.WriteFile(configFile, []byte("updates:\n  auto_check: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updates := newTestUpdates(t, configDir, github, clock, updatesBuild{version: "01.10.2026", channel: "release"})
	runUpdates(t, updates)
	clock.waitForWaiters(t, 1)

	if err := os.WriteFile(configFile, []byte("updates:\n  auto_check: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	clock.Advance(autoCheckOffPoll)
	clock.waitForWaiters(t, 1)

	if got := github.callCount(); got != 1 {
		t.Fatalf("\nwanted:\n1 GitHub call within %s of auto_check turning on\ngot:\n%d", autoCheckOffPoll, got)
	}
}

func TestUpdatesDevelopmentBuildNeverChecks(t *testing.T) {
	github := newUpdateGitHub(t)
	github.setRelease("10.10.2026")
	for _, build := range []updatesBuild{
		{version: "18.10.2026 (dev)", channel: ""},
		{version: "dev", channel: ""},
		{version: "18.10.2026 (dev)", channel: "release"},
	} {
		updates := newTestUpdates(t, t.TempDir(), github, newFakeClock(), build)
		updates.Run(context.Background()) // returns at once
		if _, err := updates.Check(context.Background()); !errors.Is(err, update.ErrDevBuild) {
			t.Fatalf("%v: Check error = %v, want ErrDevBuild", build, err)
		}
		status, err := updates.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !status.Development || status.Channel != "" || updates.Updater() != nil {
			t.Fatalf("%v: status = %+v, want a development build with no channel", build, status)
		}
	}
	if got := github.callCount(); got != 0 {
		t.Fatalf("GitHub calls from development builds = %d, want 0", got)
	}
}

func TestUpdatesSwitchingChannelChecksTheNewChannel(t *testing.T) {
	github := newUpdateGitHub(t)
	github.setRelease("01.10.2026")
	github.setNightly("12.10.2026 (nightly abc1234)", time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC))
	clock := newFakeClock()
	updates := newTestUpdates(t, t.TempDir(), github, clock, updatesBuild{version: "01.10.2026", channel: "release"})
	runUpdates(t, updates)
	clock.waitForWaiters(t, 1)

	if _, err := updates.SetChannel(context.Background(), update.Nightly); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for availableVersion(t, updates) != "12.10.2026 (nightly abc1234)" {
		if time.Now().After(deadline) {
			t.Fatal("the nightly was not checked after switching to it")
		}
		time.Sleep(time.Millisecond)
	}
	if got := github.callCount(); got != 2 {
		t.Fatalf("GitHub calls = %d, want 2 (release on start, nightly on switch)", got)
	}
}
