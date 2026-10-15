package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
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
	// annotationIndex is what the repository knows about the annotation
	// index backfill.
	annotationIndex indexBuildState
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
	if err := pinSearchIndexStatistics(db); err != nil {
		db.Close()
		dbLogger.Error("Failed to pin search index statistics", "error", err)
		return nil, fmt.Errorf("pinning search index statistics: %w", err)
	}
	return db, nil
}

// pinSearchIndexStatistics gives every FTS5 shadow table the statistics
// SQLite assumes for a table it has never analysed, a million rows, in place
// of whatever ANALYZE measured. FTS5 reads and writes its shadow tables only
// by key, so measured statistics can only make that worse: taken while a
// project is new, they record a two-row traffic_index_data, and the planner
// runs the rowid-range DELETE that every FTS5 segment merge issues as a full
// scan of the index. Each capture then costs more the larger the index has
// grown, until the project is reopened and analysed again. Deleting the rows
// is not enough: reloading statistics keeps a table's last measured size.
func pinSearchIndexStatistics(db *sqlx.DB) error {
	var statTables []string
	if err := db.Select(&statTables, `SELECT name FROM sqlite_schema
		WHERE type = 'table' AND name IN ('sqlite_stat1', 'sqlite_stat4')`); err != nil {
		return err
	}
	if !slices.Contains(statTables, "sqlite_stat1") {
		return nil
	}
	shadows := `SELECT s.name FROM sqlite_schema s
		JOIN sqlite_schema v ON v.type = 'table' AND v.sql LIKE 'CREATE VIRTUAL TABLE%USING fts5%'
		WHERE s.type = 'table' AND s.name IN (v.name || '_data', v.name || '_idx',
			v.name || '_content', v.name || '_docsize', v.name || '_config')`
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stat := range statTables {
		if _, err := tx.Exec(`DELETE FROM ` + stat + ` WHERE tbl IN (` + shadows + `)`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO sqlite_stat1 (tbl, idx, stat)
		SELECT name, NULL, '1000000' FROM (` + shadows + `)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.Exec(`ANALYZE sqlite_schema`)
	return err
}
