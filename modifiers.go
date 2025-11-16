package marasi

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
)

var (
	// ErrDropped is returned when the request / response should be dropped completely.
	// The request / response will not be processed by any modifier and will not continue to the client / server
	ErrDropped = errors.New("item dropped by extension or user")

	// ErrSkipPipeline is returned to stop the modifier pipeline for a request / response.
	// The request / response will still continue but won't be processed by any future modifiers
	ErrSkipPipeline = errors.New("stop processing item")

	// ErrMetadata is returned when metadata is invalid or missing
	ErrMetadataNotFound = errors.New("invalid or missing metadata")

	// ErrExtensionNotFound is returned when extension is invalid or missing
	ErrExtensionNotFound = errors.New("invalid or missing extension")

	// ErrRequestIDNotFound is returned when requestID is not found
	ErrRequestIDNotFound = errors.New("invalid or missing requestID")

	// ErrRebuildRequest is returned when the request is malformed and cannot be rebuilt
	ErrRebuildRequest = errors.New("cannot rebuild request")

	// ErrRebuildResponse is returned when the response is malformed and cannot be rebuilt
	ErrRebuildResponse = errors.New("cannot rebuild response")

	// ErrRequestHandlerUndefined is returned when the request handler is undefined
	ErrRequestHandlerUndefined = errors.New("no request handler defined")

	// ErrResponseHandlerUndefined is returned when the response handler is undefined
	ErrResponseHandlerUndefined = errors.New("no response handler defined")

	// ErrProxyRequest is returned when ProxyRequest cannot be created
	ErrProxyRequest = errors.New("failed to create proxyrequest")

	// ErrProxyResponse is returned when ErrProxyResponse cannot be created
	ErrProxyResponse = errors.New("failed to create proxyresponse")

	// ErrReadBody is returned when there is an error with reading the response body
	ErrReadBody = errors.New("failed to read the body")
)

// RequestModifierFunc is a signature for HTTP request modifiers, it takes in the request and *Proxy
type RequestModifierFunc func(proxy *Proxy, req *http.Request) error

// ResponseModifierFunc is a signature for HTTP response modifiers, it takes in the response and *Proxy
type ResponseModifierFunc func(proxy *Proxy, res *http.Response) error

// reqAdapter adapts the `RequestModifierFunc` and implements the `martian.RequestModifier` interface.
// This allows custom modifiers to be added with access to the *Proxy while satisfying the `martian.RequestModifier` interface
type reqAdapter struct {
	proxy    *Proxy
	modifier RequestModifierFunc
}

// ModifyRequest implements the `martian.RequestModifier` interface and allows the modifier to access the *Proxy
func (adapter *reqAdapter) ModifyRequest(req *http.Request) error {
	return adapter.modifier(adapter.proxy, req)
}

// resAdapter adapts the `ResponseModifierFunc` and implements the `martian.ResponseModifier` interface.
// This allows custom modifiers to be added with access to the *Proxy while satisfying the `martian.ResponseModifier` interface
type resAdapter struct {
	proxy    *Proxy
	modifier ResponseModifierFunc
}

// ModifyResponse implements the `martian.ResponseModifier` interface and allows the modifier to access the *Proxy
func (adapter *resAdapter) ModifyResponse(res *http.Response) error {
	return adapter.modifier(adapter.proxy, res)
}

// martianReqModifierFunc allows functions with "func(*http.Request) error" signature to satisfy the `martian.RequestModifier` interface
type martianReqModifierFunc func(*http.Request) error

// ModifyRequest implements the `martian.RequestModifier` interface
func (f martianReqModifierFunc) ModifyRequest(req *http.Request) error {
	return f(req)
}

// martianResModifierFunc allows functions with "func(*http.Response) error" signature to satisfy the `martian.ResponseModifier` interface
type martianResModifierFunc func(*http.Response) error

// ModifyResponse implements the `martian.ResponseModifier` interface
func (f martianResModifierFunc) ModifyResponse(res *http.Response) error {
	return f(res)
}

