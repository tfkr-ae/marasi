package marasi

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
)

// Pair is a request/response pair in flight: one request and its matching
// response, identified by one UUID from the moment the proxy reads the request.
// Modifiers read and edit its typed fields instead of values in a context.
type Pair struct {
	// Request is the request as it will go upstream.
	Request *http.Request
	// Response is the response, nil on the request chain.
	Response *http.Response
	// ID is the pair UUID. It is uuid.Nil until the pair is set up.
	ID uuid.UUID
	// Metadata is the document stored with the pair.
	Metadata map[string]any
	// Timing records when the request and the response were read.
	Timing PairTiming
	// Route collects the route the request went out on.
	Route *core.RouteRecord
	// LaunchpadID is the launchpad a launch came from, or uuid.Nil.
	LaunchpadID uuid.UUID
	// ArmoryRunID is the Armory run that made the request, or uuid.Nil.
	ArmoryRunID uuid.UUID

	// webSocketUpstream is the upstream stream of a successful upgrade,
	// detached from the response body by WebSocketPrepareModifier.
	webSocketUpstream io.ReadWriteCloser
	// webSocketNegotiation is the extension response mirrored to the client.
	webSocketNegotiation *webSocketNegotiation
	// release ends the response side's project admission. A WebSocket
	// session calls it once registered.
	release func()

	// ctx is used only for cancellation.
	ctx context.Context
}

// PairTiming records when each half of a pair was read.
type PairTiming struct {
	Requested time.Time
	Responded time.Time
}

// Context returns the pair's context. It is done when the client leaves or the
// proxy stops, and carries no pair values.
func (pair *Pair) Context() context.Context {
	if pair.ctx == nil {
		return context.Background()
	}
	return pair.ctx
}

// PairModifier edits a pair on the request chain or the response chain. A
// returned error stops the chain.
type PairModifier func(proxy *Proxy, pair *Pair) error

// ModifierGroup runs pair modifiers in the order they were added.
type ModifierGroup struct {
	modifiers []PairModifier
}

// NewModifierGroup returns an empty modifier group.
func NewModifierGroup() *ModifierGroup {
	return &ModifierGroup{}
}

// Add appends modifier to the end of the group.
func (group *ModifierGroup) Add(modifier PairModifier) {
	group.modifiers = append(group.modifiers, modifier)
}

// Modify runs every modifier in the order added and stops at the first error,
// which it returns.
func (group *ModifierGroup) Modify(proxy *Proxy, pair *Pair) error {
	for _, modifier := range group.modifiers {
		if err := modifier(proxy, pair); err != nil {
			return err
		}
	}
	return nil
}

// NewTestPair builds a pair around req for modifier unit tests, as the proxy
// would set it up: a new pair UUID, empty metadata, the request time and an
// empty route record. Tests set any other field directly.
func NewTestPair(req *http.Request) *Pair {
	return &Pair{
		Request:  req,
		ID:       uuid.Must(uuid.NewV7()),
		Metadata: map[string]any{},
		Timing:   PairTiming{Requested: time.Now()},
		Route:    &core.RouteRecord{},
		ctx:      req.Context(),
	}
}
