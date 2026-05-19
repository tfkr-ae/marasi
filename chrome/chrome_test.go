package chrome

import (
	"runtime"
	"strings"
	"testing"
)

func TestNewLauncher(t *testing.T) {
	t.Run("Should initialize with default values", func(t *testing.T) {
		l := NewLauncher()

		if l.addr != "127.0.0.1" {
			t.Fatalf("\nwanted:\n127.0.0.1\ngot:\n%q", l.addr)
		}
		if l.port != "8080" {
			t.Fatalf("\nwanted:\n8080\ngot:\n%q", l.port)
		}
		if l.profile != "default-profile" {
			t.Fatalf("\nwanted:\ndefault-profile\ngot:\n%q", l.profile)
		}
	})

	t.Run("Should apply provided options correctly", func(t *testing.T) {
		customPaths := []PathConfig{{OS: "linux", Path: "/custom/chrome"}}
		l := NewLauncher(
			WithProxy("10.0.0.1", "9090"),
			WithSPKIHash("testhash"),
			WithConfigDir("/tmp/marasi"),
			WithProfile("test-profile"),
			WithCustomPaths(customPaths),
		)

		if l.addr != "10.0.0.1" {
			t.Fatalf("\nwanted:\n10.0.0.1\ngot:\n%q", l.addr)
		}
		if l.port != "9090" {
			t.Fatalf("\nwanted:\n9090\ngot:\n%q", l.port)
		}
		if l.spkiHash != "testhash" {
			t.Fatalf("\nwanted:\ntesthash\ngot:\n%q", l.spkiHash)
		}
		if l.configDir != "/tmp/marasi" {
			t.Fatalf("\nwanted:\n/tmp/marasi\ngot:\n%q", l.configDir)
		}
		if l.profile != "test-profile" {
			t.Fatalf("\nwanted:\ntest-profile\ngot:\n%q", l.profile)
		}
		if len(l.customPaths) != 1 || l.customPaths[0].Path != "/custom/chrome" {
			t.Fatalf("\nwanted:\ncustom path /custom/chrome\ngot:\n%v", l.customPaths)
		}
	})

	t.Run("Should ignore empty profile string and keep default", func(t *testing.T) {
		l := NewLauncher(WithProfile(""))

		if l.profile != "default-profile" {
			t.Fatalf("\nwanted:\ndefault-profile\ngot:\n%q", l.profile)
		}
	})
}

func TestGetChromePath(t *testing.T) {
	t.Run("Should prioritize custom paths over defaults", func(t *testing.T) {
		l := NewLauncher(WithCustomPaths([]PathConfig{
			{OS: runtime.GOOS, Path: "go"},
		}))

		path := l.getChromePath()

		if path == "" {
			t.Fatalf("\nwanted:\nnon-empty executable path\ngot:\n%q", path)
		}
		if !strings.HasSuffix(path, "go") && !strings.HasSuffix(path, "go.exe") {
			t.Fatalf("\nwanted:\npath ending with go or go.exe\ngot:\n%q", path)
		}
	})
}

func TestStart(t *testing.T) {
	t.Run("Should fail when configDir is empty", func(t *testing.T) {
		l := NewLauncher()
		err := l.Start()

		if err == nil {
			t.Fatalf("\nwanted:\nan error\ngot:\nnil")
		}

		expectedErr := "configDir is required to start Chrome"
		if !strings.Contains(err.Error(), expectedErr) {
			t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%q", expectedErr, err.Error())
		}
	})

	t.Run("Should succeed when valid configuration and executable are provided", func(t *testing.T) {
		configDir := t.TempDir()

		l := NewLauncher(
			WithConfigDir(configDir),
			WithCustomPaths([]PathConfig{
				{OS: runtime.GOOS, Path: "go"},
			}),
		)

		err := l.Start()
		if err != nil {
			t.Fatalf("\nwanted:\nnil error (successful start)\ngot:\n%v", err)
		}
	})
}