// getHostPort will return a host:port string based on the request
// It will fall back to 443 or 80 depending on the scheme or req.TLS
func getHostPort(req *http.Request) string {
	hostPort := req.URL.Host
	if hostPort == "" {
		hostPort = req.Host
	}

	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		// If port is missing, use default
		host = hostPort
		if req.URL.Scheme == "https" || req.TLS != nil {
			port = "443"
		} else {
			port = "80"
		}
	}

	return net.JoinHostPort(host, port)
}

// PreventLoopModifier skips processing a request if it is made to marasi's active listener address and port, preventing an infinite loop
// It will normalize localhost & 127.0.0.1 when checking the host and port
func PreventLoopModifier(proxy *Proxy, req *http.Request) error {
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		// fallback to req.Host
		host = req.Host

		// if net.SplitHostPort fails the fallback is either 443 or 80 depending on the URL scheme or req.TLS
		if req.URL.Scheme == "https" || req.TLS != nil {
			port = "443"
		} else {
			port = "80"
		}
	}

	if host == "localhost" {
		host = "127.0.0.1"
	}

	listenerAddr := proxy.Addr
	if listenerAddr == "localhost" {
		listenerAddr = "127.0.0.1"
	}

	if host == listenerAddr && port == proxy.Port {
		martian.NewContext(req).SkipRoundTrip()
		return ErrSkipPipeline
	}
	return nil
}

// SkipConnectRequestModifier will skip processing for CONNECT requests
func SkipConnectRequestModifier(proxy *Proxy, req *http.Request) error {
	if req.Method == http.MethodConnect {
		return ErrSkipPipeline
	}
	return nil
}

// SetupRequestModifier initializes the request context. It will generate and set the request ID,
// set the request time, initial and set the metadata map, and stores the Martian session. If the request is coming
// from launchpad, it will set the launchapd ID in the context
func SetupRequestModifier(proxy *Proxy, req *http.Request) error {
	*req = *core.ContextWithRequestTime(req, time.Now())
	metadata := make(map[string]any)
	uuid, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generating uuid for request : %w", err)
	}

	// Requests coming from launchpad will have x-launchpad-id set as a header
	if isLaunchpad, launchpadId := IsLaunchpad(req); isLaunchpad {
		metadata["launchpad"] = true
		metadata["launchpad_id"] = launchpadId
		*req = *core.ContextWithLaunchpadID(req, launchpadId)

		// Header is removed after processign
		req.Header.Del("x-launchpad-id")
	}

	*req = *core.ContextWithRequestID(req, uuid)
	*req = *core.ContextWithMetadata(req, metadata)

	ctx := martian.NewContext(req)
	session := ctx.Session()
	*req = *core.ContextWithSession(req, session)
	return nil
}

// OverrideWaypointsModifier checks if a Waypoint (host override) is defined for this host:port.
// If a waypoint exists it will write the "original_host" and "override_host" to the metadata.
// These values are used later in the `DialContext` function. If the metadata is not found
// the modifier will return `ErrMetadataNotFound`
// TODO should allow TLS -> Non TLS override
func OverrideWaypointsModifier(proxy *Proxy, req *http.Request) error {
	if metadata, ok := core.MetadataFromContext(req.Context()); ok {
		if override, ok := proxy.Waypoints[getHostPort(req)]; ok {
			metadata["original_host"] = getHostPort(req)
			metadata["override_host"] = override
			*req = *core.ContextWithMetadata(req, metadata)

			req.URL.Host = override
			req.Host = override
		}
		return nil
	}
	return ErrMetadataNotFound
}

