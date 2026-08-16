package marasi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/report"
	"github.com/tfkr-ae/marasi/wordlist"
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

func TestWithWordlistManager(t *testing.T) {
	t.Run("should set wordlist manager", func(t *testing.T) {
		manager, err := wordlist.NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		proxy, err := New(WithWordlistManager(manager))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := proxy.GetWordlistManager()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != manager {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", manager, got)
		}
	})

	t.Run("should raise an error if wordlist manager is nil", func(t *testing.T) {
		_, err := New(WithWordlistManager(nil))
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestGetWordlistManager(t *testing.T) {
	proxy := &Proxy{}

	_, err := proxy.GetWordlistManager()
	if !errors.Is(err, ErrWordlistManagerNotSet) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrWordlistManagerNotSet, err)
	}
}

func TestWithWebSocketOpenHandler(t *testing.T) {
	proxy := &Proxy{}
	called := false
	handler := func(domain.WebSocketConnection) error {
		called = true
		return nil
	}

	err := WithWebSocketOpenHandler(handler)(proxy)
	if err != nil {
		t.Fatalf("wanted: nil\ngot: %v", err)
	}
	if proxy.OnWebSocketOpen == nil {
		t.Fatalf("wanted websocket open handler to be set")
	}
	if err := proxy.OnWebSocketOpen(domain.WebSocketConnection{}); err != nil {
		t.Fatalf("calling websocket open handler: %v", err)
	}
	if !called {
		t.Fatalf("wanted websocket open handler to be called")
	}
	if err := WithWebSocketOpenHandler(handler)(proxy); err == nil {
		t.Fatalf("wanted duplicate websocket open handler error")
	}
}

func TestWithWebSocketMessageHandler(t *testing.T) {
	proxy := &Proxy{}
	called := false
	handler := func(domain.WebSocketMessage) error {
		called = true
		return nil
	}

	err := WithWebSocketMessageHandler(handler)(proxy)
	if err != nil {
		t.Fatalf("wanted: nil\ngot: %v", err)
	}
	if proxy.OnWebSocketMessage == nil {
		t.Fatalf("wanted websocket message handler to be set")
	}
	if err := proxy.OnWebSocketMessage(domain.WebSocketMessage{}); err != nil {
		t.Fatalf("calling websocket message handler: %v", err)
	}
	if !called {
		t.Fatalf("wanted websocket message handler to be called")
	}
	if err := WithWebSocketMessageHandler(handler)(proxy); err == nil {
		t.Fatalf("wanted duplicate websocket message handler error")
	}
}

func TestWithWebSocketCloseHandler(t *testing.T) {
	proxy := &Proxy{}
	called := false
	handler := func(domain.WebSocketConnection) error {
		called = true
		return nil
	}

	err := WithWebSocketCloseHandler(handler)(proxy)
	if err != nil {
		t.Fatalf("wanted: nil\ngot: %v", err)
	}
	if proxy.OnWebSocketClose == nil {
		t.Fatalf("wanted websocket close handler to be set")
	}
	if err := proxy.OnWebSocketClose(domain.WebSocketConnection{}); err != nil {
		t.Fatalf("calling websocket close handler: %v", err)
	}
	if !called {
		t.Fatalf("wanted websocket close handler to be called")
	}
	if err := WithWebSocketCloseHandler(handler)(proxy); err == nil {
		t.Fatalf("wanted duplicate websocket close handler error")
	}
}

func TestWithWebSocketInterceptHandler(t *testing.T) {
	proxy := &Proxy{}
	called := false
	handler := func(domain.WebSocketMessage) error {
		called = true
		return nil
	}

	err := WithWebSocketInterceptHandler(handler)(proxy)
	if err != nil {
		t.Fatalf("wanted: nil\ngot: %v", err)
	}
	if proxy.OnWebSocketIntercept == nil {
		t.Fatalf("wanted websocket intercept handler to be set")
	}
	if err := proxy.OnWebSocketIntercept(domain.WebSocketMessage{}); err != nil {
		t.Fatalf("calling websocket intercept handler: %v", err)
	}
	if !called {
		t.Fatalf("wanted websocket intercept handler to be called")
	}
	if err := WithWebSocketInterceptHandler(handler)(proxy); err == nil {
		t.Fatalf("wanted duplicate websocket intercept handler error")
	}
}
