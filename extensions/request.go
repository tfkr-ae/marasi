package extensions

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
)

// Request is the `req` a Lua hook receives: the HTTP request, and the values
// of its pair that Lua reads and sets. The caller copies Drop and Skip back to
// the pair after the hook. Metadata is the pair's own map, edited in place.
type Request struct {
	Request *http.Request
	// ID is the pair UUID, or uuid.Nil before the pair is set up.
	ID uuid.UUID
	// Metadata is the pair's metadata, or nil before the pair is set up.
	Metadata map[string]any
	// Drop is set when the hook asked for the request to be dropped.
	Drop bool
	// Skip is set when the hook asked for the request to be skipped.
	Skip bool

	// ofResponse is set for the request a response hook reached through
	// res:request(). Its flags are still kept in the request context, which
	// the response modifiers read.
	ofResponse bool
}

// requestOfResponse returns the `req` of res's request, read from its context.
func requestOfResponse(res *http.Response) *Request {
	req := &Request{Request: res.Request, ofResponse: true}
	req.ID, _ = core.RequestIDFromContext(res.Request.Context())
	req.Metadata, _ = core.MetadataFromContext(res.Request.Context())
	return req
}

// drop marks req to be dropped.
func (req *Request) drop() {
	req.Drop = true
	if req.ofResponse {
		*req.Request = *core.ContextWithDropFlag(req.Request, true)
	}
}

// skip marks req to be skipped.
func (req *Request) skip() {
	req.Skip = true
	if req.ofResponse {
		*req.Request = *core.ContextWithSkipFlag(req.Request, true)
	}
}
