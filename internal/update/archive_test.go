package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type zipEntrySpec struct {
	name    string
	mode    fs.FileMode
	content []byte
}

func makeZip(t *testing.T, entries ...zipEntrySpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if len(e.content) > 0 {
			if _, err := w.Write(e.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tarEntrySpec struct {
	name     string
	typeflag byte
	linkname string
	content  []byte
}

func makeTarGz(t *testing.T, entries ...tarEntrySpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o755,
			Size:     int64(len(e.content)),
			Typeflag: typeflag,
			Linkname: e.linkname,
		}
		if typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg && len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallPrefersPlatformArchive(t *testing.T) {
	t.Run("should prefer tar.gz over bare executable on linux", func(t *testing.T) {
		archive := makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("new linux build")})
		f := newInstallFixture(t, "linux", "marasi", map[string][]byte{
			"marasi-linux-amd64":        []byte("bare linux build"),
			"marasi-linux-amd64.tar.gz": archive,
		})
		u := f.updater(t, Config{GOOS: "linux"})

		var steps []Step
		var last Progress
		err := u.Install(context.Background(), f.candidate(t, u), f.target, func(p Progress) {
			if len(steps) == 0 || steps[len(steps)-1] != p.Step {
				steps = append(steps, p.Step)
			}
			last = p
		})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got, err := os.ReadFile(f.target)
		if err != nil || string(got) != "new linux build" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q (%v)", "new linux build", got, err)
		}
		if info, err := os.Stat(f.target); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0) {
			t.Fatalf("\nwanted:\nexecutable mode\ngot:\n%v (%v)", info, err)
		}
		downloaded := f.github.downloaded()
		if !slices.Contains(downloaded, "marasi-linux-amd64.tar.gz") || slices.Contains(downloaded, "marasi-linux-amd64") {
			t.Fatalf("\nwanted:\nonly archive downloaded\ngot:\n%v", downloaded)
		}
		if !slices.Equal(steps, []Step{StepDownload, StepVerify, StepInstall}) {
			t.Fatalf("\nwanted:\n[download verify install]\ngot:\n%v", steps)
		}
		if last.Received != int64(len(archive)) || last.Total != int64(len(archive)) {
			t.Fatalf("\nwanted:\nprogress %d/%d\ngot:\n%+v", len(archive), len(archive), last)
		}
		f.assertDirHolds(t, "marasi")
	})

	t.Run("should prefer zip over bare executable on darwin and check Apple team on extracted binary", func(t *testing.T) {
		archive := makeZip(t, zipEntrySpec{name: "marasi", mode: 0o755, content: []byte("new darwin build")})
		f := newInstallFixture(t, "darwin", "marasi", map[string][]byte{
			"marasi-darwin-amd64":     []byte("bare darwin build"),
			"marasi-darwin-amd64.zip": archive,
		})
		u := f.updater(t, Config{
			GOOS: "darwin",
			Team: fakeTeams{"old build": "TEAM123", "new darwin build": "TEAM123"},
		})

		if err := u.Install(context.Background(), f.candidate(t, u), f.target, nil); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got, err := os.ReadFile(f.target)
		if err != nil || string(got) != "new darwin build" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q (%v)", "new darwin build", got, err)
		}
		downloaded := f.github.downloaded()
		if !slices.Contains(downloaded, "marasi-darwin-amd64.zip") || slices.Contains(downloaded, "marasi-darwin-amd64") {
			t.Fatalf("\nwanted:\nonly archive downloaded\ngot:\n%v", downloaded)
		}
		f.assertDirHolds(t, "marasi")
	})

	t.Run("should reject wrong Apple team on binary extracted from darwin zip", func(t *testing.T) {
		archive := makeZip(t, zipEntrySpec{name: "marasi", mode: 0o755, content: []byte("wrong team build")})
		f := newInstallFixture(t, "darwin", "marasi", map[string][]byte{
			"marasi-darwin-amd64.zip": archive,
		})
		u := f.updater(t, Config{
			GOOS: "darwin",
			Team: fakeTeams{"old build": "TEAM123", "wrong team build": "OTHER99"},
		})

		err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
		assertFailure(t, err, KindTeam, StepVerify)
		f.assertUnchanged(t)
	})

	t.Run("should prefer zip over bare executable on windows and extract marasi.exe", func(t *testing.T) {
		archive := makeZip(t, zipEntrySpec{name: "marasi.exe", content: []byte("new windows build")})
		f := newInstallFixture(t, "windows", "marasi.exe", map[string][]byte{
			"marasi-windows-amd64.exe": []byte("bare windows build"),
			"marasi-windows-amd64.zip": archive,
		})
		u := f.updater(t, Config{GOOS: "windows"})

		if err := u.Install(context.Background(), f.candidate(t, u), f.target, nil); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got, err := os.ReadFile(f.target)
		if err != nil || string(got) != "new windows build" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q (%v)", "new windows build", got, err)
		}
		downloaded := f.github.downloaded()
		if !slices.Contains(downloaded, "marasi-windows-amd64.zip") || slices.Contains(downloaded, "marasi-windows-amd64.exe") {
			t.Fatalf("\nwanted:\nonly zip archive downloaded\ngot:\n%v", downloaded)
		}
		asides, _ := filepath.Glob(filepath.Join(f.dir, ".marasi.exe.*.old"))
		if len(asides) != 1 {
			t.Fatalf("\nwanted:\n1 aside file\ngot:\n%v", asides)
		}
		if err := CleanupAside(f.target); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		f.assertDirHolds(t, "marasi.exe")
	})
}