// CompassRequestModifier will run the `processRequest` function in the compass extension to determine if the request is in scope.
// After `processRequest`, it will check if the request is passed through (nil), skipped (`ErrSkipPipeline`), or dropped (`ErrDropped`).
// If the compass extension is not found the modifier will return `ErrExtensionNotFound` as "compass" is considered a core extension.
func CompassRequestModifier(proxy *Proxy, req *http.Request) error {
	if compassExt, ok := proxy.GetExtension("compass"); ok {
		compassExt.Mu.Lock()
		defer compassExt.Mu.Unlock()
		if compassExt.CheckGlobalFunction("processRequest") {
			err := compassExt.CallRequestHandler(req)
			if err != nil {
				proxy.WriteLog("ERROR", fmt.Sprintf("Running processRequest : %s", err.Error()), core.LogWithExtensionID(compassExt.Data.ID))
				// Continue as a err in Lua should not bring down the proxy
			}
			if skip, ok := core.SkipFlagFromContext(req.Context()); ok && skip {
				return ErrSkipPipeline
			}

			if dropped, ok := core.DroppedFlagFromContext(req.Context()); ok && dropped {
				martian.NewContext(req).SkipRoundTrip()
				return ErrDropped
			}
		}
		return nil
	}
	return ErrExtensionNotFound
}

// ExtensionsRequestModifier will run the `processRequest` function (if it is defined) for all the loaded extensions (except compass and checkpoint).
// Initially the modifier will check if the request originated from an extension by reading the "x-extension-id" header. This extension ID
// will be set in the context so that the response modifier will be able to read it.
// After processRequest, it will check if the request is passed through (nil), skipped (`ErrSkipPipeline`), or dropped (`ErrDropped`).
func ExtensionsRequestModifier(proxy *Proxy, req *http.Request) error {
	extensionID := req.Header.Get("x-extension-id")
	*req = *core.ContextWithExtensionID(req, extensionID)

	// header is removed after processing
	req.Header.Del("x-extension-id")

	for _, ext := range proxy.Extensions {
		if ext.Data.Name != "checkpoint" && ext.Data.Name != "compass" {
			if extensionID != ext.Data.ID.String() {
				ext.Mu.Lock()
				defer ext.Mu.Unlock()
				if ext.CheckGlobalFunction("processRequest") {
					err := ext.CallRequestHandler(req)
					if err != nil {
						proxy.WriteLog("ERROR", fmt.Sprintf("Running processRequest : %s", err.Error()), core.LogWithExtensionID(ext.Data.ID))
						// Continue as a err in Lua should not bring down the proxy
					}

					if skip, ok := core.SkipFlagFromContext(req.Context()); ok && skip {
						return ErrSkipPipeline
					}

					if dropped, ok := core.DroppedFlagFromContext(req.Context()); ok && dropped {
						martian.NewContext(req).SkipRoundTrip()
						return ErrDropped
					}

				}
			}
		}
	}
	return nil
}

