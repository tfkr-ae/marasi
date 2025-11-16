// Package db provides the database layer for the Marasi application.
// It encapsulates all interactions with the underlying SQL database, managing
// data persistence for various application domains such as configurations,
// extensions, launchpads, logs, statistics, traffic (HTTP requests/responses),
// and waypoints.
//
// This package is responsible for:
// - Establishing and managing database connections (`db.go`).
// - Defining database-specific data structures that map to SQL table schemas.
// - Implementing repository interfaces (e.g., `TrafficRepository`, `ConfigRepository`)
//   to perform CRUD operations.
// - Handling data conversion between domain-specific structs (from the `domain` package)
//   and database-friendly structs, including the use of `sql.Null*` types for nullable fields.
// - Managing database migrations (`migrations/`).
// - Providing common database utility types (`types.go`).
package db
