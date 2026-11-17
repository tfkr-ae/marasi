package marasi

import (
	"context"
	"net/http"
	"reflect"
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
	// In place: Martian finds its context by the request's address.
	if out != req {
		*req = *out
	}
	pair.Request = req
	pair.ctx = req.Context()
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
		return err
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
		return err
	})
}
