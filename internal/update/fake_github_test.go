package update

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aead.dev/minisign"
)

// signingKey is a throwaway minisign key pair made for one test.
type signingKey struct {
	public  string
	private minisign.PrivateKey
}

func newSigningKey(t *testing.T) signingKey {
	t.Helper()
	public, private, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := public.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{public: string(text), private: private}
}

// fakeRelease is one release the fake GitHub serves.
type fakeRelease struct {
	tag       string
	name      string
	body      string
	published time.Time
	assets    map[string][]byte
}

// fakeGitHub serves the GitHub releases API for one repository and the
// release assets, and records every asset download.
type fakeGitHub struct {
	server *httptest.Server

	mu        sync.Mutex
	latest    *fakeRelease
	nightly   *fakeRelease
	downloads []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	return g
}

// baseURL is the repository's API base, as Config.BaseURL takes it.
func (g *fakeGitHub) baseURL() string { return g.server.URL + "/repos/tfkr-ae/marasi" }

func (g *fakeGitHub) setLatest(r *fakeRelease) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.latest = r
}

func (g *fakeGitHub) setNightly(r *fakeRelease) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nightly = r
}

// downloaded returns the asset names downloaded so far.
func (g *fakeGitHub) downloaded() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.downloads...)
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.URL.Path == "/repos/tfkr-ae/marasi/releases/latest":
		g.writeRelease(w, g.latest)
	case r.URL.Path == "/repos/tfkr-ae/marasi/releases/tags/nightly":
		g.writeRelease(w, g.nightly)
	case strings.HasPrefix(r.URL.Path, "/download/"):
		tag, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/download/"), "/")
		for _, release := range []*fakeRelease{g.latest, g.nightly} {
			if release != nil && release.tag == tag {
				if content, ok := release.assets[name]; ok {
					g.downloads = append(g.downloads, name)
					w.Header().Set("Content-Length", fmt.Sprint(len(content)))
					w.Write(content)
					return
				}
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeGitHub) writeRelease(w http.ResponseWriter, release *fakeRelease) {
	if release == nil {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	type asset struct {
		Name string `json:"name"`
		Size int    `json:"size"`
		URL  string `json:"browser_download_url"`
	}
	body := struct {
		Tag       string  `json:"tag_name"`
		Name      string  `json:"name"`
		Body      string  `json:"body"`
		Page      string  `json:"html_url"`
		Published string  `json:"published_at"`
		Assets    []asset `json:"assets"`
	}{
		Tag:       release.tag,
		Name:      release.name,
		Body:      release.body,
		Page:      "https://github.com/tfkr-ae/marasi/releases/tag/" + release.tag,
		Published: release.published.UTC().Format(time.RFC3339),
		Assets:    []asset{},
	}
	for name, content := range release.assets {
		body.Assets = append(body.Assets, asset{Name: name, Size: len(content), URL: g.server.URL + "/download/" + release.tag + "/" + name})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

// signedAssets returns builds plus a checksums.txt listing them and its
// minisign signature made with key.
func signedAssets(key signingKey, builds map[string][]byte) map[string][]byte {
	assets := map[string][]byte{}
	var checksums strings.Builder
	for name, content := range builds {
		sum := sha256.Sum256(content)
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		assets[name] = content
	}
	assets["checksums.txt"] = []byte(checksums.String())
	assets["checksums.txt.minisig"] = minisign.Sign(key.private, assets["checksums.txt"])
	return assets
}
