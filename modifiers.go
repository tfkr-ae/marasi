package marasi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

var (
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

	// ErrRequestHandlerUndefined is no longer returned: a proxy with no request handler still captures.
	ErrRequestHandlerUndefined = errors.New("no request handler defined")

	// ErrResponseHandlerUndefined is no longer returned: a proxy with no response handler still captures.
	ErrResponseHandlerUndefined = errors.New("no response handler defined")

	// ErrProxyRequest is returned when ProxyRequest cannot be created
	ErrProxyRequest = errors.New("failed to create proxyrequest")

	// ErrProxyResponse is returned when ErrProxyResponse cannot be created
	ErrProxyResponse = errors.New("failed to create proxyresponse")

	// ErrReadBody is returned when there is an error with reading the response body
	ErrReadBody = errors.New("failed to read the body")

	// ErrWebSocketUpstreamUnavailable is returned when an upgrade response does not expose a bidirectional upstream stream.
	ErrWebSocketUpstreamUnavailable = errors.New("websocket upstream stream unavailable")
)

// armoryRunID returns the Armory run ID carried by an internal request header.
func armoryRunID(req *http.Request) (uuid.UUID, bool) {
	runID, err := uuid.Parse(req.Header.Get(armoryRunIDHeader))
	if err != nil {
		return uuid.Nil, false
	}
	return runID, true
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

// hookInterrupted reports whether a failed hook was interrupted rather than raising a Lua error.
// A project switch, service stop, or listener shutdown cancels the execution context, and a client disconnect cancels ctx.
// Interrupted hooks fail closed so the item is never sent. Other Lua errors fail open.
func hookInterrupted(ctx context.Context, extension *extensions.Runtime) bool {
	if extension.ExecutionContext != nil && extension.ExecutionContext.Err() != nil {
		return true
	}
	return ctx.Err() != nil
}

// hookLogOptions links a hook error to its extension. The request ID goes in the log context,
// not the request link, because the request is stored later or, when dropped, never.
func hookLogOptions(requestID uuid.UUID, extension *extensions.Runtime) []func(*domain.Log) error {
	options := []func(*domain.Log) error{core.LogWithExtensionID(extension.Data.ID)}
	if requestID != uuid.Nil {
		options = append(options, core.LogWithContext(map[string]any{"request_id": requestID.String()}))
	}
	return options
}

// hookRequest returns the `req` a Lua hook receives for pair.
func hookRequest(pair *Pair) *extensions.Request {
	return &extensions.Request{Request: pair.Request, ID: pair.ID, Metadata: pair.Metadata}
}

// PreventLoopModifier answers a request made to marasi's active listener
// address and port with a 502 proxy error response, so it never loops back
// through the proxy. It normalises localhost and 127.0.0.1 when it checks the
// host and port. Like a refused request, the pair is set up, stored whatever
// the scope and its cause logged
// (docs/adr/2026-11-17-go-modifier-errors-fail-closed-and-proxy-error-responses-never-go-upstream.md).
func PreventLoopModifier(proxy *Proxy, pair *Pair) error {
	if !proxy.targetsListener(pair.Request) {
		return nil
	}
	if err := SetupRequestModifier(proxy, pair); err != nil {
		return err
	}
	if pair.Dropped() {
		// A second claim of the same launch.
		return nil
	}
	proxy.respondWithProxyError(pair, http.StatusBadGateway, errLoop)
	if err := WriteRequestModifier(proxy, pair); err != nil {
		log.Printf("capturing a request that loops back: %v", err)
	}
	proxy.logProxyError(pair, errLoop)
	return nil
}

// SetupRequestModifier sets up the pair: its UUID, request time, metadata and route record.
// It also captures internal Launchpad and Armory correlation IDs. A claimed launch's id
// becomes the pair's UUID, and a second claim of the same launch is dropped.
func SetupRequestModifier(proxy *Proxy, pair *Pair) error {
	req := pair.Request
	requested := time.Now()
	metadata := make(map[string]any)
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generating uuid for request: %w", err)
	}

	// An internal request naming a pending, unclaimed launch becomes that
	// launch's pair. Any other launch id is ignored and the request is ordinary traffic.
	if launchID := pair.requestedLaunch; launchID != uuid.Nil {
		launch, claim := proxy.claimLaunch(launchID)
		switch claim {
		case launchClaimed:
			id = launch.ID
			setLaunchMetadata(metadata, launch)
			pair.LaunchpadID = launch.LaunchpadID
		case launchAlreadyClaimed:
			proxy.WriteLog("WARN", fmt.Sprintf("Dropped a second claim of launch %s", launchID))
			pair.Drop()
			return nil
		}
	}

	if runID, exists := armoryRunID(req); exists {
		metadata["armory_run_id"] = runID
		pair.ArmoryRunID = runID
	}
	req.Header.Del(armoryRunIDHeader)

	if metadataString := req.Header.Get("x-marasi-metadata"); metadataString != "" {
		var headerMetadata map[string]any

		if err := json.Unmarshal([]byte(metadataString), &headerMetadata); err == nil {
			maps.Copy(metadata, headerMetadata)
		}
		req.Header.Del("x-marasi-metadata")
	}

	if marasiws.IsUpgradeRequest(req) {
		metadata["protocol"] = "websocket"
		metadata["websocket.transport"] = marasiws.TransportFromRequest(req)
		metadata["websocket.state"] = "pending"
	}

	pair.ID = id
	pair.Metadata = metadata
	if pair.RequestBodyEnding == BodyTooLarge {
		markPassedThrough(pair, "request")
	}
	pair.Timing.Requested = requested
	// The transport records the route the request goes out on, and the
	// response writes it as the pair's route snapshot.
	pair.Route = &core.RouteRecord{}
	return nil
}

