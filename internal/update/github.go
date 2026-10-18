package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// Release assets every signed release and nightly publishes beside the
// builds.
const (
	checksumsAsset = "checksums.txt"
	signatureAsset = "checksums.txt.minisig"
)

// maxMetadataSize bounds a release API response.
const maxMetadataSize = 4 << 20

// ErrNotPublished reports that GitHub has no build on the channel: no
// release yet, or no nightly while the nightly workflow republishes it.
var ErrNotPublished = errors.New("no build is published on this channel right now; try again in a few minutes")

// Candidate is the latest build published on a channel.
type Candidate struct {
	// Channel the build was found on.
	Channel Channel
	// Build is the parsed version, with Published set.
	Build Build
	// Notes are the release notes in Markdown. A nightly's are the commit
	// subjects since the previous nightly, without its Ref and Commit lines.
	Notes string
	// Page is the build's GitHub release page, where it can be installed by
	// hand.
	Page string
	// Assets are the files the release publishes.
	Assets []Asset
}

// Asset is one file a release publishes.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Asset returns the asset called name.
func (c Candidate) Asset(name string) (Asset, bool) {
	for _, a := range c.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Signed reports whether the release publishes checksums.txt and its
// minisign signature. Releases from before ADR-0048 are unsigned.
func (c Candidate) Signed() bool {
	_, sums := c.Asset(checksumsAsset)
	_, sig := c.Asset(signatureAsset)
	return sums && sig
}

// NewHTTPClient returns the client update requests use by default. It reads
// HTTP_PROXY, HTTPS_PROXY and NO_PROXY (and their lowercase forms) when it
// is made, as http.ProxyFromEnvironment does, and bounds connection setup
// and response headers but not a whole download.
func NewHTTPClient() *http.Client {
	proxy := httpproxy.FromEnvironment().ProxyFunc()
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 func(r *http.Request) (*url.URL, error) { return proxy(r.URL) },
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}

// githubRelease is the part of a GitHub release the updater reads.
type githubRelease struct {
	Tag       string    `json:"tag_name"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	Page      string    `json:"html_url"`
	Published time.Time `json:"published_at"`
	Assets    []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// fetchRelease reads the release at path under the base URL. A release's
// version is its tag; a nightly's is its title.
func (u *Updater) fetchRelease(ctx context.Context, path string, channel Channel) (Candidate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.baseURL+path, nil)
	if err != nil {
		return Candidate{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "marasi/"+u.installed.Version)
	resp, err := u.client.Do(req)
	if err != nil {
		return Candidate{}, fmt.Errorf("checking GitHub for the latest %s: %w", channel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Candidate{}, ErrNotPublished
	}
	if resp.StatusCode != http.StatusOK {
		return Candidate{}, fmt.Errorf("checking GitHub for the latest %s: %s", channel, resp.Status)
	}
	var gh githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataSize)).Decode(&gh); err != nil {
		return Candidate{}, fmt.Errorf("reading the latest %s from GitHub: %w", channel, err)
	}

	version, notes := gh.Tag, gh.Body
	if channel == Nightly {
		version, notes = gh.Name, nightlyNotes(gh.Body)
	}
	build, err := ParseBuild(version)
	if err != nil {
		return Candidate{}, fmt.Errorf("the latest %s on GitHub: %w", channel, err)
	}
	if build.Nightly != (channel == Nightly) {
		return Candidate{}, fmt.Errorf("the latest %s on GitHub has version %q", channel, version)
	}
	build.Published = gh.Published
	c := Candidate{Channel: channel, Build: build, Notes: notes, Page: gh.Page}
	for _, a := range gh.Assets {
		c.Assets = append(c.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
	}
	return c, nil
}

// nightlyNotes drops the trailing Ref, Branch and Commit lines the nightly
// workflow adds below the commit subjects.
func nightlyNotes(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	end := len(lines)
	for end > 0 {
		line := strings.TrimSpace(lines[end-1])
		if line == "" || strings.HasPrefix(line, "Ref:") || strings.HasPrefix(line, "Branch:") || strings.HasPrefix(line, "Commit:") {
			end--
			continue
		}
		break
	}
	return strings.Join(lines[:end], "\n")
}
