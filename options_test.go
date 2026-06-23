package marasi

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/report"
)

func setupTestDB(t *testing.T) (*db.Repository, func()) {
	t.Helper()

	tempFile, err := os.CreateTemp(t.TempDir(), "test_*.db")
	if err != nil {
		t.Fatalf("os.CreateTemp() failed: %v", err)
	}
	tempFile.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConn, err := db.New(tempFile.Name(), logger)
	if err != nil {
		t.Fatalf("db.New() failed: %v", err)
	}

	repo := db.NewProxyRepo(dbConn)

	teardown := func() {
		repo.Close()
		os.Remove(tempFile.Name())
	}

	return repo, teardown
}

func TestWithLogger(t *testing.T) {
	t.Run("sets custom logger", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))

		p, err := New(
			WithLogger(logger),
		)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if p.Logger != logger {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", logger, p.Logger)
		}

		p.Logger.Info("test log message")
		if !strings.Contains(buf.String(), "test log message") {
			t.Fatalf("\nwanted:\nlog output containing 'test log message'\ngot:\n%q", buf.String())
		}
	})

	t.Run("handles nil logger safely", func(t *testing.T) {
		p, err := New(
			WithLogger(nil),
		)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if p.Logger == nil {
			t.Fatalf("\nwanted:\nnon-nil logger\ngot:\nnil")
		}

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("\nwanted:\nno panic\ngot:\n%v", r)
			}
		}()

		p.Logger.Info("safe check")
	})
}

func TestWithReportGenerator(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	generator, err := report.NewGenerator(repo, report.WithConfigDir(t.TempDir()))
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	p, err := New(
		WithReportGenerator(generator),
	)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	if p.ReportGenerator != generator {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", generator, p.ReportGenerator)
	}
}