// OverrideWaypointsModifier checks if a Waypoint (host override) is defined for this host:port.
// If a waypoint exists it will write the "original_host" and "override_host" to the pair's metadata
// and rewrite the URL host to the destination, which the transport then dials as is.
// If the pair has no metadata the modifier will return `ErrMetadataNotFound`
// TODO should allow TLS -> Non TLS override
func OverrideWaypointsModifier(proxy *Proxy, pair *Pair) error {
	if pair.Metadata == nil {
		return ErrMetadataNotFound
	}
	req := pair.Request
	if override, ok := proxy.waypointOverride(getHostPort(req)); ok {
		pair.Metadata["original_host"] = getHostPort(req)
		pair.Metadata["override_host"] = override
		req.URL.Host = override
		req.Host = override
	}
	return nil
}

// CompassRequestModifier will run the `processRequest` function in the compass extension to determine if the request is in scope.
// After `processRequest`, it will check if the request is passed through (nil), stopped (Stop), or dropped (Drop).
// If the compass extension is not found the modifier will return `ErrExtensionNotFound` as "compass" is considered a core extension.
// Compass's skip decision does not apply to a launch (see compassSkips).
func CompassRequestModifier(proxy *Proxy, pair *Pair) error {
	if compassExt, ok := proxy.GetExtension("compass"); ok {
		if !compassExt.MetadataSnapshot().Enabled {
			return nil
		}
		hookReq := hookRequest(pair)
		err := compassExt.CallRequestHandler(hookReq)
		pair.Skipped = pair.Skipped || hookReq.Skip
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Running processRequest : %s", err.Error()), core.LogWithExtensionID(compassExt.Data.ID))
			if hookInterrupted(pair.Context(), compassExt) {
				pair.Drop()
				return nil
			}
			// Continue as a err in Lua should not bring down the proxy
		}
		if proxy.compassSkips(pair) {
			pair.Stop()
			return nil
		}

		if hookReq.Drop {
			pair.Drop()
			return nil
		}
		return nil
	}
	return ErrExtensionNotFound
}

// ExtensionsRequestModifier will run the `processRequest` function (if it is defined) for all the loaded extensions (except compass and checkpoint).
// Initially the modifier will check if the request originated from an extension by reading the "x-extension-id" header. That
// extension is recorded on the pair, so that its hooks run on neither half of its own pair.
// After processRequest, it will check if the request is passed through (nil), stopped (Stop), or dropped (Drop).
func ExtensionsRequestModifier(proxy *Proxy, pair *Pair) error {
	req := pair.Request
	if pair.RequestBodyEnding == BodyTooLarge {
		defer proxy.keepPassedBody(pair, &req.Body, "request")()
	}
	extensionID := req.Header.Get("x-extension-id")
	pair.ExtensionID, _ = uuid.Parse(extensionID)

	// header is removed after processing
	req.Header.Del("x-extension-id")

	for _, ext := range proxy.Extensions {
		if ext.Data.Name != "checkpoint" && ext.Data.Name != "compass" && ext.MetadataSnapshot().Enabled {
			if extensionID != ext.Data.ID.String() {
				hookReq := hookRequest(pair)
				err := ext.CallRequestHandler(hookReq)
				pair.Skipped = pair.Skipped || hookReq.Skip
				if err != nil {
					proxy.WriteLog("ERROR", fmt.Sprintf("Running processRequest : %s", err.Error()), core.LogWithExtensionID(ext.Data.ID))
					if hookInterrupted(pair.Context(), ext) {
						pair.Drop()
						return nil
					}
					// Continue as a err in Lua should not bring down the proxy
				}

				if pair.Skipped {
					pair.Stop()
					return nil
				}

				if hookReq.Drop {
					pair.Drop()
					return nil
				}

			}
		}
	}
	return nil
}

