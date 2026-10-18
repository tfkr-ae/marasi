package update

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallLockNamesItsHolder(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "marasi")

	held, err := AcquireLock(dir, target, "alpha")
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	_, err = AcquireLock(dir, target, "beta")
	var locked *LockedError
	if !errors.As(err, &locked) || locked.Holder != "alpha" {
		t.Fatalf("second AcquireLock error = %v, want LockedError naming alpha", err)
	}
	if err.Error() != `instance "alpha" is already installing an update for this build` {
		t.Errorf("message = %q", err.Error())
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := AcquireLock(dir, target, "beta")
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	defer again.Release()
}

func TestInstallLockIsPerInstalledBuild(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir, filepath.Join(t.TempDir(), "marasi"), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := AcquireLock(dir, filepath.Join(t.TempDir(), "marasi"), "beta")
	if err != nil {
		t.Fatalf("a different installed build is locked too: %v", err)
	}
	defer second.Release()
}

func TestCodesignReadsTeamIdentifier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake codesign is a shell script")
	}
	tool := filepath.Join(t.TempDir(), "codesign")
	script := `#!/bin/sh
case "$3" in
  /x/signed) printf 'Executable=%s\nIdentifier=marasi\nAuthority=Developer ID Application: Example (TEAM123)\nTeamIdentifier=TEAM123\n' "$3" >&2 ;;
  /x/adhoc) printf 'Executable=%s\nSignature=adhoc\nTeamIdentifier=not set\n' "$3" >&2 ;;
  *) printf '%s: code object is not signed at all\n' "$3" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Codesign{Path: tool}
	for path, want := range map[string]string{"/x/signed": "TEAM123", "/x/adhoc": "", "/x/unsigned": ""} {
		got, err := c.TeamID(t.Context(), path)
		if err != nil || got != want {
			t.Errorf("TeamID(%s) = %q, %v; want %q", path, got, err, want)
		}
	}
}
