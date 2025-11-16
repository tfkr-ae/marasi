package db

import (
	"embed"
	"fmt"

	_ "github.com/tfkr-ae/marasi/db/migrations"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
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
func New(name string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("sqlite", fmt.Sprintf("%s?_journal=WAL&_timeout=5000&_fk=true", name))
	if err != nil {
		return nil, fmt.Errorf("connecting to db : %w", err)
	}

	db.SetMaxOpenConns(1)

	_, err = db.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	goose.SetBaseFS(embedMigrations)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect(string(goose.DialectSQLite3)); err != nil {
		return nil, fmt.Errorf("setting dialect for migrations : %w", err)
	}

	if err := goose.Up(db.DB, "migrations"); err != nil {
		return nil, fmt.Errorf("applying migration : %w", err)
	}
	return db, nil
}