// CheckpointRequestModifier holds requests when the global HTTP Checkpoint flag is set or interceptRequest returns true.
// A nil notify still holds until forward, drop, or drop-all. Forward rebuilds from optional raw. Drop does not rebuild.
// A Lua error in interceptRequest does not skip the global flag.
func CheckpointRequestModifier(proxy *Proxy, pair *Pair) error {
	if checkpointExt, ok := proxy.GetExtension("checkpoint"); ok {
		if !checkpointExt.MetadataSnapshot().Enabled {
			return nil
		}
		req := pair.Request
		shouldIntercept, err := checkpointExt.ShouldInterceptRequest(hookRequest(pair))
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptRequest : %s", err.Error()), hookLogOptions(pair.ID, checkpointExt)...)
			if hookInterrupted(pair.Context(), checkpointExt) {
				pair.Drop()
				return nil
			}
			// A Lua error does not match the rule. The global flag still holds.
		}

		if (shouldIntercept || proxy.GetIntercept()) && pair.RequestBodyEnding == BodyTooLarge {
			proxy.logPassedThrough(pair, "request", "A Checkpoint hold was")
			return nil
		}
		if shouldIntercept || proxy.GetIntercept() {
			// A request read from the wire shows as it goes upstream and
			// is edited through the codec (h1engine_checkpoint.go).
			var original []byte
			var edit *checkpointEdit
			if pair.requestWire != nil {
				original, _, err = dumpWireRequest(pair.requestWire, req)
				edit = wireRequestEdit(req, pair.requestWire)
			} else {
				original, err = httputil.DumpRequest(req, true)
			}
			if err != nil {
				return fmt.Errorf("getting raw request for intercept: %w", err)
			}
			if pair.ID == uuid.Nil {
				return ErrRequestIDNotFound
			}

			result, err := proxy.holdHTTPRequest(pair.ID, original, req, edit)
			if err != nil {
				return err
			}

			metadata := pair.Metadata
			if metadata == nil {
				return ErrMetadataNotFound
			}
			metadata["intercepted"] = true
			metadata["original-request"] = string(original)
			if result.dropped {
				metadata["dropped"] = true
			}

			if result.dropped {
				pair.Drop()
				return nil
			}

			if edit != nil {
				edit.apply(pair)
			} else {
				pair.Request = result.rebuiltReq
			}
			if result.interceptResponse {
				pair.InterceptResponse = true
			}

			return nil
		}
		return nil
	}
	return ErrExtensionNotFound
}

// WriteRequestModifier is the final modifier in the default request pipeline.
// It will create a `ProxyRequest` struct and queue it for database insertion.
// If the request came from launchpad, it will create a `LaunchpadRequest` struct and queue it for database insertion as well.
// If the `proxy.OnRequest` handler is defined, it is called with the `ProxyRequest`. A proxy with no handler still captures the request.
func WriteRequestModifier(proxy *Proxy, pair *Pair) error {
	if pair.ID == uuid.Nil {
		return ErrRequestIDNotFound
	}
	proxyRequest, err := NewProxyRequest(pair)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProxyRequest, err)
	}
	// The number is taken here, not by the writer, so the request
	// event already carries it (docs/adr/2026-10-16-pairs-are-numbered-in-capture-order.md).
	if proxy.TrafficRepo != nil {
		proxyRequest.Number = proxy.TrafficRepo.NextPairNumber()
	}
	proxy.DBWriteChannel <- proxyRequest
	pair.requestCaptured = true
	if proxy.OnRequest != nil {
		proxy.OnRequest(*proxyRequest)
	}
	return nil
}

