package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	_ "github.com/tfkr-ae/marasi/db/migrations"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql migrations/*.go
var embedMigrations embed.FS

// migrateMu serializes goose.NewProvider and Up. Goose's global Go migration
// registry is not safe for concurrent providers.
var migrateMu sync.Mutex

// Repository provides a centralized structure for database operations, embedding the database connection.
// It acts as a receiver for methods that implement the various repository interfaces defined in the domain package.
type Repository struct {
	dbConn *sqlx.DB // dbConn is the active database connection pool.
	index  indexBuildState
	// jobMu serialises background job batches with job resets, so a reset
	// never lands between a batch reading its progress and committing it.
	jobMu sync.Mutex
	// sitemapFrom, when set, means every pair with an id at or above it is
	// in the sitemap. jobMu guards it.
	sitemapFrom *uuid.UUID
	// prettifiedMovedTo is the oldest pair the prettified move has passed in
	// this session, so its next batch starts below it. It is guarded by jobMu.
	prettifiedMovedTo *uuid.UUID
}

// NewProxyRepo initializes a new Repository with the given sqlx.DB database connection.
func NewProxyRepo(db *sqlx.DB) *Repository {
	return &Repository{dbConn: db}
}

// Close terminates the database connection.
// It is critical to call this to free up database resources.
func (repo *Repository) Close() error {
	// Refresh the statistics the planner uses to choose an index
	// (ADR-0041). A failure here must not keep the project open.
	_, _ = repo.dbConn.Exec(`PRAGMA optimize`)
	err := repo.dbConn.Close()
	if err != nil {
		return fmt.Errorf("closing repo : %w", err)
	}
	return nil
}

// New establishes a new connection to a SQLite database file and applies all pending migrations.
// It configures the connection for optimal performance and data integrity by enabling WAL mode and foreign keys.
//
// The `name` parameter should be the file path for the SQLite database.
//
// It returns a ready-to-use sqlx.DB connection pool or an error if the connection or migrations fail.
func New(name string, logger *slog.Logger) (*sqlx.DB, error) {
	if logger == nil {
		logger = slog.Default()
	}

	dbLogger := logger.With("component", "db")
	dbLogger.Info("Connecting to SQLite...", "path", name)

	dsn := name
	if name != ":memory:" && name != "" {
		path, err := filepath.Abs(name)
		if err != nil {
			return nil, fmt.Errorf("resolving database path: %w", err)
		}
		path = filepath.ToSlash(path)
		// SQLite file URIs use /C:/... for absolute Windows drive paths.
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		dsn = (&url.URL{Scheme: "file", Path: path}).String()
	}
	// modernc.org/sqlite reads only _pragma parameters; it ignores the
	// _journal, _timeout, and _fk spellings of other drivers.
	// synchronous NORMAL fsyncs only at WAL checkpoints (ADR-0029).
	db, err := sqlx.Connect("sqlite", dsn+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")

	if err != nil {
		dbLogger.Error("Failed to connect to database", "error", err)
		return nil, fmt.Errorf("connecting to db : %w", err)
	}

	db.SetMaxOpenConns(1)

	_, err = db.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		db.Close()
		dbLogger.Error("Failed to enable foreign keys", "error", err)
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	migrationsFS, err := fs.Sub(embedMigrations, "migrations")
	if err != nil {
		dbLogger.Error("Failed to load migration file system", "error", err)
		return nil, fmt.Errorf("creating migrations fs: %w", err)
	}

	migrateMu.Lock()
	defer migrateMu.Unlock()

	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db.DB,
		migrationsFS,
		goose.WithVerbose(true),
		goose.WithSlog(logger),
	)

	if err != nil {
		dbLogger.Error("Failed to initialize migration provider", "error", err)
		return nil, fmt.Errorf("creating goose provider : %w", err)
	}

	res, err := provider.Up(context.Background())

	if err != nil {
		dbLogger.Error("Failed to apply migrations", "error", err)
		return nil, fmt.Errorf("applying migration : %w", err)
	}
	if len(res) > 0 {
		names := make([]string, len(res))

		for i, res := range res {
			names[i] = res.Source.Path
		}
		dbLogger.Info("Migrations completed", "count", len(res), "applied", names)
	} else {
		dbLogger.Info("Migrations up to date", "count", 0)
	}
	if err := reconcileJobs(db); err != nil {
		db.Close()
		dbLogger.Error("Failed to reconcile background jobs", "error", err)
		return nil, fmt.Errorf("reconciling background jobs: %w", err)
	}
	// The planner chooses between a sort index and a filter's index from
	// statistics (ADR-0041). analysis_limit keeps the first analysis of a
	// large project to a sample.
	if _, err := db.Exec(`PRAGMA analysis_limit = 400; PRAGMA optimize = 0x10002`); err != nil {
		db.Close()
		dbLogger.Error("Failed to optimize the database", "error", err)
		return nil, fmt.Errorf("optimizing the database: %w", err)
	}
	return db, nil
}
