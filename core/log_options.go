// Package core provides fundamental utilities for the Marasi proxy.
// This file contains option functions for customizing log entries.
package core

import (
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// LogWithContext is an option to add a context map to a log entry.
func LogWithContext(context map[string]any) func(log *domain.Log) error {
	return func(log *domain.Log) error {
		log.Context = context
		return nil
	}
}

// LogWithReqResID is an option to associate a log entry with a request/response ID.
func LogWithReqResID(id uuid.UUID) func(log *domain.Log) error {
	return func(log *domain.Log) error {
		log.RequestID = &id
		return nil
	}
}

// LogWithExtensionID is an option to associate a log entry with an extension ID.
func LogWithExtensionID(id uuid.UUID) func(log *domain.Log) error {
	return func(log *domain.Log) error {
		log.ExtensionID = &id
		return nil
	}
}