// ResponseFilterModifier will perform an initial filtering round on responses.
// It skips responses where the skip flag was set. It records when the response
// was read on the pair. A CONNECT never reaches the chain (see admitTunnel), and
// neither does a response to a request a request modifier answered or dropped (see WithBasePipeline).
func ResponseFilterModifier(proxy *Proxy, pair *Pair) error {
	if pair.Skipped {
		pair.Stop()
		return nil
	}
	pair.Timing.Responded = time.Now()
	return nil
}

// WebSocketPrepareModifier detaches the upstream stream from a successful upgrade
// response and marks the request metadata state as open.
func WebSocketPrepareModifier(proxy *Proxy, pair *Pair) error {
	res := pair.Response
	if !marasiws.IsUpgradeResponse(res) {
		return nil
	}

	upstream, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		return fmt.Errorf(
			"%w: response body has type %T",
			ErrWebSocketUpstreamUnavailable,
			res.Body,
		)
	}
	negotiation, err := negotiateWebSocketExtensions(pair)
	if err != nil {
		failWebSocketHandshake(proxy, pair, err)
		return nil
	}
	if pair.Metadata == nil {
		return ErrMetadataNotFound
	}
	metadata := maps.Clone(pair.Metadata)
	metadata["websocket.state"] = "open"
	pair.Metadata = metadata
	pair.webSocketUpstream = upstream
	pair.webSocketNegotiation = &negotiation

	res.Body = http.NoBody
	res.ContentLength = 0
	res.Header.Del("Content-Length")
	res.TransferEncoding = nil

	return nil
}

// shouldInterceptWebSocketMessage reports whether a message should enter manual interception.
// Disabled checkpoint skips interception, including the global flag.
// An interrupted interceptWebSocketMessage drops the message.
func shouldInterceptWebSocketMessage(proxy *Proxy, message *marasiws.Message) bool {
	checkpoint, ok := proxy.GetExtension("checkpoint")
	if ok && (checkpoint.Data == nil || !checkpoint.MetadataSnapshot().Enabled) {
		return false
	}

	if proxy.GetWebSocketIntercept() {
		return true
	}

	if !ok {
		return false
	}

	shouldIntercept, err := checkpoint.ShouldInterceptWebSocketMessage(message)
	if err != nil {
		proxy.WriteLog(
			"ERROR",
			fmt.Sprintf("Running interceptWebSocketMessage : %s", err.Error()),
			core.LogWithExtensionID(checkpoint.Data.ID),
		)
		if hookInterrupted(context.Background(), checkpoint) {
			message.Dropped = true
		}
		return false
	}

	return shouldIntercept
}

// processWebSocketMessageExtensions runs enabled non-checkpoint extensions against a live message.
// Dropped and skipped messages stop further extension processing.
// An interrupted hook drops the message.
func processWebSocketMessageExtensions(proxy *Proxy, message *marasiws.Message) {
	for _, extension := range proxy.Extensions {
		if extension == nil || extension.Data == nil || !extension.MetadataSnapshot().Enabled || extension.Data.Name == "checkpoint" {
			continue
		}

		if err := extension.CallWebSocketMessageHandler(message); err != nil {
			proxy.WriteLog(
				"ERROR",
				fmt.Sprintf("Running processWebSocketMessage : %s", err.Error()),
				core.LogWithExtensionID(extension.Data.ID),
			)
			if hookInterrupted(context.Background(), extension) {
				message.Dropped = true
			}
		}
		if message.Dropped || message.Skipped {
			return
		}
	}
}

