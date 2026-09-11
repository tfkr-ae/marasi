package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func projectPath(configDir, name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".marasi")
	if name == "" {
		return "", fmt.Errorf("project name is empty")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("invalid project name %q", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid project name %q", name)
	}

	return filepath.Join(configDir, "projects", name+".marasi"), nil
}

func lockProject(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating projects dir: %w", err)
	}

	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening project lock %s: %w", lockPath, err)
	}
	fd := int(f.Fd())

	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("project already open: %w", err)
	}

	return func() error {
		unlockErr := unix.Flock(fd, unix.LOCK_UN)
		closeErr := f.Close()
		if unlockErr != nil {
			return fmt.Errorf("unlocking project: %w", unlockErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing project lock: %w", closeErr)
		}
		return nil
	}, nil
}
