package update

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func newTestUpdater(t *testing.T, g *fakeGitHub, key signingKey, version string) *Updater {
	t.Helper()
	u, err := New(Config{
		Version:   version,
		PublicKey: key.public,
		BaseURL:   g.baseURL(),
		GOOS:      "linux",
		GOARCH:    "amd64",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return u
}

func TestNewRefusesDevBuilds(t *testing.T) {
	key := newSigningKey(t)
	if _, err := New(Config{Version: "18.10.2026 (dev)", PublicKey: key.public}); !errors.Is(err, ErrDevBuild) {
		t.Errorf("dev version: err = %v, want ErrDevBuild", err)
	}
	if _, err := New(Config{Version: "18.10.2026", PublicKey: ""}); !errors.Is(err, ErrDevBuild) {
		t.Errorf("no key: err = %v, want ErrDevBuild", err)
	}
	if _, err := New(Config{Version: "18.10.2026", PublicKey: "not a key"}); err == nil || errors.Is(err, ErrDevBuild) {
		t.Errorf("bad key: err = %v, want an invalid-key error", err)
	}
}

func TestCheckFindsNewerRelease(t *testing.T) {
	key := newSigningKey(t)
	g := newFakeGitHub(t)
	g.setLatest(&fakeRelease{
		tag:       "19.10.2026",
		name:      "19.10.2026",
		body:      "## What's Changed\n* feat: things",
		published: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC),
		assets:    signedAssets(key, map[string][]byte{"marasi-linux-amd64": []byte("new")}),
	})
	u := newTestUpdater(t, g, key, "18.10.2026")

	rel, err := u.Check(context.Background(), Release)
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil {
		t.Fatal("Check found no update, want 19.10.2026")
	}
	if rel.Build.Version != "19.10.2026" || rel.Channel != Release {
		t.Errorf("update = %s on %s", rel.Build.Version, rel.Channel)
	}
	if rel.Notes != "## What's Changed\n* feat: things" {
		t.Errorf("notes = %q", rel.Notes)
	}
	if rel.Page != "https://github.com/tfkr-ae/marasi/releases/tag/19.10.2026" {
		t.Errorf("page = %q", rel.Page)
	}
}

func TestCheckUpToDate(t *testing.T) {
	key := newSigningKey(t)
	g := newFakeGitHub(t)
	g.setLatest(&fakeRelease{tag: "18.10.2026", name: "18.10.2026", assets: signedAssets(key, nil)})
	u := newTestUpdater(t, g, key, "18.10.2026")

	rel, err := u.Check(context.Background(), Release)
	if err != nil || rel != nil {
		t.Fatalf("Check = %v, %v; want no update", rel, err)
	}
}

func TestCheckNeverOffersUnsignedRelease(t *testing.T) {
	key := newSigningKey(t)
	g := newFakeGitHub(t)
	assets := signedAssets(key, map[string][]byte{"marasi-linux-amd64": []byte("new")})
	delete(assets, "checksums.txt.minisig")
	g.setLatest(&fakeRelease{tag: "19.10.2026", name: "19.10.2026", assets: assets})
	u := newTestUpdater(t, g, key, "18.10.2026")

	rel, err := u.Check(context.Background(), Release)
	if err != nil || rel != nil {
		t.Fatalf("Check = %v, %v; want no update for an unsigned release", rel, err)
	}
}

func TestCheckSameDateReleaseVsNightly(t *testing.T) {
	key := newSigningKey(t)
	g := newFakeGitHub(t)
	g.setLatest(&fakeRelease{tag: "18.10.2026", name: "18.10.2026", assets: signedAssets(key, nil)})
	g.setNightly(&fakeRelease{
		tag:       "nightly",
		name:      "18.10.2026 (nightly bbbbbbb)",
		published: time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC),
		assets:    signedAssets(key, nil),
	})

	fromNightly := newTestUpdater(t, g, key, "18.10.2026 (nightly aaaaaaa)")
	if rel, err := fromNightly.Check(context.Background(), Release); err != nil || rel != nil {
		t.Errorf("nightly switched to release: Check = %v, %v; want no update", rel, err)
	}
	fromRelease := newTestUpdater(t, g, key, "18.10.2026")
	if rel, err := fromRelease.Check(context.Background(), Nightly); err != nil || rel != nil {
		t.Errorf("release switched to nightly: Check = %v, %v; want no update", rel, err)
	}
}

func TestCheckNightlyReadsVersionPublishTimeAndNotes(t *testing.T) {
	key := newSigningKey(t)
	g := newFakeGitHub(t)
	published := time.Date(2026, 10, 19, 2, 5, 0, 0, time.UTC)
	g.setNightly(&fakeRelease{
		tag:       "nightly",
		name:      "19.10.2026 (nightly bbbbbbb)",
		body:      "- feat(update): add package\n- fix: typo\n\nRef: main\nCommit: bbbbbbbcafe\n",
		published: published,
		assets:    signedAssets(key, nil),
	})
	u := newTestUpdater(t, g, key, "18.10.2026 (nightly aaaaaaa)")

	rel, err := u.Check(context.Background(), Nightly)
	if err != nil || rel == nil {
		t.Fatalf("Check = %v, %v; want the 19.10 nightly", rel, err)
	}
	if !rel.Build.Nightly || rel.Build.Commit != "bbbbbbb" || !rel.Build.Published.Equal(published) {
		t.Errorf("build = %+v", rel.Build)
	}
	if rel.Notes != "- feat(update): add package\n- fix: typo" {
		t.Errorf("notes = %q, want commit subjects without Ref/Commit", rel.Notes)
	}
}

func TestCheckNoNightlyPublished(t *testing.T) {
	key := newSigningKey(t)
	g := newFakeGitHub(t)
	u := newTestUpdater(t, g, key, "18.10.2026 (nightly aaaaaaa)")
	if _, err := u.Check(context.Background(), Nightly); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("err = %v, want ErrNotPublished", err)
	}
}

func TestLatestReturnsOlderBuildForReinstall(t *testing.T) {
	key := newSigningKey(t)
	g := newFakeGitHub(t)
	g.setLatest(&fakeRelease{tag: "17.10.2026", name: "17.10.2026", assets: signedAssets(key, nil)})
	u := newTestUpdater(t, g, key, "18.10.2026 (nightly aaaaaaa)")

	rel, err := u.Latest(context.Background(), Release)
	if err != nil || rel.Build.Version != "17.10.2026" {
		t.Fatalf("Latest = %+v, %v", rel, err)
	}
}

func TestHTTPClientHonoursProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test:3128")
	t.Setenv("https_proxy", "http://proxy.example.test:3128")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	transport := NewHTTPClient().Transport.(*http.Transport)
	target, _ := url.Parse("https://api.github.com/repos/tfkr-ae/marasi/releases/latest")
	got, err := transport.Proxy(&http.Request{URL: target})
	if err != nil || got == nil || got.String() != "http://proxy.example.test:3128" {
		t.Fatalf("Proxy() = %v, %v; want the HTTPS_PROXY address", got, err)
	}
}