// WebSocketUpgradeHandler is the upgrade handler for WebSocket: it relays the
// session between the client and the origin and records it. The proxy has
// already written the 101 to the client.
func WebSocketUpgradeHandler(proxy *Proxy, pair *Pair, client, upstream io.ReadWriteCloser) error {
	defer client.Close()
	defer upstream.Close()
	req := pair.Request

	requestID := pair.ID
	if requestID == uuid.Nil {
		return ErrRequestIDNotFound
	}

	connectionID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("creating websocket connection ID: %w", err)
	}

	deflate, removedExtensions := webSocketNegotiated(pair)

	// Both peers are read through a watch, above TLS and buffering, so a watch reads
	// frames and can tell a peer that closes after its close frame from one that leaves.
	watchedClient := marasiws.NewWatchedStream(client)
	watchedUpstream := marasiws.NewWatchedStream(upstream)
	var connection *marasiws.Connection
	process := func(message *marasiws.Message) error {
		generated, _ := message.Metadata["generated"].(bool)
		// A passthrough message is only recorded: the relay cannot forward
		// it as processed, so no hook may hold or change it.
		if !generated && !message.Passthrough {
			processWebSocketMessageExtensions(proxy, message)

			if !message.Dropped && shouldInterceptWebSocketMessage(proxy, message) {
				if proxy.WebSocketInterceptor == nil {
					return errors.New("websocket interceptor not configured")
				}
				err := proxy.WebSocketInterceptor.Intercept(message, func(snapshot domain.WebSocketMessage) {
					proxy.noteWebSocketHold(message)
					// Nothing reads the sender while its message is held, so watch it: a
					// client or origin that leaves abandons the hold (docs/adr/2026-10-13-checkpoint-hold-ends-when-its-client-leaves.md). The other
					// side is still read, so its close ends the connection. A sender that
					// closes after its close frame has not left (see WatchedStream).
					//
					// A held close frame is not watched: its sender closes the socket next
					// as part of closing (RFC 6455 7.1.1, 7.1.7), and abandoning would drop
					// the close frame the other side needs. The other side's close still
					// ends that hold.
					abandon := func() { proxy.abandonWebSocketCheckpoint(connectionID) }
					switch {
					case snapshot.Opcode == marasiws.OpClose:
					case message.Direction == marasiws.DirectionFromClient:
						watchedClient.Watch(abandon)
					case message.Direction == marasiws.DirectionFromServer:
						watchedUpstream.Watch(abandon)
					}
					if proxy.OnWebSocketIntercept == nil {
						return
					}
					if err := proxy.OnWebSocketIntercept(snapshot); err != nil {
						_ = proxy.ResolveWebSocketInterception(snapshot.ID, marasiws.InterceptionDecision{
							Resume:  true,
							Opcode:  snapshot.Opcode,
							Payload: snapshot.Payload,
						})
					}
				})
				if err != nil {
					return fmt.Errorf("intercepting websocket message: %w", err)
				}
			}
		}
		if message.Dropped {
			if message.Metadata == nil {
				message.Metadata = make(map[string]any)
			}
			message.Metadata["dropped"] = true
		}

		snapshot := message.ToDomain()
		proxy.DBWriteChannel <- &snapshot
		if proxy.OnWebSocketMessage != nil {
			_ = proxy.OnWebSocketMessage(snapshot)
		}
		return nil
	}

	connection = marasiws.NewConnection(
		connectionID,
		requestID,
		watchedClient,
		watchedClient,
		watchedUpstream,
		process,
	)
	connection.SetDeflate(deflate)
	if proxy.WebSocketInterceptor != nil {
		connection.SetShutdownHandler(proxy.abandonWebSocketCheckpoint)
	}
	if proxy.Config != nil {
		connection.SetMaxMessageSize(proxy.Config.WebSocketMaxMessageSize)
	}
	if proxy.OnWebSocketProgress != nil {
		connection.SetProgressHandler(func(progress domain.WebSocketMessageProgress) {
			_ = proxy.OnWebSocketProgress(progress)
		})
	}
	connection.SetMessageTooBigHandler(func(tooBig marasiws.MessageTooBig) {
		message := fmt.Sprintf("a %s message of %d bytes is over", tooBig.Direction, tooBig.Size)
		if tooBig.Inflated {
			message = fmt.Sprintf("a compressed %s message inflates to over", tooBig.Direction)
		}
		_ = proxy.WriteLog("WARN", fmt.Sprintf(
			"WebSocket %s closed: %s the %d-byte limit. Sent 1009 to the sender and 1001 to the receiver.",
			connectionID, message, tooBig.Limit,
		), core.LogWithReqResID(requestID))
	})
	connection.SetInflateFailureHandler(func(failure marasiws.InflateFailure) {
		_ = proxy.WriteLog("WARN", fmt.Sprintf(
			"WebSocket %s closed: a compressed %s message cannot be inflated (%v). Sent 1007 to the sender and 1001 to the receiver.",
			connectionID, failure.Direction, failure.Err,
		), core.LogWithReqResID(requestID))
	})

	path := req.URL.Path
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}

	record := domain.WebSocketConnection{
		ID:        connectionID,
		RequestID: requestID,
		State:     "open",
		Transport: marasiws.TransportFromRequest(req),
		Host:      req.Host,
		Path:      path,
		StartedAt: time.Now(),

		Deflate:           deflate,
		RemovedExtensions: removedExtensions,
	}

	return proxy.runWebSocketSessionWithRelease(connection, record, pair.release)
}