// CheckpointRequestModifier will intercept requests if the global `proxy.InterceptFlag` is set or the `interceptRequest` function returns true.
// If a request is intercepted, the modifier will block until the user decides to resume or drop the request. If the request is resumed it will be
// rebuilt with the same context and metadata from the modified raw request. The metadata will be updated to include "intercepted", "original-request", and "dropped" based
// on the user action. If the modifier receives `ShouldInterceptResponse` the flag is added to the context so that the
// response is intercepted regardless of the `processResponse` or `proxy.InterceptFlag`
func CheckpointRequestModifier(proxy *Proxy, req *http.Request) error {
	if checkpointExt, ok := proxy.GetExtension("checkpoint"); ok {
		shouldIntercept, err := checkpointExt.ShouldInterceptRequest(req)
		if err != nil {
			if reqID, ok := core.RequestIDFromContext(req.Context()); ok {
				proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptRequest : %s", err.Error()), core.LogWithReqResID(reqID))
			} else {
				proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptRequest : %s", err.Error()))
			}
			return nil
		}

		if shouldIntercept || proxy.InterceptFlag {
			original, err := httputil.DumpRequest(req, true)
			if err != nil {
				return fmt.Errorf("getting raw request for intercept : %w", err)
			}

			interceptedRequest := Intercepted{
				Type:    "request",
				Raw:     string(original),
				Channel: make(chan InterceptionTuple),
			}
			proxy.InterceptedQueue = append(proxy.InterceptedQueue, &interceptedRequest)

			// TODO return different error?
			if proxy.OnIntercept == nil {
				proxy.WriteLog("ERROR", "Request intercepted but OnIntercept is not defined. Dropping request")
				martian.NewContext(req).SkipRoundTrip()
				return ErrDropped
			}

			proxy.OnIntercept(&interceptedRequest)

			userAction := <-interceptedRequest.Channel

			if metadata, ok := core.MetadataFromContext(req.Context()); ok {
				metadata["intercepted"] = true
				metadata["original-request"] = string(original)
				if !userAction.Resume {
					metadata["dropped"] = true
				}
				*req = *core.ContextWithMetadata(req, metadata)
			} else {
				return ErrMetadataNotFound
			}

			if !userAction.Resume {
				martian.NewContext(req).SkipRoundTrip()
				return ErrDropped
			}

			if userAction.ShouldInterceptResponse {
				*req = *core.ContextWithInterceptFlag(req, true)
			}

			rebuiltReq, err := rawhttp.RebuildRequest([]byte(interceptedRequest.Raw), req)
			if err != nil {
				return fmt.Errorf("%w : %w", ErrRebuildRequest, err)
			}

			*req = *rebuiltReq

			return nil
		}
		return nil
	}
	return ErrExtensionNotFound
}

// WriteRequestModifier is the final modifier in the default request pipeline.
// It will create a `ProxyRequest` struct and queue it for database insertion.
// If the request came from launchpad, it will create a `LaunchpadRequest` struct and queue it for database insertion as well.
// If the `proxy.OnRequest` handler is defined, it will be called with the `ProxyRequest` otherwise the modifier will return `ErrRequestHandlerUndefined`
func WriteRequestModifier(proxy *Proxy, req *http.Request) error {
	if reqID, ok := core.RequestIDFromContext(req.Context()); ok {
		proxyRequest, err := NewProxyRequest(req, reqID)
		if err != nil {
			return fmt.Errorf("%w : %w", ErrProxyRequest, err)
		}
		proxy.DBWriteChannel <- proxyRequest
		if launchpadID, ok := core.LaunchpadIDFromContext(req.Context()); ok {
			launchpadRequest := &domain.LaunchpadRequest{
				LaunchpadID: launchpadID,
				RequestID:   proxyRequest.ID,
			}
			proxy.DBWriteChannel <- launchpadRequest
		}
		if proxy.OnRequest == nil {
			return ErrRequestHandlerUndefined
		} else {
			proxy.OnRequest(*proxyRequest)
			return nil
		}
	}
	return ErrRequestIDNotFound
}

// ResponseFilterModifier will perform an initial filtering round on responses.
// It will skip processing for responses to CONNECT requests, responses where the skip flag was set, or SkipRoundTrip is true.
// It will also add the response time to the context
func ResponseFilterModifier(proxy *Proxy, res *http.Response) error {
	if res.Request.Method == http.MethodConnect || martian.NewContext(res.Request).SkippingRoundTrip() {
		return ErrSkipPipeline
	}
	if skip, ok := core.SkipFlagFromContext(res.Request.Context()); ok && skip {
		return ErrSkipPipeline
	}
	res.Request = core.ContextWithResponseTime(res.Request, time.Now())
	return nil
}

// BufferStreamingBodyModifier reads the entire streaming response body into memory
// and replaces the `res.Body` with a new `io.NopCloser` on the full body. It will
// remove the `Transfer-Encoding` and update the `Content-Length` to reflect the new body.
func BufferStreamingBodyModifier(proxy *Proxy, res *http.Response) error {
	defer res.Body.Close()

	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%w : %w", ErrReadBody, err)
	}

	res.Body = io.NopCloser(bytes.NewReader(responseBody))
	res.ContentLength = int64(len(responseBody))
	res.Header.Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	res.TransferEncoding = nil
	return nil
}

