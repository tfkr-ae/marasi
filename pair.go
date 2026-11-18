package marasi

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
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
	// ExtensionID is the extension that sent the request, or uuid.Nil. That
	// extension's hooks do not run on its own pair.
	ExtensionID uuid.UUID
	// Skipped is set when an extension skipped the pair: it goes on with no
	// further extension or Checkpoint and is not stored.
	Skipped bool
	// InterceptResponse is set when Checkpoint was asked, while holding the
	// request, to hold the response too.
	InterceptResponse bool
	// ArmoryRunID is the Armory run that made the request, or uuid.Nil.
	ArmoryRunID uuid.UUID
	// RequestBodyEnding is how the proxy's read of the request body ended.
	RequestBodyEnding BodyEnding
	// ResponseBodyEnding is how the proxy's read of the response body
	// ended, BodyUnread on the request chain.
	ResponseBodyEnding BodyEnding

	// webSocketUpstream is the upstream stream of a successful upgrade,
	// detached from the response body by WebSocketPrepareModifier.
	webSocketUpstream io.ReadWriteCloser
	// webSocketNegotiation is the extension response mirrored to the client.
	webSocketNegotiation *webSocketNegotiation
	// webSocketFailed is set when the proxy dropped an upgrade whose
	// handshake it could not complete, as opposed to one a user or extension
	// dropped.
	webSocketFailed bool
	// release ends the response side's project admission. A WebSocket
	// session calls it once registered.
	release func()

	// ctx is used only for cancellation.
	ctx context.Context

	// control is how a modifier ended the chain, if one did.
	control control
	// fromMarasi is set when Response is one Marasi made, not the origin's.
	fromMarasi bool
	// proxyError is the cause of the proxy error response Marasi answered
	// the pair with, empty for any other response.
	proxyError string
	// requestCaptured is set once the request is queued for storage.
	requestCaptured bool

	// requestWire and responseWire are the messages as the engine read
	// them from the wire, nil when the engine keeps no wire form (Martian)
	// or Marasi made the response. Request and Response are views of them.
	requestWire  *wireRequest
	responseWire *wireResponse
	// decoded is what CompressedResponseModifier undid, nil when it decoded
	// nothing. The h1 engine sends the origin's encoded body while the
	// view's body still equals the decoded one.
	decoded *decodedResponse

	// requestState is what the proxy knows of the request before the chain
	// runs, and what the request chain leaves for the proxy.
	requestState
}

// requestState is the request-side state of a pair that only the proxy reads.
type requestState struct {
	// admission is how the request side was admitted, nil outside the proxy.
	admission *projectAdmission
	// requestedLaunch is the launch an internal request named, or uuid.Nil.
	requestedLaunch uuid.UUID
	// removedWebSocketExtensions are the extension offers removed from an
	// upgrade request.
	removedWebSocketExtensions []string
	// client is the connection the request came in on, which the proxy
	// takes over to close a dropped response or to hand off an upgrade.
	client clientHijacker
}

// clientHijacker takes over the client connection of a pair from the engine
// that serves it. After Hijack, the engine no longer reads or writes it.
type clientHijacker interface {
	Hijack() (net.Conn, *bufio.ReadWriter, error)
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

// control is how a modifier ended a chain.
type control int

const (
	// controlNone: the chain goes on.
	controlNone control = iota
	// controlStop: the pair goes on with no further modifier in this chain.
	controlStop
	// controlDrop: the pair goes no further.
	controlDrop
	// controlRespond: Marasi answers the pair with its own response.
	controlRespond
)

// Respond ends the chain and answers the pair with res, which Marasi made. On
// the request chain the request never goes upstream and no response modifier
// runs; on the response chain res replaces the origin's response. A res
// without a request is given the pair's.
func (pair *Pair) Respond(res *http.Response) {
	if res.Request == nil {
		res.Request = pair.Request
	}
	pair.Response = res
	pair.fromMarasi = true
	pair.control = controlRespond
}

// Drop ends the chain and the pair: a request does not go upstream, and a
// response does not reach the client, whose connection is closed.
func (pair *Pair) Drop() {
	pair.control = controlDrop
}

// Stop ends the chain. The pair goes on, with no further modifier in this
// chain.
func (pair *Pair) Stop() {
	pair.control = controlStop
}

// Dropped reports whether a modifier dropped the pair.
func (pair *Pair) Dropped() bool {
	return pair.control == controlDrop
}

// FromMarasi reports whether the pair's response is one Marasi made through
// Respond, rather than one the origin sent.
func (pair *Pair) FromMarasi() bool {
	return pair.fromMarasi
}

// ended reports whether a modifier ended the chain with a control.
func (pair *Pair) ended() bool {
	return pair.control != controlNone
}

// PairModifier edits a pair on the request chain or the response chain. It
// ends the chain with a control on the pair (Respond, Drop or Stop). A
// returned error means only that something broke, and also stops the chain.
type PairModifier func(proxy *Proxy, pair *Pair) error

// ModifierGroup runs pair modifiers in the order they were added.
type ModifierGroup struct {
	mu        sync.RWMutex
	modifiers []PairModifier
}

// NewModifierGroup returns an empty modifier group.
func NewModifierGroup() *ModifierGroup {
	return &ModifierGroup{}
}

// Add appends modifier to the end of the group.
func (group *ModifierGroup) Add(modifier PairModifier) {
	group.mu.Lock()
	defer group.mu.Unlock()
	group.modifiers = append(group.modifiers, modifier)
}

// Modify runs every modifier in the order added. It stops at the first error,
// which it returns, or at the first control a modifier sets on the pair.
func (group *ModifierGroup) Modify(proxy *Proxy, pair *Pair) error {
	group.mu.RLock()
	modifiers := group.modifiers
	group.mu.RUnlock()
	for _, modifier := range modifiers {
		if err := modifier(proxy, pair); err != nil {
			return err
		}
		if pair.ended() {
			return nil
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