// CompressedResponseModifier decodes a response whose Content-Encoding is
// exactly gzip or br, on one field line, so the rest of the chain, Checkpoint
// and storage see the body decoded with Content-Encoding removed and
// Content-Length set to the decoded length (when the response is not
// chunked). Every other coding is left as received. The pair's metadata
// records the coding in decoded_content_encoding and the length of the
// encoded body in wire_content_length.
//
// On the h1 engine the client still receives the origin's encoded bytes
// unless the chain changed the decoded body
// (docs/adr/2026-11-17-compressed-responses-are-transparent-on-the-wire-and-decoded-in-storage.md).
// A body that does not decode is not something that broke: it is left as
// received, so the client and storage get the origin's coded bytes, and
// metadata.content_decoding_error holds why it did not decode. A body that
// decodes to more than the body threshold is left coded the same way, and
// named in a proxy log.
func CompressedResponseModifier(proxy *Proxy, pair *Pair) error {
	res := pair.Response
	codings := res.Header.Values("Content-Encoding")
	// A passed body is only what arrived before it passed: it goes to
	// the client and storage as the origin coded it.
	if pair.ResponseBodyEnding == BodyTooLarge {
		return nil
	}
	if len(codings) != 1 || (codings[0] != "gzip" && codings[0] != "br") {
		return nil
	}
	if res.Body == nil || res.Body == http.NoBody {
		return nil
	}
	coded, err := io.ReadAll(res.Body)
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(coded))
	if err != nil {
		return fmt.Errorf("reading the coded body: %w", err)
	}
	if len(coded) == 0 {
		return nil
	}
	if pair.Metadata == nil {
		pair.Metadata = make(map[string]any)
	}
	limit := proxy.bodyThreshold()
	decoded, err := decodeContent(codings[0], coded, limit)
	if err != nil {
		pair.Metadata[contentDecodingErrorMetadataKey] = err.Error()
		if errors.Is(err, ErrDecodedContentTooLarge) {
			proxy.logDecodedContentTooLarge(pair, codings[0], limit)
		}
		return nil
	}
	pair.decoded = &decodedResponse{
		coded:   coded,
		decoded: decoded,
		framing: http.Header{"Content-Encoding": codings, "Content-Length": res.Header.Values("Content-Length")},
	}
	res.Body = io.NopCloser(bytes.NewReader(decoded))
	res.ContentLength = int64(len(decoded))
	if res.Header.Get("Transfer-Encoding") == "" {
		res.Header.Set("Content-Length", strconv.Itoa(len(decoded)))
	}
	res.Header.Del("Content-Encoding")
	pair.Metadata["decoded_content_encoding"] = codings[0]
	pair.Metadata["wire_content_length"] = len(coded)
	return nil
}

// contentDecodingErrorMetadataKey is the metadata field that holds why a gzip
// or br body did not decode, so a stored pair whose body is still coded says so.
const contentDecodingErrorMetadataKey = "content_decoding_error"

// decodedResponse is a response body CompressedResponseModifier decoded.
type decodedResponse struct {
	coded   []byte
	decoded []byte
	// framing holds the Content-Encoding and Content-Length values as the
	// origin sent them.
	framing http.Header
}

// ErrDecodedContentTooLarge means a gzip or br body decodes to more than
// the body threshold. Such a body is left coded, as one that does not decode
// is, so a small compressed body cannot expand into gigabytes of memory.
var ErrDecodedContentTooLarge = errors.New("decoded content is larger than the body threshold")

// decodeContent undoes the content coding gzip or br, reading at most limit
// decoded bytes: a body that decodes to more fails with
// ErrDecodedContentTooLarge.
func decodeContent(coding string, coded []byte, limit int64) ([]byte, error) {
	if coding == "br" {
		decoded, err := readDecodedContent(brotli.NewReader(bytes.NewReader(coded)), limit)
		if err != nil {
			return nil, fmt.Errorf("reading brotli content: %w", err)
		}
		return decoded, nil
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(coded))
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gzipReader.Close()
	decoded, err := readDecodedContent(gzipReader, limit)
	if err != nil {
		return nil, fmt.Errorf("reading gzip content: %w", err)
	}
	return decoded, nil
}

