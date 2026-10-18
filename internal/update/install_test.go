package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// installFixture is an installed build in a temp dir and a newer release
// on a fake GitHub.
type installFixture struct {
	github *fakeGitHub
	key    signingKey
	dir    string
	target string
}

func newInstallFixture(t *testing.T, goos, executable string, builds map[string][]byte) *installFixture {
	t.Helper()
	f := &installFixture{github: newFakeGitHub(t), key: newSigningKey(t), dir: t.TempDir()}
	f.target = filepath.Join(f.dir, executable)
	if err := os.WriteFile(f.target, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.github.setLatest(&fakeRelease{
		tag:       "19.10.2026",
		name:      "19.10.2026",
		published: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC),
		assets:    signedAssets(f.key, builds),
	})
	return f
}

func (f *installFixture) updater(t *testing.T, cfg Config) *Updater {
	t.Helper()
	cfg.Version = "18.10.2026"
	if cfg.PublicKey == "" {
		cfg.PublicKey = f.key.public
	}
	cfg.BaseURL = f.github.baseURL()
	if cfg.GOOS == "" {
		cfg.GOOS = "linux"
	}
	cfg.GOARCH = "amd64"
	u, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (f *installFixture) candidate(t *testing.T, u *Updater) Candidate {
	t.Helper()
	c, err := u.Latest(context.Background(), Release)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// assertUnchanged checks the installed build is the old one and nothing
// else was left in its directory.
func (f *installFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	got, err := os.ReadFile(f.target)
	if err != nil || string(got) != "old build" {
		t.Errorf("installed build = %q, %v; want it unchanged", got, err)
	}
	f.assertDirHolds(t, filepath.Base(f.target))
}

func (f *installFixture) assertDirHolds(t *testing.T, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(got)
	slices.Sort(names)
	if !slices.Equal(got, names) {
		t.Errorf("install dir holds %v, want %v", got, names)
	}
}

func assertFailure(t *testing.T, err error, kind Kind, step Step) *Error {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("Install error = %v, want an *update.Error", err)
	}
	if failure.Kind != kind || failure.Step != step {
		t.Fatalf("Install failed with %s at %s (%v), want %s at %s", failure.Kind, failure.Step, failure, kind, step)
	}
	return failure
}

func TestInstallSwapsInVerifiedBuild(t *testing.T) {
	f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-linux-amd64": []byte("new build")})
	u := f.updater(t, Config{})

	var steps []Step
	var last Progress
	err := u.Install(context.Background(), f.candidate(t, u), f.target, func(p Progress) {
		if len(steps) == 0 || steps[len(steps)-1] != p.Step {
			steps = append(steps, p.Step)
		}
		last = p
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, _ := os.ReadFile(f.target)
	if string(got) != "new build" {
		t.Errorf("installed build = %q, want the new build", got)
	}
	info, _ := os.Stat(f.target)
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Errorf("installed build mode = %v, want executable", info.Mode())
	}
	if !slices.Equal(steps, []Step{StepDownload, StepVerify, StepInstall}) {
		t.Errorf("steps = %v", steps)
	}
	if last.Received != int64(len("new build")) || last.Total != int64(len("new build")) {
		t.Errorf("final progress = %+v", last)
	}
	f.assertDirHolds(t, "marasi")
}

func TestInstallRejectsTamperedChecksum(t *testing.T) {
	f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-linux-amd64": []byte("new build")})
	f.github.latest.assets["marasi-linux-amd64"] = []byte("evil build")
	u := f.updater(t, Config{})

	err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
	assertFailure(t, err, KindChecksum, StepVerify)
	f.assertUnchanged(t)
}

func TestInstallRejectsWrongKeyBeforeFetchingBuild(t *testing.T) {
	f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-linux-amd64": []byte("new build")})
	other := newSigningKey(t)
	u := f.updater(t, Config{PublicKey: other.public})

	err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
	assertFailure(t, err, KindSignature, StepVerify)
	f.assertUnchanged(t)
	if slices.Contains(f.github.downloaded(), "marasi-linux-amd64") {
		t.Error("the build was downloaded although its checksums' signature failed")
	}
}

// fakeTeams maps a file's content to the team that signed it.
type fakeTeams map[string]string

func (teams fakeTeams) TeamID(_ context.Context, path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return teams[string(content)], nil
}

func TestInstallRejectsWrongTeamOnMacOS(t *testing.T) {
	f := newInstallFixture(t, "darwin", "marasi", map[string][]byte{"marasi-darwin-amd64": []byte("new build")})
	u := f.updater(t, Config{GOOS: "darwin", Team: fakeTeams{"old build": "TEAM123", "new build": "OTHER99"}})

	err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
	assertFailure(t, err, KindTeam, StepVerify)
	f.assertUnchanged(t)
}

func TestInstallRejectsUnsignedBuildOnMacOS(t *testing.T) {
	f := newInstallFixture(t, "darwin", "marasi", map[string][]byte{"marasi-darwin-amd64": []byte("new build")})
	u := f.updater(t, Config{GOOS: "darwin", Team: fakeTeams{"old build": "TEAM123"}})

	err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
	assertFailure(t, err, KindTeam, StepVerify)
	f.assertUnchanged(t)
}

func TestInstallAcceptsSameTeamOnMacOS(t *testing.T) {
	f := newInstallFixture(t, "darwin", "marasi", map[string][]byte{"marasi-darwin-amd64": []byte("new build")})
	u := f.updater(t, Config{GOOS: "darwin", Team: fakeTeams{"old build": "TEAM123", "new build": "TEAM123"}})

	if err := u.Install(context.Background(), f.candidate(t, u), f.target, nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, _ := os.ReadFile(f.target); string(got) != "new build" {
		t.Errorf("installed build = %q", got)
	}
}

func TestInstallRefusesUnwritableDirBeforeDownloading(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not stop writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write to any directory")
	}
	f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-linux-amd64": []byte("new build")})
	if err := os.Chmod(f.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f.dir, 0o755) })
	u := f.updater(t, Config{})

	if err := CheckWritable(f.target); err == nil {
		t.Error("CheckWritable passed for a read-only directory")
	}
	err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
	failure := assertFailure(t, err, KindPermissions, StepInstall)
	if failure.Page != "https://github.com/tfkr-ae/marasi/releases/tag/19.10.2026" {
		t.Errorf("failure page = %q, want the release page", failure.Page)
	}
	if got := f.github.downloaded(); len(got) != 0 {
		t.Errorf("downloaded %v before refusing", got)
	}
	f.assertUnchanged(t)
}

