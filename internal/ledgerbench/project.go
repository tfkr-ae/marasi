package ledgerbench

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// Environment is what a benchmark run records about where it ran, so
// baselines from different runs can be compared.
type Environment struct {
	Hardware      string
	CPUs          int
	MemoryBytes   int64
	OS            string
	GoVersion     string
	ModerncSQLite string
	// Settings of a connection opened as the Repository opens one.
	PageSize  int64
	CacheSize int64
	MmapSize  int64
	// WALBytes is the size of the project's WAL file before the run.
	WALBytes int64
	// Jobs is the saved state of each background job: complete or
	// incomplete, with its progress once it has counted its work.
	Jobs map[domain.JobName]string
}

// ProbeEnvironment records the environment of a run against the project at
// path. It opens the project once, as the Repository does.
func ProbeEnvironment(path string) (Environment, error) {
	env := Environment{
		CPUs:      runtime.NumCPU(),
		OS:        runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion: runtime.Version(),
		Hardware:  runtime.GOARCH,
		Jobs:      map[domain.JobName]string{},
	}
	if info, err := os.Stat(path + "-wal"); err == nil {
		env.WALBytes = info.Size()
	}
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		env.OS = strings.TrimSpace(string(out)) + " " + runtime.GOARCH
	}
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			env.Hardware = strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			fmt.Sscan(string(out), &env.MemoryBytes)
		}
	case "linux":
		if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if name, ok := strings.CutPrefix(line, "model name"); ok {
					env.Hardware = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), ":"))
					break
				}
			}
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "modernc.org/sqlite" {
				env.ModerncSQLite = dep.Version
			}
		}
	}
	if env.ModerncSQLite == "" {
		// Test binaries carry no dependency versions; ask the module instead.
		if out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "modernc.org/sqlite").Output(); err == nil {
			env.ModerncSQLite = strings.TrimSpace(string(out))
		}
	}

	conn, err := db.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return env, err
	}
	repo := db.NewProxyRepo(conn)
	defer repo.Close()
	for pragma, into := range map[string]*int64{"page_size": &env.PageSize, "cache_size": &env.CacheSize, "mmap_size": &env.MmapSize} {
		if err := conn.Get(into, "PRAGMA "+pragma); err != nil {
			return env, fmt.Errorf("reading %s: %w", pragma, err)
		}
	}
	jobs, err := repo.ListJobs()
	if err != nil {
		return env, err
	}
	for _, job := range jobs {
		state := "incomplete"
		if job.Complete {
			state = "complete"
		}
		if job.Total > 0 {
			state += fmt.Sprintf(" (%d/%d)", job.Done, job.Total)
		}
		env.Jobs[job.Name] = state
	}
	return env, nil
}

// String prints the environment one field per line.
func (env Environment) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "hardware: %s, %d CPUs, %d GiB\n", env.Hardware, env.CPUs, env.MemoryBytes>>30)
	fmt.Fprintf(&b, "os: %s\n", env.OS)
	fmt.Fprintf(&b, "go: %s\n", env.GoVersion)
	fmt.Fprintf(&b, "modernc.org/sqlite: %s\n", env.ModerncSQLite)
	fmt.Fprintf(&b, "page_size: %d\ncache_size: %d\nmmap_size: %d\n", env.PageSize, env.CacheSize, env.MmapSize)
	fmt.Fprintf(&b, "wal_bytes: %d\n", env.WALBytes)
	jobs := make([]domain.JobName, 0, len(env.Jobs))
	for job := range env.Jobs {
		jobs = append(jobs, job)
	}
	slices.Sort(jobs)
	for _, job := range jobs {
		fmt.Fprintf(&b, "job %s: %s\n", job, env.Jobs[job])
	}
	return b.String()
}

// CloneProject copies the project at src into dir and returns the copy's
// path, for cases that write. On macOS the copy is an APFS clone, which takes
// no time and no space until a page is written.
func CloneProject(src, dir string) (string, error) {
	dst := filepath.Join(dir, filepath.Base(src))
	for _, suffix := range []string{"", "-wal"} {
		if _, err := os.Stat(src + suffix); err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if err := copyFile(src+suffix, dst+suffix); err != nil {
			return "", fmt.Errorf("cloning %s: %w", src+suffix, err)
		}
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	if runtime.GOOS == "darwin" {
		if err := exec.Command("cp", "-c", src, dst).Run(); err == nil {
			return nil
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
