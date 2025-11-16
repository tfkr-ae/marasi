package domain

import (
	"time"

	"github.com/google/uuid"
)

// LogRepository defines the interface for managing application logs.
// It provides methods for persisting and retrieving log entries.
type LogRepository interface {
	// InsertLog saves a new log entry to the repository.
	InsertLog(log *Log) error
	// GetLogs retrieves all log entries from the repository.
	GetLogs() ([]*Log, error)
}

// Log represents a single log entry, containing information about an event that occurred in the application.
type Log struct {
	ID          uuid.UUID      // Unique identifier for the log entry.
	Timestamp   time.Time      // The time at which the log entry was created.
	Level       string         // The severity level of the log (e.g., DEBUG, INFO, WARN, ERROR, FATAL).
	Message     string         // The main content of the log message.
	Context     map[string]any // A map of additional key-value data for structured logging.
	RequestID   *uuid.UUID     // An optional ID of an associated HTTP request, for context.
	ExtensionID *uuid.UUID     // An optional ID of an associated extension, for context.
}
