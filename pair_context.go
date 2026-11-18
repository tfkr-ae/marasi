package marasi

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
)

// The pair modifiers keep the pair's values in the request context too, for
// the code that still reads them there: the transport's route record, the
// launch failure log and the Lua response hooks. Before a pair modifier runs,
// its pair is loaded from the context; after, what it changed is written back.
// It goes away once nothing reads the context.

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

// loadPair loads pair from the context of req, with res as its response, and
// returns the values loaded, for storePair to compare against. A pair the
// context does not hold yet is put there first, with every value it has.
func loadPair(pair *Pair, req *http.Request, res *http.Response) pairValues {
	if req.Context().Value(pairKey{}) != pair {
		storePair(pair, pairValues{}, req)
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
	return values
}

// storePair writes the pair back onto req in place, with the fields a
// modifier changed since loaded put in its context. In place, because the
// engine and the response side find the pair through req's address.
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

// AddRequestPairModifier adds modifier to the end of the request chain.
func (proxy *Proxy) AddRequestPairModifier(modifier PairModifier) {
	proxy.requestModifiers.Add(func(proxy *Proxy, pair *Pair) error {
		req := pair.Request
		loaded := loadPair(pair, req, nil)
		err := modifier(proxy, pair)
		storePair(pair, loaded, req)
		return err
	})
}

// AddResponsePairModifier adds modifier to the end of the response chain.
func (proxy *Proxy) AddResponsePairModifier(modifier PairModifier) {
	proxy.responseModifiers.Add(func(proxy *Proxy, pair *Pair) error {
		req, res := pair.Request, pair.Response
		loaded := loadPair(pair, req, res)
		err := modifier(proxy, pair)
		// The engine writes the response it handed to the chain, so a
		// replaced response is copied into it.
		if pair.Response != nil && pair.Response != res {
			*res = *pair.Response
			res.Request = req
		}
		storePair(pair, loaded, req)
		pair.Response = res
		return err
	})
}
