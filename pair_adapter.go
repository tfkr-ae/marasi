package marasi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
)

// This file is the expand step of the move to the pair: pair modifiers run in
// the same Martian chain as the old modifiers, which still keep the pair's
// values in the request context. Before a pair modifier runs, its pair is
// loaded from the context; after, what it changed is written back, so either
// kind sees the other's edits. It goes away once no modifier reads the context.

// pairKey holds a request's in-flight *Pair, so both chains share one.
type pairKey struct{}

// pairValues are the pair fields the old modifiers keep in the context.
type pairValues struct {
	id          uuid.UUID
	metadata    map[string]any
	timing      PairTiming
	route       *core.RouteRecord
	launchpadID uuid.UUID
	armoryRunID uuid.UUID

	extensionID       uuid.UUID
	skipped           bool
	interceptResponse bool
	removedOffers     []string
}

// values returns the context-backed fields of pair.
func (pair *Pair) values() pairValues {
	return pairValues{
		id:          pair.ID,
		metadata:    pair.Metadata,
		timing:      pair.Timing,
		route:       pair.Route,
		launchpadID: pair.LaunchpadID,
		armoryRunID: pair.ArmoryRunID,

		extensionID:       pair.ExtensionID,
		skipped:           pair.Skipped,
		interceptResponse: pair.InterceptResponse,
		removedOffers:     pair.removedWebSocketExtensions,
	}
}

// contextPairValues reads the pair fields the old modifiers keep in ctx.
func contextPairValues(ctx context.Context) pairValues {
	var values pairValues
	values.id, _ = core.RequestIDFromContext(ctx)
	values.metadata, _ = core.MetadataFromContext(ctx)
	values.timing.Requested, _ = core.RequestTimeFromContext(ctx)
	values.timing.Responded, _ = core.ResponseTimeFromContext(ctx)
	values.route, _ = core.RouteRecordFromContext(ctx)
	values.launchpadID, _ = core.LaunchpadIDFromContext(ctx)
	values.armoryRunID, _ = core.ArmoryRunIDFromContext(ctx)

	if id, ok := core.ExtensionIDFromContext(ctx); ok {
		values.extensionID, _ = uuid.Parse(id)
	}
	values.skipped, _ = core.SkipFlagFromContext(ctx)
	values.interceptResponse, _ = core.InterceptFlagFromContext(ctx)
	values.removedOffers, _ = ctx.Value(webSocketRemovedExtensionsKey{}).([]string)
	return values
}

// loadPair returns the pair of req, loaded from its context, with res as its
// response. It returns the values loaded, for storePair to compare against.
func loadPair(req *http.Request, res *http.Response) (*Pair, pairValues) {
	pair, ok := req.Context().Value(pairKey{}).(*Pair)
	if !ok {
		pair = &Pair{}
	}
	values := contextPairValues(req.Context())
	pair.Request = req
	pair.Response = res
	pair.ID = values.id
	pair.Metadata = values.metadata
	pair.Timing = values.timing
	pair.Route = values.route
	pair.LaunchpadID = values.launchpadID
	pair.ArmoryRunID = values.armoryRunID

	pair.ExtensionID = values.extensionID
	pair.Skipped = values.skipped
	pair.InterceptResponse = values.interceptResponse
	pair.removedWebSocketExtensions = values.removedOffers
	pair.ctx = req.Context()
	return pair, values
}

// storePair writes the pair back onto the Martian request in place, with the
// fields a modifier changed since loaded put in its context.
func storePair(pair *Pair, loaded pairValues, req *http.Request) {
	out := req
	if pair.Request != nil && pair.Request != req {
		// The context is the proxy's, not the modifier's: a replaced request
		// keeps the cancellation and state the proxy put on the original.
		out = pair.Request.WithContext(req.Context())
	}
	if out.Context().Value(pairKey{}) != pair {
		out = out.WithContext(context.WithValue(out.Context(), pairKey{}, pair))
	}
	now := pair.values()
	if now.id != loaded.id && now.id != uuid.Nil {
		out = core.ContextWithRequestID(out, now.id)
	}
	if now.metadata != nil && !sameMap(now.metadata, loaded.metadata) {
		out = core.ContextWithMetadata(out, now.metadata)
	}
	if setTime(now.timing.Requested, loaded.timing.Requested) {
		out = core.ContextWithRequestTime(out, now.timing.Requested)
	}
	if setTime(now.timing.Responded, loaded.timing.Responded) {
		out = core.ContextWithResponseTime(out, now.timing.Responded)
	}
	if now.route != nil && now.route != loaded.route {
		out = core.ContextWithRouteRecord(out, now.route)
	}
	if now.launchpadID != loaded.launchpadID && now.launchpadID != uuid.Nil {
		out = core.ContextWithLaunchpadID(out, now.launchpadID)
	}
	if now.armoryRunID != loaded.armoryRunID && now.armoryRunID != uuid.Nil {
		out = core.ContextWithArmoryRunID(out, now.armoryRunID)
	}
	if now.extensionID != loaded.extensionID && now.extensionID != uuid.Nil {
		out = core.ContextWithExtensionID(out, now.extensionID.String())
	}
	// A flag is written back when it changed either way: Compass clears
	// the skip of a launch.
	if now.skipped != loaded.skipped {
		out = core.ContextWithSkipFlag(out, now.skipped)
	}
	if now.interceptResponse != loaded.interceptResponse {
		out = core.ContextWithInterceptFlag(out, now.interceptResponse)
	}
	if now.removedOffers != nil && !slices.Equal(now.removedOffers, loaded.removedOffers) {
		out = out.WithContext(context.WithValue(out.Context(), webSocketRemovedExtensionsKey{}, now.removedOffers))
	}
	// In place: Martian finds its context by the request's address.
	if out != req {
		*req = *out
	}
	pair.Request = req
	pair.ctx = req.Context()
}

