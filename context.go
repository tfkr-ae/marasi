package marasi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/martian"
	"github.com/google/uuid"
)

type contextKey string

const (
	// RequestIDKey is the context key for the request ID (uuid.UUID). The same ID is shared between the request and response
	RequestIDKey contextKey = "RequestID"
	// LaunchpadIDKey is the context key for the launchpad ID (uuid.UUID). This is set if the request originated from a launchpad
	LaunchpadIDKey contextKey = "LaunchpadID"
	// MetadataKey is the context key for the request & response metadata (Metadata)
	MetadataKey contextKey = "Metadata"
	// ExtensionKey is the context key for the extension ID (uuid.UUID) that the request originated from
	ExtensionKey contextKey = "ExtensionID"
	// DropKey is the context key for the flag (bool) to indicate that the request / response should be dropped by the modifier
	DropKey contextKey = "Drop"
	// SkipKey is the context key for the flag (bool) to indicate that the request / response should be skipped by the modifiers
	SkipKey contextKey = "Skip"
	// ShouldInterceptKey is the context key for the flag (bool) that indicates if the response of this request should be intercepted
	ShouldInterceptKey contextKey = "ShouldIntercept"
	// RequestTimeKey is the context key for the request timestamp (time.Time)
	RequestTimeKey contextKey = "RequestTime"
	// ResponseTimeKey is the context key for the response timestamp (time.Time)
	ResponseTimeKey contextKey = "ResponseTime"
	// MartianSessionKey is the context key to store the martian session (*martian.Session). This is used to hijack connection and control the response
	MartianSessionKey contextKey = "SessionKey"
)

// ContextWithSession returns a new request with a martian session in the context
func ContextWithSession(req *http.Request, session *martian.Session) *http.Request {
	ctx := context.WithValue(req.Context(), MartianSessionKey, session)
	return req.WithContext(ctx)
}

// SessionFromContext returns the martian session from the context if it exists
func SessionFromContext(ctx context.Context) (*martian.Session, bool) {
	session, ok := ctx.Value(MartianSessionKey).(*martian.Session)
	return session, ok
}

// ContextWithRequestID returns a new request with a request ID in the context
func ContextWithRequestID(req *http.Request, requestId uuid.UUID) *http.Request {
	ctx := context.WithValue(req.Context(), RequestIDKey, requestId)
	return req.WithContext(ctx)
}

// RequestIDFromContext returns the request ID from the context if it exists
func RequestIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(RequestIDKey).(uuid.UUID)
	return id, ok
}

// ContextWithLaunchpadID returns a new request with the launchpad ID in the context
func ContextWithLaunchpadID(req *http.Request, launchpadId uuid.UUID) *http.Request {
	ctx := context.WithValue(req.Context(), LaunchpadIDKey, launchpadId)
	return req.WithContext(ctx)
}

// LaunchpadIDFromContext returns the launchpad ID from the context if it exists
func LaunchpadIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(LaunchpadIDKey).(uuid.UUID)
	return id, ok
}

// ContextWithMetadata returns a new request with metadata in the context
func ContextWithMetadata(req *http.Request, metadata Metadata) *http.Request {
	ctx := context.WithValue(req.Context(), MetadataKey, metadata)
	return req.WithContext(ctx)
}

// MetadataFromContext returns the metadata from the context if it exists
func MetadataFromContext(ctx context.Context) (Metadata, bool) {
	metadata, ok := ctx.Value(MetadataKey).(Metadata)
	return metadata, ok
}

// ContextWithExtensionID returns a new request with the extension ID in the context
func ContextWithExtensionID(req *http.Request, extensionId string) *http.Request {
	ctx := context.WithValue(req.Context(), ExtensionKey, extensionId)
	return req.WithContext(ctx)
}

// ExtensionIDFromContext returns the extension ID from the context if it exists
func ExtensionIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ExtensionKey).(string)
	return id, ok
}

// ContextWithInterceptFlag returns a new request with the intercept flag in the context
func ContextWithInterceptFlag(req *http.Request, shouldIntercept bool) *http.Request {
	ctx := context.WithValue(req.Context(), ShouldInterceptKey, shouldIntercept)
	return req.WithContext(ctx)
}

// InterceptFlagFromContext returns the intercept flag value from the context if it exists
func InterceptFlagFromContext(ctx context.Context) (bool, bool) {
	interceptFlag, ok := ctx.Value(ShouldInterceptKey).(bool)
	return interceptFlag, ok
}

// ContextWithRequestTime returns a new request with the request time in the context
func ContextWithRequestTime(req *http.Request, requestTime time.Time) *http.Request {
	ctx := context.WithValue(req.Context(), RequestTimeKey, requestTime)
	return req.WithContext(ctx)
}

// RequestTimeFromContext returns the request time from the context if it exists
func RequestTimeFromContext(ctx context.Context) (time.Time, bool) {
	timestamp, ok := ctx.Value(RequestTimeKey).(time.Time)
	return timestamp, ok
}

// ContextWithResponseTime returns a new request with the response time in the context
func ContextWithResponseTime(req *http.Request, responseTime time.Time) *http.Request {
	ctx := context.WithValue(req.Context(), ResponseTimeKey, responseTime)
	return req.WithContext(ctx)
}

// ResponseTimeFromContext returns the response time from the context if it exists
func ResponseTimeFromContext(ctx context.Context) (time.Time, bool) {
	timestamp, ok := ctx.Value(ResponseTimeKey).(time.Time)
	return timestamp, ok
}

// ContextWithSkipFlag returns a new request with the skipped flag in the context
func ContextWithSkipFlag(req *http.Request, skip bool) *http.Request {
	ctx := context.WithValue(req.Context(), SkipKey, skip)
	return req.WithContext(ctx)
}

// SkipFlagFromContext returns the value of the skipped flag from the context if it exists
func SkipFlagFromContext(ctx context.Context) (bool, bool) {
	skip, ok := ctx.Value(SkipKey).(bool)
	return skip, ok
}

// ContextWithDropFlag returns a new request with the dropped flag in the context
func ContextWithDropFlag(req *http.Request, drop bool) *http.Request {
	ctx := context.WithValue(req.Context(), DropKey, drop)
	return req.WithContext(ctx)
}

// DroppedFlagFromContext returns the value of the dropped flag from the context if it exists
func DroppedFlagFromContext(ctx context.Context) (bool, bool) {
	dropped, ok := ctx.Value(DropKey).(bool)
	return dropped, ok
}
