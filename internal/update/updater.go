package update

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"aead.dev/minisign"
)

// DefaultBaseURL is the GitHub releases API of the Marasi repository.
const DefaultBaseURL = "https://api.github.com/repos/tfkr-ae/marasi"

// Config is what an Updater needs from the build and its caller.
type Config struct {
	// Version is the installed build's version string (main.version).
	Version string
	// PublicKey is the embedded minisign public key (main.signingPublicKey):
	// the base64 key line, optionally preceded by its untrusted comment line.
	PublicKey string
	// Published is when the installed build was published, if the caller
	// knows (for example, recorded when it installed the build). Zero means
	// unknown. Only the nightly ordering reads it (IsUpdate).
	Published time.Time
	// BaseURL is the repository's GitHub API base; DefaultBaseURL when empty.
	BaseURL string
	// Client makes every request; NewHTTPClient() when nil.
	Client *http.Client
	// GOOS and GOARCH pick the release asset and the replacement strategy;
	// runtime.GOOS and runtime.GOARCH when empty.
	GOOS, GOARCH string
	// Team reads the Apple team that signed an executable. When nil it is
	// Codesign{} on darwin and unused elsewhere.
	Team TeamReader
}

// Updater finds, verifies and installs updates for one installed build. It
// holds no state between calls and is safe for concurrent use.
type Updater struct {
	installed Build
	key       minisign.PublicKey
	baseURL   string
	client    *http.Client
	goos      string
	goarch    string
	team      TeamReader
}

// New returns an Updater for the build cfg describes. It returns ErrDevBuild
// for a development build or one with no embedded key.
func New(cfg Config) (*Updater, error) {
	installed, err := ParseBuild(cfg.Version)
	if err != nil {
		return nil, err
	}
	if cfg.PublicKey == "" {
		return nil, ErrDevBuild
	}
	var key minisign.PublicKey
	if err := key.UnmarshalText([]byte(cfg.PublicKey)); err != nil {
		return nil, fmt.Errorf("embedded signing key: %w", err)
	}
	installed.Published = cfg.Published
	u := &Updater{
		installed: installed,
		key:       key,
		baseURL:   cfg.BaseURL,
		client:    cfg.Client,
		goos:      cfg.GOOS,
		goarch:    cfg.GOARCH,
		team:      cfg.Team,
	}
	if u.baseURL == "" {
		u.baseURL = DefaultBaseURL
	}
	if u.client == nil {
		u.client = NewHTTPClient()
	}
	if u.goos == "" {
		u.goos = runtime.GOOS
	}
	if u.goarch == "" {
		u.goarch = runtime.GOARCH
	}
	if u.team == nil && u.goos == "darwin" {
		u.team = Codesign{}
	}
	return u, nil
}

// Installed is the installed build.
func (u *Updater) Installed() Build { return u.installed }

// AssetName is the release asset this platform installs, such as
// "marasi-linux-amd64" or "marasi-windows-amd64.exe".
func (u *Updater) AssetName() string {
	name := "marasi-" + u.goos + "-" + u.goarch
	if u.goos == "windows" {
		name += ".exe"
	}
	return name
}

// Latest returns the latest build on channel, even when it is older than
// the installed build (a reinstall).
func (u *Updater) Latest(ctx context.Context, channel Channel) (Candidate, error) {
	switch channel {
	case Release:
		return u.fetchRelease(ctx, "/releases/latest", Release)
	case Nightly:
		return u.fetchRelease(ctx, "/releases/tags/nightly", Nightly)
	}
	return Candidate{}, fmt.Errorf("channel %q is not release or nightly", channel)
}

// Check returns the update on channel, or nil when the installed build is
// the newest. An unsigned build is never offered (ADR-0048).
func (u *Updater) Check(ctx context.Context, channel Channel) (*Candidate, error) {
	rel, err := u.Latest(ctx, channel)
	if err != nil {
		return nil, err
	}
	if !rel.Signed() || !IsUpdate(channel, u.installed, rel.Build) {
		return nil, nil
	}
	return &rel, nil
}