// requestPair returns the pair the proxy keeps for req, or nil when req did not
// come through the proxy's base pipeline.
func requestPair(req *http.Request) *Pair {
	if req == nil {
		return nil
	}
	pair, _ := req.Context().Value(pairKey{}).(*Pair)
	return pair
}

// sameMap reports whether a and b are the same map.
func sameMap(a, b map[string]any) bool {
	return reflect.ValueOf(a).UnsafePointer() == reflect.ValueOf(b).UnsafePointer()
}

// setTime reports whether a modifier set a new time.
func setTime(now, loaded time.Time) bool {
	return !now.IsZero() && !now.Equal(loaded)
}

// AddRequestPairModifier adds modifier to the end of the request chain, after
// every modifier added before it, old or new.
func (proxy *Proxy) AddRequestPairModifier(modifier PairModifier) {
	proxy.AddRequestModifier(func(proxy *Proxy, req *http.Request) error {
		pair, loaded := loadPair(req, nil)
		err := modifier(proxy, pair)
		storePair(pair, loaded, req)
		return endChain(pair, err)
	})
}

// AddResponsePairModifier adds modifier to the end of the response chain,
// after every modifier added before it, old or new.
func (proxy *Proxy) AddResponsePairModifier(modifier PairModifier) {
	proxy.AddResponseModifier(func(proxy *Proxy, res *http.Response) error {
		pair, loaded := loadPair(res.Request, res)
		err := modifier(proxy, pair)
		if pair.Response != nil && pair.Response != res {
			request := res.Request
			*res = *pair.Response
			res.Request = request
		}
		storePair(pair, loaded, res.Request)
		pair.Response = res
		return endChain(pair, err)
	})
}

// errChainEnded stops Martian's group after a pair modifier set a control.
// The base pipeline reads the control from the pair, not from the error.
var errChainEnded = errors.New("chain ended by a control on the pair")

// endChain returns err, or, when a modifier set a control on pair, the error
// that stops Martian's group there. A drop stops it with ErrDropped, which
// marasi-app's wrapper of Proxy.Modifiers still reads to announce a dropped
// WebSocket upgrade.
func endChain(pair *Pair, err error) error {
	if err != nil || !pair.ended() {
		return err
	}
	if pair.Dropped() {
		return ErrDropped
	}
	return errChainEnded
}

// chainError returns what is left of err, the error Martian's group returned
// for pair, once the control signal endChain sent is taken out: nil when the
// chain ended by a control, err otherwise.
func chainError(pair *Pair, err error) error {
	if pair != nil && pair.ended() && (errors.Is(err, errChainEnded) || errors.Is(err, ErrDropped)) {
		return nil
	}
	return err
}

// setRoundTripper makes base Martian's round tripper, behind the step that
// returns a response from Respond. Everything that sets Martian's round
// tripper goes through it, so Respond works whatever the transport.
func (proxy *Proxy) setRoundTripper(base http.RoundTripper) {
	proxy.martianProxy.SetRoundTripper(&respondRoundTripper{proxy: proxy, base: base})
}

// respondRoundTripper returns the response a request modifier answered a
// pair with through Respond, in place of a round trip. Every other request
// goes to base. A round trip that gets no response is answered with a proxy
// error response, never with an error: Martian would answer that with an
// empty 502 carrying a Warning header.
type respondRoundTripper struct {
	proxy *Proxy
	base  http.RoundTripper
}

// RoundTrip satisfies http.RoundTripper.
func (rt *respondRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	pair := requestPair(req)
	if pair != nil && pair.control == controlRespond {
		return pair.Response, nil
	}
	res, err := rt.base.RoundTrip(req)
	if err == nil {
		return res, nil
	}
	if pair == nil {
		res := plainTextResponse(req, http.StatusBadGateway, err.Error())
		res.Close = true
		return res, nil
	}
	rt.proxy.respondWithProxyError(pair, http.StatusBadGateway, err)
	rt.proxy.logProxyError(pair, err)
	return pair.Response, nil
}
