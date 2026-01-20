package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	_ "github.com/tfkr-ae/marasi/db/migrations"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql migrations/*.go
var embedMigrations embed.FS

// Repository provides a centralized structure for database operations, embedding the database connection.
// It acts as a receiver for methods that implement the various repository interfaces defined in the domain package.
type Repository struct {
	dbConn *sqlx.DB // dbConn is the active database connection pool.
}

// NewProxyRepo initializes a new Repository with the given sqlx.DB database connection.
func NewProxyRepo(db *sqlx.DB) *Repository {
	return &Repository{
		dbConn: db,
	}
}

// Close terminates the database connection.
// It is critical to call this to free up database resources.
func (repo *Repository) Close() error {
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

	db, err := sqlx.Connect("sqlite", fmt.Sprintf("%s?_journal=WAL&_timeout=5000&_fk=true", name))

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
	return db, nil
}
