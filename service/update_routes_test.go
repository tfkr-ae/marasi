package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aead.dev/minisign"
)

// installGitHub is a fake GitHub that publishes one signed release with a
// build for this platform. While gated, a build download sends half the
// build and waits for release, or for its client to leave.
type installGitHub struct {
	server *httptest.Server
	public string

	mu        sync.Mutex
	private   minisign.PrivateKey
	release   map[string]any
	build     []byte
	gate      chan struct{}
	started   chan struct{}
	downloads int
}

func installAssetName() string {
	name := "marasi-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func newInstallGitHub(t *testing.T) *installGitHub {
	t.Helper()
	public, private, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := public.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	g := &installGitHub{public: string(text), private: private, started: make(chan struct{}, 16)}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	return g
}

// publish makes tag the latest release, with build as its asset for this
// platform.
func (g *installGitHub) publish(tag, build string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.build = []byte(build)
	base := g.server.URL + "/download/" + tag + "/"
	g.release = map[string]any{
		"tag_name": tag, "name": tag, "body": "notes for " + tag,
		"html_url":     "https://github.com/tfkr-ae/marasi/releases/tag/" + tag,
		"published_at": time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC),
		"assets": []map[string]any{
			{"name": "checksums.txt", "browser_download_url": base + "checksums.txt", "size": 100},
			{"name": "checksums.txt.minisig", "browser_download_url": base + "checksums.txt.minisig", "size": 100},
			{"name": installAssetName(), "browser_download_url": base + "build", "size": len(build)},
		},
	}
}

// hold makes build downloads wait after half the build until the returned
// function runs.
func (g *installGitHub) hold() func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	gate := make(chan struct{})
	g.gate = gate
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (g *installGitHub) downloadCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.downloads
}

func (g *installGitHub) checksums() []byte {
	sum := sha256.Sum256(g.build)
	return []byte(hex.EncodeToString(sum[:]) + "  " + installAssetName() + "\n")
}

func (g *installGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	release, build, gate := g.release, g.build, g.gate
	g.mu.Unlock()
	switch {
	case r.URL.Path == "/releases/latest" && release != nil:
		_ = json.NewEncoder(w).Encode(release)
	case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
		g.mu.Lock()
		sums := g.checksums()
		g.mu.Unlock()
		_, _ = w.Write(sums)
	case strings.HasSuffix(r.URL.Path, "/checksums.txt.minisig"):
		g.mu.Lock()
		signature := minisign.Sign(g.private, g.checksums())
		g.mu.Unlock()
		_, _ = w.Write(signature)
	case strings.HasSuffix(r.URL.Path, "/build"):
		g.mu.Lock()
		g.downloads++
		g.mu.Unlock()
		g.started <- struct{}{}
		w.Header().Set("Content-Length", fmt.Sprint(len(build)))
		half := len(build) / 2
		_, _ = w.Write(build[:half])
		if gate != nil {
			w.(http.Flusher).Flush()
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(build[half:])
	default:
		http.NotFound(w, r)
	}
}

// waitForDownload waits until a build download has started.
func (g *installGitHub) waitForDownload(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("\nwanted:\nbuild download\ngot:\nnone within 5s")
	}
}

type fixedTeam struct{}

func (fixedTeam) TeamID(context.Context, string) (string, error) { return "TEAM", nil }

// installedBuildFile writes a stand-in installed build in its own directory.
func installedBuildFile(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "marasi")
	if err := os.WriteFile(target, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	return target
}

