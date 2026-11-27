package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// serviceStartTimeout bounds how long a test waits for an in-process service
// to report its proxy listener. Startup creates the certificate authority and
// migrates SQLite, which under -race and alongside parallel tests can take
// several seconds; the bound only decides how long a broken start takes to fail.
const serviceStartTimeout = 30 * time.Second

// testBuilds holds the marasi binaries this test process has built. Each build
// runs go build once per process, not once per test: the package has well over
// a hundred tests that run the command, and building for each of them pushed
// the package past go test's default timeout.
var testBuilds struct {
	dir string

	mu       sync.Mutex
	versions map[string]*testBuild
}

// testBuild is one go build, run at most once and shared by every test that
// asks for the same version.
type testBuild struct {
	once   sync.Once
	path   string
	output []byte
	err    error
}

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests returns m.Run's exit code so the shared build directory is removed
// before TestMain exits; os.Exit skips deferred calls.
func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "marasi-test-build-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the test build directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	testBuilds.dir = dir
	return m.Run()
}

// sharedBuild returns the path of the marasi command built with version v, or
// with the default version when v is empty. The file is shared, so a caller
// must not move, replace or remove it.
func sharedBuild(t *testing.T, v string) string {
	t.Helper()
	testBuilds.mu.Lock()
	if testBuilds.versions == nil {
		testBuilds.versions = map[string]*testBuild{}
	}
	build, ok := testBuilds.versions[v]
	if !ok {
		build = &testBuild{}
		testBuilds.versions[v] = build
	}
	testBuilds.mu.Unlock()

	build.once.Do(func() {
		dir, err := os.MkdirTemp(testBuilds.dir, "build-")
		if err != nil {
			build.err = err
			return
		}
		build.path = filepath.Join(dir, "marasi")
		if runtime.GOOS == "windows" {
			build.path += ".exe"
		}
		args := []string{"build"}
		if v != "" {
			args = append(args, "-ldflags", "-X main.version="+v)
		}
		args = append(args, "-o", build.path, ".")
		command := exec.Command("go", args...)
		command.Env = append(os.Environ(), "GOWORK=off")
		build.output, build.err = command.CombinedOutput()
	})
	if build.err != nil {
		t.Fatalf("\nwanted:\nbuilt marasi command\ngot:\n%s\n%v", build.output, build.err)
	}
	return build.path
}

// copyBuild places the shared build at source at a fresh path under the test's
// temporary directory, for a test that renames over or replaces its binary.
//
// It prefers a hard link so the test process never opens a writable file
// descriptor to the executable: on Linux, a writable descriptor held while
// another parallel test forks a subprocess can stay open in that child until
// execve and cause exec of the copy to fail with ETXTBSY ("text file busy").
// Replacing the linked path with os.Rename swaps the directory entry without
// touching source.
func copyBuild(t *testing.T, source string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), filepath.Base(source))
	if err := os.Link(source, binary); err == nil {
		return binary
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("\nwanted:\nopened shared build\ngot:\n%v", err)
	}
	defer in.Close()
	out, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatalf("\nwanted:\ncreated build copy\ngot:\n%v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("\nwanted:\ncopied shared build\ngot:\n%v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("\nwanted:\nclosed build copy\ngot:\n%v", err)
	}
	return binary
}

// writeExecutable writes a Unix shell script at path from a child process so
// the test process never holds a writable descriptor to it across a parallel
// test's fork/exec, avoiding Linux ETXTBSY ("text file busy") failures.
func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	command := exec.Command("sh", "-c", `cat > "$1" && chmod 0755 "$1"`, "_", path)
	command.Stdin = strings.NewReader(content)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("\nwanted:\nwrote executable %s\ngot:\n%v\n%s", path, err, output)
	}
}

func TestCopyBuild(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "marasi")
	if err := os.WriteFile(source, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	copied := copyBuild(t, source)
	replacement := filepath.Join(t.TempDir(), "next")
	if err := os.WriteFile(replacement, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, copied); err != nil {
		t.Fatal(err)
	}
	gotSource, err := os.ReadFile(source)
	if err != nil || string(gotSource) != "old" {
		t.Fatalf("\nwanted:\nsource preserved as %q\ngot:\n%q (%v)", "old", gotSource, err)
	}
	gotCopied, err := os.ReadFile(copied)
	if err != nil || string(gotCopied) != "new" {
		t.Fatalf("\nwanted:\ncopy replaced with %q\ngot:\n%q (%v)", "new", gotCopied, err)
	}
}