func TestInstallAcceptsArchiveOnlyRelease(t *testing.T) {
	cases := []struct {
		name      string
		goos      string
		exe       string
		assetName string
		archive   func(t *testing.T) []byte
	}{
		{
			name:      "should install linux tar.gz without bare executable",
			goos:      "linux",
			exe:       "marasi",
			assetName: "marasi-linux-amd64.tar.gz",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("linux archive only")})
			},
		},
		{
			name:      "should install darwin zip without bare executable",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "marasi", mode: 0o755, content: []byte("darwin archive only")})
			},
		},
		{
			name:      "should install windows zip without bare executable",
			goos:      "windows",
			exe:       "marasi.exe",
			assetName: "marasi-windows-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "marasi.exe", content: []byte("windows archive only")})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t, tc.goos, tc.exe, map[string][]byte{tc.assetName: tc.archive(t)})
			cfg := Config{GOOS: tc.goos}
			if tc.goos == "darwin" {
				cfg.Team = fakeTeams{"old build": "TEAM123", "darwin archive only": "TEAM123"}
			}
			u := f.updater(t, cfg)

			if err := u.Install(context.Background(), f.candidate(t, u), f.target, nil); err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got, _ := os.ReadFile(f.target)
			if !strings.HasSuffix(string(got), "archive only") {
				t.Fatalf("\nwanted:\nextracted archive content\ngot:\n%q", got)
			}
		})
	}
}

func TestInstallFallsBackToBareExecutable(t *testing.T) {
	t.Run("should fall back when archive is published but omitted from signed checksums", func(t *testing.T) {
		archive := makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("archive build")})
		f := newInstallFixture(t, "linux", "marasi", map[string][]byte{
			"marasi-linux-amd64": []byte("bare fallback build"),
		})
		// Attach the archive to the release without adding it to signed checksums.txt.
		f.github.latest.assets["marasi-linux-amd64.tar.gz"] = archive
		u := f.updater(t, Config{GOOS: "linux"})

		if err := u.Install(context.Background(), f.candidate(t, u), f.target, nil); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got, err := os.ReadFile(f.target)
		if err != nil || string(got) != "bare fallback build" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q (%v)", "bare fallback build", got, err)
		}
		downloaded := f.github.downloaded()
		if slices.Contains(downloaded, "marasi-linux-amd64.tar.gz") || !slices.Contains(downloaded, "marasi-linux-amd64") {
			t.Fatalf("\nwanted:\nonly bare executable downloaded\ngot:\n%v", downloaded)
		}
		f.assertDirHolds(t, "marasi")
	})

	t.Run("should fail before downloading when neither archive nor bare executable is in signed checksums", func(t *testing.T) {
		archive := makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("archive build")})
		f := newInstallFixture(t, "linux", "marasi", map[string][]byte{
			"marasi-darwin-arm64.zip": []byte("other platform"),
		})
		f.github.latest.assets["marasi-linux-amd64.tar.gz"] = archive
		f.github.latest.assets["marasi-linux-amd64"] = []byte("bare build")
		u := f.updater(t, Config{GOOS: "linux"})

		err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
		assertFailure(t, err, KindChecksum, StepVerify)
		downloaded := f.github.downloaded()
		if slices.Contains(downloaded, "marasi-linux-amd64.tar.gz") || slices.Contains(downloaded, "marasi-linux-amd64") {
			t.Fatalf("\nwanted:\nneither build asset downloaded\ngot:\n%v", downloaded)
		}
		f.assertUnchanged(t)
	})
}