// CompressedResponseModifier decompresses the response bodies and replaces the `res.Body`
// with the decompressed data. It will remove the "Content-Encoding" header and update the "Content-Length" to the new length.
// Currently the modifier handles gzip and br compressed bodies.
func CompressedResponseModifier(proxy *Proxy, res *http.Response) error {
	if res.Header.Get("Content-Encoding") != "" && res.Body != nil && res.ContentLength > 0 {
		switch res.Header.Get("Content-Encoding") {
		case "gzip":
			defer res.Body.Close()

			gzipReader, err := gzip.NewReader(res.Body)
			if err != nil {
				return fmt.Errorf("creating gzip reader: %w", err)
			}

			defer gzipReader.Close()

			decompressedBody, err := io.ReadAll(gzipReader)
			if err != nil {
				return fmt.Errorf("reading gzip content: %w", err)
			}

			res.Body = io.NopCloser(bytes.NewReader(decompressedBody))
			res.ContentLength = int64(len(decompressedBody))
			res.Header.Set("Content-Length", fmt.Sprintf("%d", len(decompressedBody)))
			res.Header.Del("Content-Encoding")
		case "br":
			defer res.Body.Close()

			brotliReader := brotli.NewReader(res.Body)

			decompressedBody, err := io.ReadAll(brotliReader)
			if err != nil {
				return fmt.Errorf("reading brotli content : %w", err)
			}

			res.Body = io.NopCloser(bytes.NewReader(decompressedBody))
			res.ContentLength = int64(len(decompressedBody))
			res.Header.Set("Content-Length", fmt.Sprintf("%d", len(decompressedBody)))
			res.Header.Del("Content-Encoding")
		default:
			return nil
		}
	}
	return nil
}

// CompassResponseModifier will run the `processResponse` function in the compass extension to determine if the response is in scope.
// After `processResponse`, it will check if the response is passed through (nil), skipped (`ErrSkipPipeline`), or dropped (`ErrDropped`).
// If the compass extension is not found the modifier will return `ErrExtensionNotFound` as "compass" is considered a core extension.
func CompassResponseModifier(proxy *Proxy, res *http.Response) error {
	if compassExt, ok := proxy.GetExtension("compass"); ok {
		compassExt.Mu.Lock()
		defer compassExt.Mu.Unlock()
		if compassExt.CheckGlobalFunction("processResponse") {
			err := compassExt.CallResponseHandler(res)
			if err != nil {
				proxy.WriteLog("ERROR", fmt.Sprintf("Running processResponse : %s", err.Error()), core.LogWithExtensionID(compassExt.Data.ID))
				// Continue as a err in Lua should not bring down the proxy
			}
			if skip, ok := core.SkipFlagFromContext(res.Request.Context()); ok && skip {
				return ErrSkipPipeline
			}

			if dropped, ok := core.DroppedFlagFromContext(res.Request.Context()); ok && dropped {
				return ErrDropped
			}
		}
		return nil
	}
	return ErrExtensionNotFound
}

// ExtensionsResponseModifier will run the `processResponse` function (if it is defined) for all the loaded extensions (except compass and checkpoint).
// The modifier will check if the extension ID in request context matches the current extension and skip execution if it does.
// After `processResponse`, it will check if the request is passed through (nil), skipped (`ErrSkipPipeline`), or dropped (`ErrDropped`).
func ExtensionsResponseModifier(proxy *Proxy, res *http.Response) error {
	for _, ext := range proxy.Extensions {
		if ext.Data.Name != "checkpoint" && ext.Data.Name != "compass" {
			if extensionID, ok := core.ExtensionIDFromContext(res.Request.Context()); !ok || extensionID != ext.Data.ID.String() {
				ext.Mu.Lock()
				defer ext.Mu.Unlock()
				if ext.CheckGlobalFunction("processResponse") {
					err := ext.CallResponseHandler(res)
					if err != nil {
						proxy.WriteLog("ERROR", fmt.Sprintf("Running processResponse : %s", err.Error()), core.LogWithExtensionID(ext.Data.ID))
						// Continue as a err in Lua should not bring down the proxy
					}

					if skip, ok := core.SkipFlagFromContext(res.Request.Context()); ok && skip {
						return ErrSkipPipeline
					}

					if dropped, ok := core.DroppedFlagFromContext(res.Request.Context()); ok && dropped {
						return ErrDropped
					}

				}
			}
		}
	}
	return nil
}

