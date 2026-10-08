package marasi

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/martian/mitm"
	"github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/report"
	"github.com/tfkr-ae/marasi/wordlist"
)

var _ ArmoryService = (*armory.Manager)(nil)

type testArmoryService struct {
	ArmoryService
}

type testTLSConfigRepository struct {
	domain.ConfigRepository
}

func (testTLSConfigRepository) UpdateSPKI(string) error {
	return nil
}

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

func TestWithTLS(t *testing.T) {
	newProxy := func(t *testing.T, configDir string, option func(*Proxy) error) *Proxy {
		t.Helper()
		proxy, err := New(
			WithConfigDir(configDir),
			WithConfigRepository(testTLSConfigRepository{}),
			option,
		)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		return proxy
	}

	t.Run("should serialize concurrent first initialization", func(t *testing.T) {
		configDir := t.TempDir()
		if _, err := New(WithConfigDir(configDir)); err != nil {
			t.Fatalf("preparing config directory: %v", err)
		}
		lock, err := acquireCertificateLock(context.Background(), configDir)
		if err != nil {
			t.Fatalf("acquiring certificate lock: %v", err)
		}

		const proxyCount = 6
		commands := make([]*exec.Cmd, proxyCount)
		outputs := make([]bytes.Buffer, proxyCount)
		errorsOutput := make([]bytes.Buffer, proxyCount)
		for i := range proxyCount {
			command := exec.Command(os.Args[0], "-test.run=^TestWithTLSHelperProcess$")
			command.Env = append(os.Environ(), "MARASI_TLS_HELPER=1", "MARASI_TLS_CONFIG_DIR="+configDir)
			command.Stdout = &outputs[i]
			command.Stderr = &errorsOutput[i]
			commands[i] = command
			if err := command.Start(); err != nil {
				releaseCertificateLock(lock)
				t.Fatalf("starting proxy %d: %v", i, err)
			}
		}
		time.Sleep(50 * time.Millisecond)
		if err := releaseCertificateLock(lock); err != nil {
			t.Fatalf("releasing certificate lock: %v", err)
		}

		for i, command := range commands {
			if err := command.Wait(); err != nil {
				t.Fatalf("\nwanted proxy %d error:\nnil\ngot:\n%v\n%s", i, err, errorsOutput[i].String())
			}
			if outputs[i].String() != outputs[0].String() {
				t.Fatalf("\nwanted proxy %d certificate:\n%s\ngot:\n%s", i, outputs[0].String(), outputs[i].String())
			}
		}
	})

	t.Run("should stop waiting when context is canceled", func(t *testing.T) {
		configDir := t.TempDir()
		lock, err := acquireCertificateLock(context.Background(), configDir)
		if err != nil {
			t.Fatalf("acquiring certificate lock: %v", err)
		}
		t.Cleanup(func() { releaseCertificateLock(lock) })

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := New(
				WithConfigDir(configDir),
				WithConfigRepository(testTLSConfigRepository{}),
				WithTLSContext(ctx),
			)
			result <- err
		}()
		time.Sleep(20 * time.Millisecond)
		cancel()

		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for canceled TLS initialization")
		}
	})

	t.Run("should replace an incomplete pair", func(t *testing.T) {
		configDir := t.TempDir()
		keyPath := filepath.Join(configDir, keyFile)
		if err := os.WriteFile(keyPath, []byte("incomplete key"), 0600); err != nil {
			t.Fatalf("writing incomplete key: %v", err)
		}

		proxy := newProxy(t, configDir, WithTLS())
		loadedCert, _, err := loadCertAndKey(configDir)
		if err != nil {
			t.Fatalf("loading replacement pair: %v", err)
		}
		if !proxy.Cert.Equal(loadedCert) {
			t.Fatalf("\nwanted:\n%x\ngot:\n%x", proxy.Cert.Raw, loadedCert.Raw)
		}
	})

	t.Run("should leave an established invalid pair unchanged", func(t *testing.T) {
		configDir := t.TempDir()
		certPath := filepath.Join(configDir, certFile)
		keyPath := filepath.Join(configDir, keyFile)
		wantCert := []byte("invalid certificate")
		wantKey := []byte("invalid key")
		if err := os.WriteFile(certPath, wantCert, 0600); err != nil {
			t.Fatalf("writing invalid certificate: %v", err)
		}
		if err := os.WriteFile(keyPath, wantKey, 0600); err != nil {
			t.Fatalf("writing invalid key: %v", err)
		}

		_, err := New(
			WithConfigDir(configDir),
			WithConfigRepository(testTLSConfigRepository{}),
			WithTLS(),
		)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		gotCert, readErr := os.ReadFile(certPath)
		if readErr != nil {
			t.Fatalf("reading certificate: %v", readErr)
		}
		gotKey, readErr := os.ReadFile(keyPath)
		if readErr != nil {
			t.Fatalf("reading key: %v", readErr)
		}
		if !bytes.Equal(gotCert, wantCert) || !bytes.Equal(gotKey, wantKey) {
			t.Fatalf("\nwanted:\n%q and %q\ngot:\n%q and %q", wantCert, wantKey, gotCert, gotKey)
		}
	})

	t.Run("should reject a mismatched pair without replacing it", func(t *testing.T) {
		configDir := t.TempDir()
		cert, _, err := mitm.NewAuthority("first", "first", time.Hour)
		if err != nil {
			t.Fatalf("creating certificate: %v", err)
		}
		_, key, err := mitm.NewAuthority("second", "second", time.Hour)
		if err != nil {
			t.Fatalf("creating key: %v", err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
		keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatalf("marshaling key: %v", err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
		certPath := filepath.Join(configDir, certFile)
		keyPath := filepath.Join(configDir, keyFile)
		if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
			t.Fatalf("writing certificate: %v", err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
			t.Fatalf("writing key: %v", err)
		}

		_, err = New(
			WithConfigDir(configDir),
			WithConfigRepository(testTLSConfigRepository{}),
			WithTLS(),
		)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		gotCert, _ := os.ReadFile(certPath)
		gotKey, _ := os.ReadFile(keyPath)
		if !bytes.Equal(gotCert, certPEM) || !bytes.Equal(gotKey, keyPEM) {
			t.Fatalf("mismatched pair was replaced")
		}
	})

	t.Run("should remove temporary files after publishing", func(t *testing.T) {
		configDir := t.TempDir()
		newProxy(t, configDir, WithTLS())

		matches, err := filepath.Glob(filepath.Join(configDir, ".marasi-*.tmp"))
		if err != nil {
			t.Fatalf("finding temporary files: %v", err)
		}
		if len(matches) != 0 {
			t.Fatalf("\nwanted:\nno temporary certificate files\ngot:\n%v", matches)
		}
		if _, err := os.Stat(filepath.Join(configDir, "certificate.lock")); err != nil {
			t.Fatalf("checking retained certificate lock: %v", err)
		}
	})

	t.Run("should remove temporary files after publishing fails", func(t *testing.T) {
		configDir := t.TempDir()
		keyPath := filepath.Join(configDir, keyFile)
		if err := os.Mkdir(keyPath, 0700); err != nil {
			t.Fatalf("creating obstructing key directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(keyPath, "keep"), []byte("keep"), 0600); err != nil {
			t.Fatalf("populating obstructing key directory: %v", err)
		}

		_, err := New(
			WithConfigDir(configDir),
			WithConfigRepository(testTLSConfigRepository{}),
			WithTLS(),
		)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		matches, globErr := filepath.Glob(filepath.Join(configDir, ".marasi-*.tmp"))
		if globErr != nil {
			t.Fatalf("finding temporary files: %v", globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("\nwanted:\nno temporary certificate files\ngot:\n%v", matches)
		}
	})
}

func TestWithTLSHelperProcess(t *testing.T) {
	if os.Getenv("MARASI_TLS_HELPER") != "1" {
		return
	}
	proxy, err := New(
		WithConfigDir(os.Getenv("MARASI_TLS_CONFIG_DIR")),
		WithConfigRepository(testTLSConfigRepository{}),
		WithTLSContext(context.Background()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.WriteString(proxy.SPKIHash); err != nil {
		t.Fatal(err)
	}
}

func TestWithArmory(t *testing.T) {
	t.Run("should set armory service", func(t *testing.T) {
		service := &testArmoryService{}

		proxy, err := New(WithArmory(service))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if proxy.Armory != service {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", service, proxy.Armory)
		}
	})

	t.Run("should return an error if armory service is nil", func(t *testing.T) {
		_, err := New(WithArmory(nil))
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "armory service cannot be nil") {
			t.Fatalf("\nwanted:\nerror containing 'armory service cannot be nil'\ngot:\n%v", err)
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