func TestInstallRejectsArchiveWithBadChecksumBeforeExtracting(t *testing.T) {
	cases := []struct {
		name      string
		goos      string
		exe       string
		assetName string
		valid     func(t *testing.T) []byte
	}{
		{
			name:      "should reject tampered tar.gz before extracting",
			goos:      "linux",
			exe:       "marasi",
			assetName: "marasi-linux-amd64.tar.gz",
			valid:     func(t *testing.T) []byte { return makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("good")}) },
		},
		{
			name:      "should reject tampered zip before extracting",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			valid: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "marasi", mode: 0o755, content: []byte("good")})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t, tc.goos, tc.exe, map[string][]byte{tc.assetName: tc.valid(t)})
			// Replace the served archive with non-archive bytes after signing.
			// If extraction ran before checksum verification, KindInstall would be returned instead of KindChecksum.
			f.github.latest.assets[tc.assetName] = []byte("not even an archive")
			cfg := Config{GOOS: tc.goos}
			if tc.goos == "darwin" {
				cfg.Team = fakeTeams{"old build": "TEAM123", "good": "TEAM123"}
			}
			u := f.updater(t, cfg)

			err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
			assertFailure(t, err, KindChecksum, StepVerify)
			f.assertUnchanged(t)
		})
	}
}

func TestInstallRejectsBadZipArchive(t *testing.T) {
	cases := []struct {
		name      string
		goos      string
		exe       string
		assetName string
		maxSize   int64
		archive   func(t *testing.T) []byte
	}{
		{
			name:      "should reject empty zip archive",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive:   func(t *testing.T) []byte { return makeZip(t) },
		},
		{
			name:      "should reject zip archive with extra files",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t,
					zipEntrySpec{name: "marasi", mode: 0o755, content: []byte("new")},
					zipEntrySpec{name: "._marasi", mode: 0o644, content: []byte("extra")},
				)
			},
		},
		{
			name:      "should reject zip archive with wrong file name",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "other", mode: 0o755, content: []byte("new")})
			},
		},
		{
			name:      "should reject zip archive with path traversal name",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "../marasi", mode: 0o755, content: []byte("new")})
			},
		},
		{
			name:      "should reject zip archive with subdir path",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "bin/marasi", mode: 0o755, content: []byte("new")})
			},
		},
		{
			name:      "should reject windows zip containing marasi without exe suffix",
			goos:      "windows",
			exe:       "marasi.exe",
			assetName: "marasi-windows-amd64.zip",
			archive:   func(t *testing.T) []byte { return makeZip(t, zipEntrySpec{name: "marasi", content: []byte("new")}) },
		},
		{
			name:      "should reject zip archive with symlink entry",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "marasi", mode: fs.ModeSymlink | 0o777, content: []byte("/etc/passwd")})
			},
		},
		{
			name:      "should reject zip archive with directory entry",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive:   func(t *testing.T) []byte { return makeZip(t, zipEntrySpec{name: "marasi", mode: fs.ModeDir | 0o755}) },
		},
		{
			name:      "should reject zip archive with named pipe entry",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "marasi", mode: fs.ModeNamedPipe | 0o644})
			},
		},
		{
			name:      "should reject corrupt zip archive",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive:   func(t *testing.T) []byte { return []byte("not a valid zip file") },
		},
		{
			name:      "should reject zip archive with corrupt payload CRC",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			archive: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				hdr := &zip.FileHeader{Name: "marasi", Method: zip.Store}
				hdr.SetMode(0o755)
				w, err := zw.CreateHeader(hdr)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte("stored payload"))
				_ = zw.Close()
				data := buf.Bytes()
				idx := bytes.Index(data, []byte("stored payload"))
				if idx < 0 {
					t.Fatal("stored payload not found in zip")
				}
				data[idx] ^= 0xff
				return data
			},
		},
		{
			name:      "should reject oversized zip entry",
			goos:      "darwin",
			exe:       "marasi",
			assetName: "marasi-darwin-amd64.zip",
			maxSize:   32,
			archive: func(t *testing.T) []byte {
				return makeZip(t, zipEntrySpec{name: "marasi", mode: 0o755, content: bytes.Repeat([]byte("a"), 64)})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t, tc.goos, tc.exe, map[string][]byte{tc.assetName: tc.archive(t)})
			cfg := Config{GOOS: tc.goos}
			if tc.goos == "darwin" {
				cfg.Team = fakeTeams{"old build": "TEAM123"}
			}
			u := f.updater(t, cfg)
			if tc.maxSize > 0 {
				// Leave room to download the zip itself while bounding extraction.
				// Use a direct extractArchive check for declared-size and streaming limits
				// when archive bytes exceed maxSize; here a 64-byte repeated string compresses
				// smaller than 32 bytes only if the zip header fits, so test via extractArchive too.
				zipBytes := tc.archive(t)
				if int64(len(zipBytes)) <= tc.maxSize {
					u.maxBuildSize = tc.maxSize
				} else {
					tmp := filepath.Join(t.TempDir(), "oversized.zip")
					if err := os.WriteFile(tmp, zipBytes, 0o600); err != nil {
						t.Fatal(err)
					}
					var out bytes.Buffer
					if err := extractArchive(tmp, tc.assetName, expectedEntryName(tc.goos), &out, tc.maxSize); err == nil {
						t.Fatalf("\nwanted:\nerror for oversized zip entry\ngot:\nnil")
					}
					// Also test Install when compressed archive fits under maxSize (highly compressible payload).
					bigArchive := makeZip(t, zipEntrySpec{name: "marasi", mode: 0o755, content: bytes.Repeat([]byte("a"), 4096)})
					f = newInstallFixture(t, tc.goos, tc.exe, map[string][]byte{tc.assetName: bigArchive})
					u = f.updater(t, cfg)
					u.maxBuildSize = int64(len(bigArchive)) + 16
				}
			}

			err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
			assertFailure(t, err, KindInstall, StepVerify)
			f.assertUnchanged(t)
		})
	}
}