// readDecodedContent reads all of r up to limit bytes. One byte past limit
// is read so that a body of exactly limit bytes still decodes.
func readDecodedContent(r io.Reader, limit int64) ([]byte, error) {
	decoded, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(decoded)) > limit {
		return nil, ErrDecodedContentTooLarge
	}
	return decoded, nil
}

// logDecodedContentTooLarge names in a proxy log a response left coded
// because it decodes to more than limit bytes.
func (proxy *Proxy) logDecodedContentTooLarge(pair *Pair, coding string, limit int64) {
	target := "a response"
	if req := pair.Request; req != nil && req.URL != nil {
		target = fmt.Sprintf("the response to %s %s", req.Method, req.URL.Redacted())
	}
	message := fmt.Sprintf("Left %s %s-coded: it decodes to more than the %d-byte body threshold", target, coding, limit)
	if pair.ID != uuid.Nil {
		_ = proxy.WriteLog("WARN", message, core.LogWithReqResID(pair.ID))
		return
	}
	_ = proxy.WriteLog("WARN", message)
}

// responseHookMarks are the marks a Lua response hook set.
type responseHookMarks struct {
	dropped bool
	skipped bool
}

// callResponseHook runs hook on the pair's response as the Lua API sees it:
// its request carries the pair's ID and metadata in its context, where the
// extensions package reads them. The pair keeps the hook's metadata edits, and
// the response keeps its request. It returns the drop and skip marks set.
func callResponseHook(pair *Pair, hook func(*http.Response) error) (responseHookMarks, error) {
	res := pair.Response
	request := res.Request
	// A copy, so the API's writes never reach the pair's request.
	view := pair.Request.WithContext(pair.Request.Context())
	if pair.ID != uuid.Nil {
		view = core.ContextWithRequestID(view, pair.ID)
	}
	if pair.Metadata != nil {
		view = core.ContextWithMetadata(view, pair.Metadata)
	}
	res.Request = view
	err := hook(res)
	ctx := res.Request.Context()
	res.Request = request

	if metadata, ok := core.MetadataFromContext(ctx); ok {
		pair.Metadata = metadata
	}
	var marks responseHookMarks
	marks.dropped, _ = core.DroppedFlagFromContext(ctx)
	marks.skipped, _ = core.SkipFlagFromContext(ctx)
	return marks, err
}

// CompassResponseModifier will run the `processResponse` function in the compass extension to determine if the response is in scope.
// After `processResponse`, it will check if the response is passed through (nil), stopped (Stop), or dropped (Drop).
// If the compass extension is not found the modifier will return `ErrExtensionNotFound` as "compass" is considered a core extension.
// Compass's skip decision does not apply to a launch (see compassSkips).
func CompassResponseModifier(proxy *Proxy, pair *Pair) error {
	// Marasi made the response, so there is nothing for a hook to judge or edit.
	if pair.FromMarasi() {
		return nil
	}
	if compassExt, ok := proxy.GetExtension("compass"); ok {
		if !compassExt.MetadataSnapshot().Enabled {
			return nil
		}
		marks, err := callResponseHook(pair, compassExt.CallResponseHandler)
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Running processResponse : %s", err.Error()), core.LogWithExtensionID(compassExt.Data.ID))
			if hookInterrupted(pair.Context(), compassExt) {
				pair.Drop()
				return nil
			}
			// Continue as a err in Lua should not bring down the proxy
		}
		pair.Skipped = pair.Skipped || marks.skipped
		if proxy.compassSkips(pair) {
			pair.Stop()
			return nil
		}
		if marks.dropped {
			pair.Drop()
			return nil
		}
		return nil
	}
	return ErrExtensionNotFound
}