// CheckpointResponseModifier will intercept response if the global `proxy.InterceptFlag` is set, `interceptResponse` function returns true, or
// if the context has an intercept flag set as true.
// If a response is intercepted, the modifier will block until the user decides to resume or drop the response. If the response is resumed it will be
// rebuilt with the same context and metadata from the modified raw response. The metadata will be updated to include "intercepted", "original-response", and "dropped" based
// on the user action.
func CheckpointResponseModifier(proxy *Proxy, res *http.Response) error {
	if checkpointExt, ok := proxy.GetExtension("checkpoint"); ok {
		shouldIntercept, err := checkpointExt.ShouldInterceptResponse(res)
		if err != nil {
			if reqID, ok := core.RequestIDFromContext(res.Request.Context()); ok {
				proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptResponse : %s", err.Error()), core.LogWithReqResID(reqID))
			} else {
				proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptResponse : %s", err.Error()))
			}
			return nil
		}

		if interceptFlag, ok := core.InterceptFlagFromContext(res.Request.Context()); (ok && interceptFlag) || shouldIntercept || proxy.InterceptFlag {
			original, err := httputil.DumpResponse(res, true)
			if err != nil {
				return fmt.Errorf("getting raw response for intercept : %w", err)
			}

			interceptedResponse := Intercepted{
				Type:    "response",
				Raw:     string(original),
				Channel: make(chan InterceptionTuple),
			}
			proxy.InterceptedQueue = append(proxy.InterceptedQueue, &interceptedResponse)

			if proxy.OnIntercept == nil {
				proxy.WriteLog("ERROR", "Response intercepted but OnIntercept is not defined. Dropping response")
				return ErrDropped
			}

			proxy.OnIntercept(&interceptedResponse)

			userAction := <-interceptedResponse.Channel

			if metadata, ok := core.MetadataFromContext(res.Request.Context()); ok {
				metadata["intercepted"] = true
				metadata["original-response"] = string(original)
				if !userAction.Resume {
					metadata["dropped"] = true
				}
				res.Request = core.ContextWithMetadata(res.Request, metadata)
			} else {
				return ErrMetadataNotFound
			}

			if !userAction.Resume {
				return ErrDropped
			}

			rebuiltRes, err := rawhttp.RebuildResponse([]byte(interceptedResponse.Raw), res.Request)
			if err != nil {
				return fmt.Errorf("%w : %w", ErrRebuildResponse, err)
			}

			*res = *rebuiltRes

			return nil
		}
		return nil
	}
	return ErrExtensionNotFound
}

// WriteResponseModifier is the final modifier in the default response pipeline.
// It will create a `ProxyResponse` struct and queue it for database insertion.
// If the `proxy.OnResponse` handler is defined, it will be called with the `ProxyResponse` otherwise the modifier will return `ErrResponseHandlerUndefined`
func WriteResponseModifier(proxy *Proxy, res *http.Response) error {
	proxyResponse, err := NewProxyResponse(res)
	if err != nil {
		return fmt.Errorf("%w : %w", ErrProxyResponse, err)
	}
	proxy.DBWriteChannel <- proxyResponse
	if proxy.OnResponse == nil {
		return ErrResponseHandlerUndefined
	} else {
		proxy.OnResponse(*proxyResponse)
		return nil
	}
}