func TestInstallRejectsBadTarGzArchive(t *testing.T) {
	cases := []struct {
		name    string
		maxSize int64
		archive func(t *testing.T) []byte
	}{
		{
			name:    "should reject empty tar.gz archive",
			archive: func(t *testing.T) []byte { return makeTarGz(t) },
		},
		{
			name: "should reject tar.gz archive with extra files",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t,
					tarEntrySpec{name: "marasi", content: []byte("new")},
					tarEntrySpec{name: "extra", content: []byte("nope")},
				)
			},
		},
		{
			name:    "should reject tar.gz archive with wrong file name",
			archive: func(t *testing.T) []byte { return makeTarGz(t, tarEntrySpec{name: "other", content: []byte("new")}) },
		},
		{
			name:    "should reject tar.gz archive with relative dot-slash prefix",
			archive: func(t *testing.T) []byte { return makeTarGz(t, tarEntrySpec{name: "./marasi", content: []byte("new")}) },
		},
		{
			name: "should reject tar.gz archive with path traversal name",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntrySpec{name: "../marasi", content: []byte("new")})
			},
		},
		{
			name: "should reject tar.gz archive with symlink entry",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntrySpec{name: "marasi", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"})
			},
		},
		{
			name: "should reject tar.gz archive with hardlink entry",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntrySpec{name: "marasi", typeflag: tar.TypeLink, linkname: "/etc/passwd"})
			},
		},
		{
			name:    "should reject tar.gz archive with directory entry",
			archive: func(t *testing.T) []byte { return makeTarGz(t, tarEntrySpec{name: "marasi", typeflag: tar.TypeDir}) },
		},
		{
			name:    "should reject tar.gz archive with fifo entry",
			archive: func(t *testing.T) []byte { return makeTarGz(t, tarEntrySpec{name: "marasi", typeflag: tar.TypeFifo}) },
		},
		{
			name:    "should reject corrupt gzip stream",
			archive: func(t *testing.T) []byte { return []byte("not a gzip stream") },
		},
		{
			name: "should reject corrupt tar inside valid gzip",
			archive: func(t *testing.T) []byte {
				var buf bytes.Buffer
				gz := gzip.NewWriter(&buf)
				_, _ = gz.Write([]byte("not a valid tar archive header at all"))
				_ = gz.Close()
				return buf.Bytes()
			},
		},
		{
			name: "should reject truncated gzip trailer",
			archive: func(t *testing.T) []byte {
				full := makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("new")})
				return full[:len(full)-4]
			},
		},
		{
			name: "should reject concatenated gzip streams",
			archive: func(t *testing.T) []byte {
				first := makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("new")})
				second := makeTarGz(t, tarEntrySpec{name: "evil", content: []byte("bad")})
				return append(first, second...)
			},
		},
		{
			name:    "should reject oversized tar.gz entry",
			maxSize: 512,
			archive: func(t *testing.T) []byte {
				// Highly compressible payload so the .tar.gz itself is well under 512 bytes
				// while the extracted entry exceeds 512 bytes.
				return makeTarGz(t, tarEntrySpec{name: "marasi", content: bytes.Repeat([]byte("a"), 8192)})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archiveBytes := tc.archive(t)
			f := newInstallFixture(t, "linux", "marasi", map[string][]byte{
				"marasi-linux-amd64.tar.gz": archiveBytes,
			})
			u := f.updater(t, Config{GOOS: "linux"})
			if tc.maxSize > 0 {
				if int64(len(archiveBytes)) >= tc.maxSize {
					t.Fatalf("\nwanted:\ncompressed archive smaller than %d bytes\ngot:\n%d bytes", tc.maxSize, len(archiveBytes))
				}
				u.maxBuildSize = tc.maxSize
			}

			err := u.Install(context.Background(), f.candidate(t, u), f.target, nil)
			assertFailure(t, err, KindInstall, StepVerify)
			f.assertUnchanged(t)
		})
	}
}