// newInstallUpdates is the updates of release 01.10.2026 installed at target,
// with config dir configDir, checking github.
func newInstallUpdates(t *testing.T, configDir, target string, github *installGitHub) *Updates {
	t.Helper()
	configFile := filepath.Join(configDir, "marasi_config.yaml")
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		if err := os.WriteFile(configFile, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	updates, err := NewUpdates(UpdatesConfig{
		ConfigDir:    configDir,
		Version:      "01.10.2026",
		BuildChannel: "release",
		PublicKey:    github.public,
		BaseURL:      github.server.URL,
		Executable:   target,
		Team:         fixedTeam{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return updates
}

// newUpdateServer is an instance running release 01.10.2026 from target,
// with config dir configDir, checking github.
func newUpdateServer(t *testing.T, configDir, target, instance string, github *installGitHub) *Server {
	t.Helper()
	updates := newInstallUpdates(t, configDir, target, github)
	server := NewServer(nil, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil, func() {}, "01.10.2026", instance, "test-project")
	server.SetBuildChannel("release")
	server.SetUpdates(updates)
	t.Cleanup(server.Close)
	return server
}

func requestUpdate(server *Server, method, path, body string) *httptest.ResponseRecorder {
	return requestChrome(server, method, path, body)
}

// nextUpdateEvent waits for the next event named name, skipping other events,
// but failing on another install outcome.
func nextUpdateEvent(t *testing.T, subscriber *eventSubscriber, name string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-subscriber.events:
			if event.name == name {
				return string(event.data)
			}
			if event.name == "update.failed" || event.name == "update.installed" {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s %s", name, event.name, event.data)
			}
		case <-timeout:
			t.Fatalf("\nwanted:\n%s event\ngot:\nnone within 5s", name)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpdateInstallContinuesAfterItsClientDisconnects(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	release := github.hold()
	target := installedBuildFile(t)
	server := newUpdateServer(t, t.TempDir(), target, "work", github)
	events := server.events.subscribe()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/update/install", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("\nwanted:\n202\ngot:\n%d", response.StatusCode)
	}
	github.waitForDownload(t)
	cancel()
	response.Body.Close()
	release()

	want := fmt.Sprintf(`{"instance":"work","version":"18.10.2026","install_path":%q}`, target)
	if got := nextUpdateEvent(t, events, "update.installed"); got != want {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
	}
	if got := readFile(t, target); got != "new build 18.10.2026" {
		t.Fatalf("\nwanted:\nnew build installed\ngot:\n%q", got)
	}
	status := requestUpdate(server, http.MethodGet, "/service/status", "")
	if !strings.Contains(status.Body.String(), `"restart_required":true`) {
		t.Fatalf("\nwanted:\nrestart_required true\ngot:\n%s", status.Body.String())
	}
}

func TestUpdateInstallSecondRequestOnTheSameInstanceJoins(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	release := github.hold()
	target := installedBuildFile(t)
	server := newUpdateServer(t, t.TempDir(), target, "work", github)
	events := server.events.subscribe()

	first := requestUpdate(server, http.MethodPost, "/update/install", "")
	github.waitForDownload(t)
	second := requestUpdate(server, http.MethodPost, "/update/install", "")
	release()

	if first.Code != http.StatusAccepted || !strings.Contains(first.Body.String(), `"joined":false`) {
		t.Fatalf("\nwanted:\n202 joined false\ngot:\n%d %s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusAccepted || !strings.Contains(second.Body.String(), `"joined":true`) || !strings.Contains(second.Body.String(), `"version":"18.10.2026"`) {
		t.Fatalf("\nwanted:\n202 joined true for 18.10.2026\ngot:\n%d %s", second.Code, second.Body.String())
	}
	nextUpdateEvent(t, events, "update.installed")
	if got := github.downloadCount(); got != 1 {
		t.Fatalf("\nwanted:\n1 build download\ngot:\n%d", got)
	}
	again := requestUpdate(server, http.MethodPost, "/update/install", "")
	if again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), `"error":"already_installed"`) {
		t.Fatalf("\nwanted:\n409 already_installed\ngot:\n%d %s", again.Code, again.Body.String())
	}
}

func TestUpdateInstallOnAnotherInstanceIsRefusedNamingTheHolder(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	release := github.hold()
	defer release()
	configDir := t.TempDir()
	target := installedBuildFile(t)
	holder := newUpdateServer(t, configDir, target, "work", github)
	other := newUpdateServer(t, configDir, target, "scratch", github)

	if response := requestUpdate(holder, http.MethodPost, "/update/install", ""); response.Code != http.StatusAccepted {
		t.Fatalf("\nwanted:\n202\ngot:\n%d %s", response.Code, response.Body.String())
	}
	github.waitForDownload(t)
	response := requestUpdate(other, http.MethodPost, "/update/install", "")

	assertChromeResponse(t, response, http.StatusConflict, `{"error":"update_in_progress","message":"instance \"work\" is already installing an update for this build","instance":"work"}`+"\n")
}

func TestUpdateInstallCancelledBeforeTheSwapLeavesTheInstalledBuild(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	release := github.hold()
	defer release()
	target := installedBuildFile(t)
	server := newUpdateServer(t, t.TempDir(), target, "work", github)
	events := server.events.subscribe()

	requestUpdate(server, http.MethodPost, "/update/install", "")
	github.waitForDownload(t)
	response := requestUpdate(server, http.MethodDelete, "/update/install", "")

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"failed"`) || !strings.Contains(response.Body.String(), `"kind":"cancelled","step":"download"`) {
		t.Fatalf("\nwanted:\n200 failed cancelled at download\ngot:\n%d %s", response.Code, response.Body.String())
	}
	if got := nextUpdateEvent(t, events, "update.failed"); !strings.HasPrefix(got, `{"instance":"work","version":"18.10.2026","kind":"cancelled","step":"download","message":"the install was cancelled`) {
		t.Fatalf("\nwanted:\nupdate.failed cancelled\ngot:\n%s", got)
	}
	if got := readFile(t, target); got != "old build" {
		t.Fatalf("\nwanted:\nold build kept\ngot:\n%q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 {
		t.Fatalf("\nwanted:\nonly the installed build in its directory\ngot:\n%v %v", entries, err)
	}
	if again := requestUpdate(server, http.MethodDelete, "/update/install", ""); again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), `"error":"no_install"`) {
		t.Fatalf("\nwanted:\n409 no_install\ngot:\n%d %s", again.Code, again.Body.String())
	}
	if status := requestUpdate(server, http.MethodGet, "/service/status", ""); !strings.Contains(status.Body.String(), `"restart_required":false`) {
		t.Fatalf("\nwanted:\nrestart_required false\ngot:\n%s", status.Body.String())
	}
}

func TestUpdateReinstallWithoutConfirmIsRefused(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	target := installedBuildFile(t)
	server := newUpdateServer(t, t.TempDir(), target, "work", github)

	for _, body := range []string{"", "{}", `{"confirm":false}`} {
		response := requestUpdate(server, http.MethodPost, "/update/reinstall", body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"confirmation_required"`) {
			t.Fatalf("\nwanted:\n400 confirmation_required for %q\ngot:\n%d %s", body, response.Code, response.Body.String())
		}
	}
	if got := github.downloadCount(); got != 0 {
		t.Fatalf("\nwanted:\nno download\ngot:\n%d", got)
	}
	if got := readFile(t, target); got != "old build" {
		t.Fatalf("\nwanted:\nold build kept\ngot:\n%q", got)
	}
}

func TestUpdateReinstallInstallsAnOlderBuild(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("20.09.2026", "old release 20.09.2026")
	target := installedBuildFile(t)
	server := newUpdateServer(t, t.TempDir(), target, "work", github)
	events := server.events.subscribe()

	if install := requestUpdate(server, http.MethodPost, "/update/install", ""); install.Code != http.StatusConflict || !strings.Contains(install.Body.String(), `"error":"no_update"`) {
		t.Fatalf("\nwanted:\n409 no_update\ngot:\n%d %s", install.Code, install.Body.String())
	}
	response := requestUpdate(server, http.MethodPost, "/update/reinstall", `{"confirm":true}`)

	if response.Code != http.StatusAccepted {
		t.Fatalf("\nwanted:\n202\ngot:\n%d %s", response.Code, response.Body.String())
	}
	nextUpdateEvent(t, events, "update.installed")
	if got := readFile(t, target); got != "old release 20.09.2026" {
		t.Fatalf("\nwanted:\nolder build installed\ngot:\n%q", got)
	}
}

func TestUpdateAvailableIsAnnouncedOncePerBuild(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	server := newUpdateServer(t, t.TempDir(), installedBuildFile(t), "work", github)
	events := server.events.subscribe()

	requestUpdate(server, http.MethodPost, "/update/check", "")
	requestUpdate(server, http.MethodPost, "/update/check", "")
	github.publish("19.10.2026", "new build 19.10.2026")
	requestUpdate(server, http.MethodPost, "/update/check", "")

	assertChromeEvent(t, events, "update.available", `{"instance":"work","version":"18.10.2026","channel":"release","published":"2026-10-18T06:00:00Z","notes":"notes for 18.10.2026","page":"https://github.com/tfkr-ae/marasi/releases/tag/18.10.2026"}`)
	assertChromeEvent(t, events, "update.available", `{"instance":"work","version":"19.10.2026","channel":"release","published":"2026-10-18T06:00:00Z","notes":"notes for 19.10.2026","page":"https://github.com/tfkr-ae/marasi/releases/tag/19.10.2026"}`)
	assertNoChromeEvent(t, events)
}

func TestUpdateStatusReportsTheInstalledBuildAndTheAvailableUpdate(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	target := installedBuildFile(t)
	server := newUpdateServer(t, t.TempDir(), target, "work", github)

	before := requestUpdate(server, http.MethodGet, "/update", "")
	check := requestUpdate(server, http.MethodPost, "/update/check", "")
	after := requestUpdate(server, http.MethodGet, "/update", "")

	assertChromeResponse(t, before, http.StatusOK, fmt.Sprintf(`{"installed":{"version":"01.10.2026","channel":"release"},"development":false,"channel":"release","auto_check":true,"last_check":null,"available":null,"install":{"state":"idle"},"restart_required":false,"install_path":%q}`+"\n", target))
	var body struct {
		LastCheck struct {
			At     time.Time       `json:"at"`
			Latest json.RawMessage `json:"latest"`
			Error  *string         `json:"error"`
		} `json:"last_check"`
		Available json.RawMessage `json:"available"`
	}
	if err := json.Unmarshal(after.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	build := `{"version":"18.10.2026","channel":"release","published":"2026-10-18T06:00:00Z","notes":"notes for 18.10.2026","page":"https://github.com/tfkr-ae/marasi/releases/tag/18.10.2026"}`
	if check.Body.String() != after.Body.String() || body.LastCheck.At.IsZero() || string(body.LastCheck.Latest) != build || body.LastCheck.Error != nil || string(body.Available) != build {
		t.Fatalf("\nwanted:\ncheck and status reporting %s\ngot:\n%s\n%s", build, check.Body.String(), after.Body.String())
	}
}

func TestUpdateRoutesRefuseADevelopmentBuild(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "marasi_config.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	updates, err := NewUpdates(UpdatesConfig{ConfigDir: configDir, Version: "18.10.2026 (dev)", Executable: installedBuildFile(t)})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(nil, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil, func() {}, "18.10.2026 (dev)", "work", "test-project")
	server.SetUpdates(updates)
	defer server.Close()

	for _, route := range [][2]string{{http.MethodPost, "/update/check"}, {http.MethodPost, "/update/install"}, {http.MethodPut, "/update/channel"}} {
		body := ""
		if route[0] == http.MethodPut {
			body = `{"channel":"nightly"}`
		}
		response := requestUpdate(server, route[0], route[1], body)
		assertChromeResponse(t, response, http.StatusConflict, `{"error":"development_build","message":"a development build does not check for or install updates"}`+"\n")
	}
}

func TestUpdateInstalledByAnotherInstanceIsNotOfferedAgain(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	configDir := t.TempDir()
	target := installedBuildFile(t)
	holder := newUpdateServer(t, configDir, target, "work", github)
	other := newUpdateServer(t, configDir, target, "scratch", github)
	otherEvents := other.events.subscribe()
	holderEvents := holder.events.subscribe()
	if response := requestUpdate(holder, http.MethodPost, "/update/install", ""); response.Code != http.StatusAccepted {
		t.Fatalf("\nwanted:\n202\ngot:\n%d %s", response.Code, response.Body.String())
	}
	nextUpdateEvent(t, holderEvents, "update.installed")

	check := requestUpdate(other, http.MethodPost, "/update/check", "")
	install := requestUpdate(other, http.MethodPost, "/update/install", "")

	if !strings.Contains(check.Body.String(), `"available":null`) || !strings.Contains(check.Body.String(), `"restart_required":true`) {
		t.Fatalf("\nwanted:\nno update offered, restart required\ngot:\n%s", check.Body.String())
	}
	assertNoChromeEvent(t, otherEvents)
	if install.Code != http.StatusConflict || !strings.Contains(install.Body.String(), `"error":"already_installed"`) {
		t.Fatalf("\nwanted:\n409 already_installed\ngot:\n%d %s", install.Code, install.Body.String())
	}
	if got := github.downloadCount(); got != 1 {
		t.Fatalf("\nwanted:\n1 build download\ngot:\n%d", got)
	}
	if reinstall := requestUpdate(other, http.MethodPost, "/update/reinstall", `{"confirm":true}`); reinstall.Code != http.StatusAccepted {
		t.Fatalf("\nwanted:\n202 reinstall\ngot:\n%d %s", reinstall.Code, reinstall.Body.String())
	}
}

func TestUpdateInstallIsRefusedOnceTheInstanceStops(t *testing.T) {
	github := newInstallGitHub(t)
	github.publish("18.10.2026", "new build 18.10.2026")
	target := installedBuildFile(t)
	server := newUpdateServer(t, t.TempDir(), target, "work", github)
	server.Close()

	response := requestUpdate(server, http.MethodPost, "/update/install", "")

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"error":"service_stopping"`) {
		t.Fatalf("\nwanted:\n503 service_stopping\ngot:\n%d %s", response.Code, response.Body.String())
	}
	if got := github.downloadCount(); got != 0 {
		t.Fatalf("\nwanted:\nno download\ngot:\n%d", got)
	}
}
