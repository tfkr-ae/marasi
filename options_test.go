package marasi

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

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
