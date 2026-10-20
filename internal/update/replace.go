package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Executable is the running executable with symlinks resolved: the
// installed build an instance replaces (docs/adr/2026-10-18-service-instances-install-updates.md).
func Executable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return resolveTarget(path)
}

func resolveTarget(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

// CheckWritable reports why the directory holding target cannot take a new
// build, or nil when it can. It creates and removes a temp file there.
func CheckWritable(target string) error {
	resolved, err := resolveTarget(target)
	if err != nil {
		return fmt.Errorf("finding the installed build: %w", err)
	}
	dir := filepath.Dir(resolved)
	probe, err := os.CreateTemp(dir, ".marasi-write-check-*")
	if err != nil {
		return fmt.Errorf("Marasi cannot write to %s, so it cannot replace itself; download the new build by hand: %w", dir, err)
	}
	name := probe.Name()
	return errors.Join(probe.Close(), os.Remove(name))
}

// replace puts staged, a file in target's directory, in place of target.
// On Unix it renames over target. On Windows, where a running executable
// cannot be overwritten, it renames target aside first and moves it back if
// staged cannot take its place; CleanupAside removes the aside file on the
// next start.
func replace(goos, staged, target string) error {
	if goos != "windows" {
		return os.Rename(staged, target)
	}
	aside := asidePath(target)
	if err := os.Rename(target, aside); err != nil {
		return fmt.Errorf("moving the running build aside: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		if restoreErr := os.Rename(aside, target); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restoring %s from %s: %w", target, aside, restoreErr))
		}
		return err
	}
	return nil
}

// asidePath names a unique aside file for target in its directory.
func asidePath(target string) string {
	dir, base := filepath.Split(target)
	return filepath.Join(dir, "."+base+"."+strconv.FormatInt(time.Now().UnixNano(), 36)+".old")
}

// CleanupAside removes builds a Windows install moved aside from target.
// Call it on start; an aside file still in use by another running instance
// stays and is reported in the error.
func CleanupAside(target string) error {
	dir, base := filepath.Split(target)
	asides, err := filepath.Glob(filepath.Join(dir, "."+base+".*.old"))
	if err != nil {
		return err
	}
	var errs []error
	for _, aside := range asides {
		if err := os.Remove(aside); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
