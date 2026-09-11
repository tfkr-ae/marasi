package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPath(t *testing.T) {
	t.Run("should resolve scratchpad under projects", func(t *testing.T) {
		configDir := t.TempDir()

		got, err := projectPath(configDir, "scratchpad")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should resolve a named project under projects", func(t *testing.T) {
		configDir := t.TempDir()

		got, err := projectPath(configDir, "juiceshop-test")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "projects", "juiceshop-test.marasi")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should strip a trailing .marasi suffix", func(t *testing.T) {
		configDir := t.TempDir()

		got, err := projectPath(configDir, "scratchpad.marasi")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should raise an error if project name is empty", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name is absolute path", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "/tmp/scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains parent directory reference", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "../scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains subdirectory", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "nested/scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains Windows path separator", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), `nested\scratchpad`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name is current directory", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), ".")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestLockProject(t *testing.T) {
	t.Run("should create the projects directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects", "scratchpad.marasi")

		unlock, err := lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer unlock()

		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if !info.IsDir() {
			t.Fatalf("\nwanted:\ndirectory\ngot:\n%v", info.Mode())
		}
	})

	t.Run("should raise an error if the project is already open", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects", "scratchpad.marasi")

		unlock, err := lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer unlock()

		_, err = lockProject(path)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should allow opening the project after it is closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects", "scratchpad.marasi")

		unlock, err := lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := unlock(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		unlock, err = lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer unlock()
	})
}

func TestStartService(t *testing.T) {
	t.Run("should create the default scratchpad project", func(t *testing.T) {
		configDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := startService(ctx, configDir, "scratchpad")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		path := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should raise an error if the project name is invalid", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := startService(ctx, t.TempDir(), "foo/bar")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}