// ExtensionsResponseModifier will run the `processResponse` function (if it is defined) for all the loaded extensions (except compass and checkpoint).
// The modifier will check if the extension ID in request context matches the current extension and skip execution if it does.
// After `processResponse`, it will check if the request is passed through (nil), stopped (Stop), or dropped (Drop).
func ExtensionsResponseModifier(proxy *Proxy, pair *Pair) error {
	// Marasi made the response, so there is nothing for a hook to judge or edit.
	if pair.FromMarasi() {
		return nil
	}
	if pair.ResponseBodyEnding == BodyTooLarge {
		defer proxy.keepPassedBody(pair, &pair.Response.Body, "response")()
	}
	for _, ext := range proxy.Extensions {
		if ext.Data.Name != "checkpoint" && ext.Data.Name != "compass" && ext.MetadataSnapshot().Enabled {
			if pair.ExtensionID != ext.Data.ID {
				marks, err := callResponseHook(pair, ext.CallResponseHandler)
				if err != nil {
					proxy.WriteLog("ERROR", fmt.Sprintf("Running processResponse : %s", err.Error()), core.LogWithExtensionID(ext.Data.ID))
					if hookInterrupted(pair.Context(), ext) {
						pair.Drop()
						return nil
					}
					// Continue as a err in Lua should not bring down the proxy
				}

				if marks.skipped {
					pair.Stop()
					return nil
				}

				if marks.dropped {
					pair.Drop()
					return nil
				}

			}
		}
	}
	return nil
}

// CheckpointResponseModifier holds responses when the global HTTP Checkpoint flag is set, interceptResponse returns true,
// or the matching request was forwarded with intercept-response. A nil notify still holds until forward, drop, or drop-all.
// A Lua error in interceptResponse does not skip either flag.
func CheckpointResponseModifier(proxy *Proxy, pair *Pair) error {
	// Marasi made the response, so there is nothing for a hook to judge or edit.
	if pair.FromMarasi() {
		return nil
	}
	if checkpointExt, ok := proxy.GetExtension("checkpoint"); ok {
		if !checkpointExt.MetadataSnapshot().Enabled {
			return nil
		}
		var shouldIntercept bool
		_, err := callResponseHook(pair, func(res *http.Response) error {
			var err error
			shouldIntercept, err = checkpointExt.ShouldInterceptResponse(res)
			return err
		})
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptResponse : %s", err.Error()), hookLogOptions(pair.ID, checkpointExt)...)
			if hookInterrupted(pair.Context(), checkpointExt) {
				pair.Drop()
				return nil
			}
			// A Lua error does not match the rule. The global and per-request flags still hold.
		}

		if (pair.InterceptResponse || shouldIntercept || proxy.GetIntercept()) && pair.ResponseBodyEnding == BodyTooLarge {
			proxy.logPassedThrough(pair, "response", "A Checkpoint hold was")
			return nil
		}
		if pair.InterceptResponse || shouldIntercept || proxy.GetIntercept() {
			// A response read from the wire shows as it goes to the
			// client and is edited through the codec (h1engine_checkpoint.go).
			var original []byte
			var edit *checkpointEdit
			if pair.responseWire != nil {
				original, _, _, err = dumpWireResponse(pair.responseWire, pair.Response)
				edit = wireResponseEdit(pair.Response, pair.responseWire)
			} else {
				original, err = httputil.DumpResponse(pair.Response, true)
			}
			if err != nil {
				return fmt.Errorf("getting raw response for intercept: %w", err)
			}
			if pair.ID == uuid.Nil {
				return ErrRequestIDNotFound
			}

			result, err := proxy.holdHTTPResponse(pair.ID, original, pair.Response, edit)
			if err != nil {
				return err
			}

			if pair.Metadata == nil {
				return ErrMetadataNotFound
			}
			pair.Metadata["intercepted"] = true
			pair.Metadata["original-response"] = string(original)
			if result.dropped {
				pair.Metadata["dropped"] = true
			}

			if result.dropped {
				pair.Drop()
				return nil
			}

			if edit != nil {
				edit.apply(pair)
			} else {
				*pair.Response = *result.rebuiltRes
			}
			return nil
		}
		return nil
	}
	return ErrExtensionNotFound
}

// WriteResponseModifier is the final modifier in the default response pipeline.
// It will create a `ProxyResponse` struct and queue it for database insertion.
// If the `proxy.OnResponse` handler is defined, it is called with the `ProxyResponse`. A proxy with no handler still captures the response.
func WriteResponseModifier(proxy *Proxy, pair *Pair) error {
	proxyResponse, err := NewProxyResponse(pair)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProxyResponse, err)
	}
	pair.responseCaptured = true
	if pair.deferResponse(proxyResponse) {
		// A streamed pair's response is stored once its directions end,
		// with their endings and trailers (streamedPair.finish).
		return nil
	}
	proxy.DBWriteChannel <- proxyResponse
	if proxy.OnResponse != nil {
		proxy.OnResponse(*proxyResponse)
	}
	return nil
}