func TestInstallNoBuildForPlatform(t *testing.T) {
	f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-darwin-arm64": []byte("new build")})
	u := f.updater(t, Config{})

	err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
	failure := assertFailure(t, err, KindNoBuild, StepDownload)
	if failure.Error() != "no build for this platform (marasi-linux-amd64)" {
		t.Errorf("message = %q", failure.Error())
	}
	if got := f.github.downloaded(); len(got) != 0 {
		t.Errorf("downloaded %v", got)
	}
	f.assertUnchanged(t)
}

func TestInstallCancelBeforeSwap(t *testing.T) {
	for _, step := range []Step{StepDownload, StepVerify} {
		t.Run(string(step), func(t *testing.T) {
			f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-linux-amd64": []byte("new build")})
			u := f.updater(t, Config{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := u.Install(ctx, f.candidate(t, u), f.target, func(p Progress) {
				if p.Step == step {
					cancel()
				}
			})
			assertFailure(t, err, KindCancelled, step)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error %v does not wrap context.Canceled", err)
			}
			f.assertUnchanged(t)
		})
	}
}

func TestInstallCancelOnceSwapStartedIsIgnored(t *testing.T) {
	f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-linux-amd64": []byte("new build")})
	u := f.updater(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := u.Install(ctx, f.candidate(t, u), f.target, func(p Progress) {
		if p.Step == StepInstall {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, _ := os.ReadFile(f.target); string(got) != "new build" {
		t.Errorf("installed build = %q", got)
	}
}

func TestInstallWindowsRenamesRunningExecutableAside(t *testing.T) {
	f := newInstallFixture(t, "windows", "marasi.exe", map[string][]byte{"marasi-windows-amd64.exe": []byte("new build")})
	u := f.updater(t, Config{GOOS: "windows"})

	if err := u.Install(context.Background(), f.candidate(t, u), f.target, nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, _ := os.ReadFile(f.target); string(got) != "new build" {
		t.Errorf("installed build = %q", got)
	}
	asides, _ := filepath.Glob(filepath.Join(f.dir, ".marasi.exe.*.old"))
	if len(asides) != 1 {
		t.Fatalf("aside files = %v, want one", asides)
	}
	if got, _ := os.ReadFile(asides[0]); string(got) != "old build" {
		t.Errorf("aside holds %q, want the old build", got)
	}

	if err := CleanupAside(f.target); err != nil {
		t.Fatalf("CleanupAside: %v", err)
	}
	f.assertDirHolds(t, "marasi.exe")
}

func TestInstallFollowsSymlinkToInstalledBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newInstallFixture(t, "linux", "marasi", map[string][]byte{"marasi-linux-amd64": []byte("new build")})
	link := filepath.Join(t.TempDir(), "marasi")
	if err := os.Symlink(f.target, link); err != nil {
		t.Fatal(err)
	}
	u := f.updater(t, Config{})

	if err := u.Install(context.Background(), f.candidate(t, u), link, nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, _ := os.ReadFile(f.target); string(got) != "new build" {
		t.Errorf("installed build = %q", got)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v %v", info, err)
	}
}
