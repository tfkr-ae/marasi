// Package marasi provides an HTTP/HTTPS proxy server with extension support, request/response interception,
// and SQLite database storage. It is designed to be decoupled from GUI implementations and provides
// methods to load handlers for building security testing tools, traffic analysis, and HTTP manipulation applications.
//
// The core functionality includes:
//   - HTTP/HTTPS proxy server with TLS certificate management
//   - Lua-based extension system for request/response processing
//   - Request/response interception and modification
//   - SQLite database storage for traffic analysis
//   - Scope-based filtering system
//   - Chrome browser integration for testing
//   - Launchpad system for organizing test requests
package marasi

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gabriel-vasile/mimetype"
	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/yosssi/gohtml"
)

type contextKey string

const (
	certFile                      = "marasi_cert.pem" // Certificate File Name
	keyFile                       = "marasi_key.pem"  // Private Key File Name
	RequestIDKey       contextKey = "RequestID"       // Context key for the request ID so that it can be passed to the http.response
	LaunchpadIDKey     contextKey = "LaunchpadID"     // Context key for the repeater ID so that the repeater ID for a request can be used to link the request with a repeater entry
	MetadataKey        contextKey = "Metadata"        // Context key to store the metadata of a request
	ExtensionKey       contextKey = "ExtensionID"
	DropRequestKey     contextKey = "Drop"
	DropResponseKey    contextKey = "Drop"
	DoNotLogKey        contextKey = "DoNotLog"
	ShouldInterceptKey contextKey = "ShouldIntercept"
	RequestTimeKey     contextKey = "RequestTime"
	ResponseTimeKey    contextKey = "ResponseTime"
)

// Repository defines the methods consumed by the proxy to interact with the SQLite backend.
// It provides an abstraction layer for all database operations including request/response storage,
// extension management, logging, and configuration.
type Repository interface {
	InsertLog(log Log) error
	InsertRequest(req ProxyRequest) error
	InsertResponse(res ProxyResponse) error
	GetItems() (map[uuid.UUID]Row, error)
	GetRaw(id uuid.UUID) (Row, error)
	GetResponse(id uuid.UUID) (ProxyResponse, error)
	UpdateMetadata(metadata Metadata, ids ...uuid.UUID) error
	GetMetadata(id uuid.UUID) (metadata Metadata, err error)
	CountRows() (int32, error)
	GetLaunchpads() ([]Launchpad, error)
	GetLaunchpadRequests(id uuid.UUID) ([]ProxyRequest, error)
	CreateLaunchpad(name string, description string) (id uuid.UUID, err error)
	LinkRequestToLaunchpad(requestID uuid.UUID, laucnpadID uuid.UUID) error
	DeleteLaunchpad(launchpadID uuid.UUID) error
	UpdateLaunchpad(laucnpadID uuid.UUID, name, description string) error
	GetExtensionLuaCode(extensionName string) (code string, err error)
	UpdateLuaCode(extensionName string, code string) error
	GetNote(id uuid.UUID) (note string, err error)
	UpdateNote(id uuid.UUID, note string) (err error)
	CreateExtension(name string, sourceUrl string, author string, luaContent string, publishedDate time.Time, description string) error
	GetExtensions() ([]*Extension, error)
	GetExtension(name string) (extension *Extension, err error)
	RemoveExtension(name string) error
	GetLogs() ([]Log, error)
	CountNotes() (count int32, err error)
	CountLaunchpads() (count int32, err error)
	CountIntercepted() (count int32, err error)
	GetExtensionSettings(uuid.UUID) (Metadata, error)
	SetExtensionSettings(id uuid.UUID, settings Metadata) error
	UpdateSPKI(spki string) error
	GetFilters() (results []string, err error)
	SetFilters(filters []string) error
	GetWaypoints() (map[string]string, error)
	CreateOrUpdateWaypoint(hostname string, override string) error
	DeleteWaypoint(hostname string) error
	Close() error
}

// ProxyItem is an interface for items that can be written to the database through the DBWriteChannel.
// This interface is implemented by ProxyRequest, ProxyResponse, LaunchpadRequest, and Log types.
type ProxyItem interface {
	// GetType returns a string identifier for the type of proxy item.
	GetType() string
}

// Proxy is the main struct that orchestrates all proxy functionality including request/response processing,
// extension management, database operations, and TLS handling. It serves as the central coordinator
// for the Marasi proxy server.
type Proxy struct {
	martianProxy     *martian.Proxy // The underlying martian.Proxy
	ConfigDir        string         // The configuration directory (defaults to the marasi folder under the user configuration directory)
	Config           *Config
	Repo             Repository                           // DB Repository Interface
	DBWriteChannel   chan ProxyItem                       // DB Write Channel
	InterceptedQueue []*Intercepted                       // Queue of intercepted requests / responses
	OnRequest        func(req ProxyRequest) error         // Function to be ran on each request - used by the GUI application to handle the new requests
	OnResponse       func(res ProxyResponse) error        // Function to be ran on each response - used by the GUI application to handle the new responses
	OnIntercept      func(intercepted *Intercepted) error // Function to be ran on each intercept - used by the GUI application to handle the new intercepted items
	OnLog            func(log Log) error
	Addr             string                // IP Address of the proxy
	Port             string                // Port of the proxy
	Client           *http.Client          // HTTP Client that is used by the repeater functionality (autoconfigured to use the proxy)
	Extensions       map[string]*Extension // Map of the loaded extensions
	SPKIHash         string
	Cert             *x509.Certificate
	TLSConfig        *tls.Config
	Scope            *Scope
	Waypoints        map[string]string
	InterceptFlag    bool
}

// New creates a new Proxy instance with default configuration and applies any provided options.
// It initializes the underlying martian proxy, database write channel, extensions map, HTTP client,
// scope, waypoints, and sets up default log modifiers.
//
// Parameters:
//   - options: Variadic list of option functions to configure the proxy
//
// Returns:
//   - *Proxy: Configured proxy instance
//   - error: Configuration error if any option fails
func New(options ...func(*Proxy) error) (*Proxy, error) {
	proxy := &Proxy{
		martianProxy:   martian.NewProxy(),
		DBWriteChannel: make(chan ProxyItem, 10),
		Extensions:     make(map[string]*Extension),
		Client:         &http.Client{},
		Scope:          NewScope(true),
		Waypoints:      make(map[string]string),
		InterceptFlag:  false,
	}
	// TODO This can be loaded from the GUI implementation ?
	err := proxy.WithOptions(WithLogModifer())
	if err != nil {
		return nil, err
	}
	err = proxy.WithOptions(options...)
	if err != nil {
		return nil, err
	}
	return proxy, nil
}

// addRequestId takes a *http.Request and the ID of the request. It returns an *http.Request with the requestId set in the context
func addRequestId(req *http.Request, requestId uuid.UUID) *http.Request {
	ctx := context.WithValue(req.Context(), RequestIDKey, requestId)
	return req.WithContext(ctx)
}

// addLaunchpadId takes a *http.Request and the ID of the Repeater entry that it is linked to. It returns an *http.Request with the repeaterID set in the context
func addLaunchpadId(req *http.Request, launchpadId uuid.UUID) *http.Request {
	ctx := context.WithValue(req.Context(), LaunchpadIDKey, launchpadId)
	return req.WithContext(ctx)
}

// addMetadata takes a *http.Request and the Metadata. It returns an *http.Request with the metadata set in the context
func addMetadata(req *http.Request, metadata Metadata) *http.Request {
	ctx := context.WithValue(req.Context(), MetadataKey, metadata)
	return req.WithContext(ctx)
}

func addExtensionId(req *http.Request, extensionId string) *http.Request {
	ctx := context.WithValue(req.Context(), ExtensionKey, extensionId)
	return req.WithContext(ctx)
}

func addInterceptFlag(req *http.Request, shouldIntercept bool) *http.Request {
	ctx := context.WithValue(req.Context(), ShouldInterceptKey, shouldIntercept)
	return req.WithContext(ctx)
}

func addRequestTime(req *http.Request) *http.Request {
	ctx := context.WithValue(req.Context(), RequestTimeKey, time.Now())
	return req.WithContext(ctx)
}

func addResponseTime(req *http.Request) *http.Request {
	ctx := context.WithValue(req.Context(), ResponseTimeKey, time.Now())
	return req.WithContext(ctx)
}

type customRoundTripper struct {
	cert *x509.Certificate
	base http.RoundTripper
}

func (c *customRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// For requests to "http://marasi.cert/", return a custom response immediately
	if req.URL.String() == "http://marasi.cert/" {
		body := c.cert.Raw
		resp := &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Request:       req,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}
		resp.Header.Set("Content-Type", "application/x-x509-ca-cert")
		resp.Header.Set("Content-Disposition", "attachment; filename=\"marasi-cert.der\"")
		return resp, nil
	}

	// Otherwise, let the default/base RoundTripper handle the request normally
	return c.base.RoundTrip(req)
}

func GetHostPort(req *http.Request) string {
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

func (proxy *Proxy) SyncWaypoints() error {
	waypoints, err := proxy.Repo.GetWaypoints()
	if err != nil {
		proxy.WriteLog("INFO", err.Error())
	}
	proxy.Waypoints = waypoints
	return nil
}

// ModifyRequest processes incoming HTTP requests through the proxy pipeline.
// The processing order is: waypoint overrides → extensions → interception → database storage.
// It assigns unique IDs, applies metadata, runs extension processors, handles interception,
// and stores the processed request in the database.
//
// Parameters:
//   - req: The HTTP request to process
//
// Returns:
//   - error: Processing error if any step fails
func (proxy *Proxy) ModifyRequest(req *http.Request) error {
	// Not the cleanest implementation tbh, but does the job for now - prevents looping infintely when the client requests the proxy URL
	if req.Host == (proxy.Addr + ":" + proxy.Port) {
		martian.NewContext(req).SkipRoundTrip()
		return nil
	}
	if proxy.Addr == "127.0.0.1" {
		if req.Host == ("localhost" + ":" + proxy.Port) {
			martian.NewContext(req).SkipRoundTrip()
			return nil
		}
	}
	// Connect methods are skipped
	if req.Method != http.MethodConnect {
		// The ID for this request - response is incremented and a new metadata map is created
		uuid, err := uuid.NewV7()
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Generating uuid for request : %s", err.Error()))
			return fmt.Errorf("generating uuid for request : %w", err)
		}
		metadata := make(Metadata)

		if override, ok := proxy.Waypoints[GetHostPort(req)]; ok {
			log.Print("Overriding ", req.Host, " with ", override)
			metadata["original_host"] = req.Host
			metadata["override_host"] = override
			*req = *addMetadata(req, metadata)
		}

		// The RequestID is set in the context
		*req = *addRequestId(req, uuid)

		// Add Request Time
		*req = *addRequestTime(req)

		// The request is checked if it is initiated by the repeater functionality
		isRepeater, launchpadId := IsLaunchpad(req)
		if isRepeater {
			log.Print("Detected repeater request")
			log.Printf("Repeater ID: %s", launchpadId)
			metadata["launchpad"] = true
			metadata["launchpad_id"] = launchpadId
			*req = *addLaunchpadId(req, launchpadId)
			// Removing the header
			req.Header.Del("x-launchpad-id")
		}

		// Metadata is updated in the context as it may be consumed in the extensions
		*req = *addMetadata(req, metadata)

		// Extensions
		extensionId := req.Header.Get("x-extension-id") // This is the extension header
		*req = *addExtensionId(req, extensionId)
		for name, ext := range proxy.Extensions {
			if name != "checkpoint" {
				if extensionId != ext.ID.String() {
					ext.mu.Lock()
					if ext.CheckGlobalFunction("processRequest") {
						err := ext.CallRequestHandler(req)
						if err != nil {
							proxy.WriteLog("ERROR", fmt.Sprintf("Running processRequest : %s", err.Error()), LogWithExtensionID(ext.ID))
							log.Print(err)
						}
						if doNotLog, ok := req.Context().Value(DoNotLogKey).(bool); ok && doNotLog {
							ext.mu.Unlock()
							log.Println("Request marked as DoNotLog by extension:", name)
							return nil // Skip further processing and logging
						}
						if dropped, ok := req.Context().Value(DropRequestKey).(bool); ok && dropped {
							ext.mu.Unlock()
							martian.NewContext(req).SkipRoundTrip()
							return fmt.Errorf("request dropped by extension %s", name)
						}
					}
					ext.mu.Unlock()
				}
				req.Header.Del("x-extension-id")
			}
		}

		// This I can 100 % push past the intercept but the intercept logic has to be updated to handle req directly
		proxyRequest, err := NewProxyRequest(req, uuid)
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Creating new proxy request : %s", err.Error()), LogWithReqResID(uuid))
			return fmt.Errorf("creating new proxy request %d : %w", uuid, err)
		}

		// Intercept
		intercept, err := proxy.Extensions["checkpoint"].ShouldInterceptRequest(req)
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptRequest : %s", err.Error()), LogWithReqResID(uuid))
			log.Print(err)
		}
		if intercept || proxy.InterceptFlag {
			original := proxyRequest.Raw.ToString()
			intercepted := Intercepted{
				Type:     "request",
				Original: proxyRequest,
				Raw:      proxyRequest.Raw.ToString(),
				Channel:  make(chan InterceptionTuple),
			}
			proxy.InterceptedQueue = append(proxy.InterceptedQueue, &intercepted)
			proxy.OnIntercept(&intercepted)
			// Wait for the user to resume the request
			action := <-intercepted.Channel
			if !action.Resume {
				martian.NewContext(req).SkipRoundTrip()
				return fmt.Errorf("request dropped")
			}
			if action.ShouldInterceptResponse {
				// Need to set context
				*req = *addInterceptFlag(req, true)
			}
			// Now we need to rebuild the request
			rebuilt, err := RebuildRequestWithCtx([]byte(intercepted.Raw), req)
			if err != nil {
				return fmt.Errorf("rebuilding new request with old ctx : %w", err)
			}
			// Need to set the modifier request to the rebuilt one
			*req = *rebuilt

			// TODO Metadata is updated here, need to recreated the request with the original metadata
			proxyRequest.Metadata["intercepted"] = true
			proxyRequest.Metadata["original-request"] = original
			metadata := proxyRequest.Metadata
			proxyRequest, err = NewProxyRequest(req, uuid)
			proxyRequest.Metadata = metadata
			if err != nil {
				return fmt.Errorf("recreating proxy request %d : %w", uuid, err)
			}
		}
		proxy.DBWriteChannel <- proxyRequest
		if isRepeater {
			repeaterRequest := LaunchpadRequest{
				LaunchpadID: launchpadId,
				RequestID:   proxyRequest.ID,
			}
			proxy.DBWriteChannel <- repeaterRequest
		}
		*req = *addMetadata(req, proxyRequest.Metadata)
		proxy.OnRequest(*proxyRequest)
	}
	return nil
}

func handleChunkedEncoding(res *http.Response) error {
	if res.TransferEncoding != nil && res.TransferEncoding[0] == "chunked" {
		defer res.Body.Close()

		// Read the entire chunked response body
		body, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("reading chunked response body: %w", err)
		}

		// Replace the original body with the full response body
		res.Body = io.NopCloser(bytes.NewReader(body))
		res.ContentLength = int64(len(body))
		res.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		res.TransferEncoding = nil
	}
	return nil
}
func decodeGzipContent(res *http.Response) error {
	if res.Header.Get("Content-Encoding") == "gzip" {
		defer res.Body.Close()

		// Check if the body is empty
		if res.ContentLength == 0 || res.Body == nil {
			return nil // Nothing to decode
		}

		gzipReader, err := gzip.NewReader(res.Body)
		if err != nil {
			return fmt.Errorf("creating gzip reader: %w", err)
		}
		defer gzipReader.Close()

		decodedBody, err := io.ReadAll(gzipReader)
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				// Handle partial content if necessary
				return fmt.Errorf("partial gzip content: %w", err)
			}
			return fmt.Errorf("reading gzip content: %w", err)
		}

		// Replace the original body with the decoded body
		res.Body = io.NopCloser(bytes.NewReader(decodedBody))
		res.ContentLength = int64(len(decodedBody))
		res.Header.Set("Content-Length", fmt.Sprintf("%d", len(decodedBody)))
		res.Header.Del("Content-Encoding")
	}
	if res.Header.Get("Content-Encoding") == "br" {
		defer res.Body.Close()

		// Create a Brotli reader
		brReader := brotli.NewReader(res.Body)

		// Read and decode the Brotli content
		decodedBody, err := io.ReadAll(brReader)
		if err != nil {
			return fmt.Errorf("error decoding Brotli content: %w", err)
		}

		// Replace the original body with the decoded content
		res.Body = io.NopCloser(bytes.NewReader(decodedBody))
		res.ContentLength = int64(len(decodedBody))
		res.Header.Del("Content-Encoding")
		res.Header.Set("Content-Length", fmt.Sprintf("%d", len(decodedBody)))
	}
	return nil
}

// ModifyResponse processes HTTP responses through the proxy pipeline.
// It handles content decoding (gzip, brotli), runs extension processors, manages interception,
// and stores the processed response in the database.
//
// Parameters:
//   - res: The HTTP response to process
//
// Returns:
//   - error: Processing error if any step fails
func (proxy *Proxy) ModifyResponse(res *http.Response) error {
	if res.Request.Method != http.MethodConnect && !martian.NewContext(res.Request).SkippingRoundTrip() {
		if doNotLog, ok := res.Request.Context().Value(DoNotLogKey).(bool); ok && doNotLog {
			return nil // Skip further processing and logging
		}
		if err := decodeGzipContent(res); err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Decoding GZIP Content : %s", err.Error()))
			return fmt.Errorf("decoding gzip content: %w", err)
		}
		// Handle chunked transfer encoding
		if err := handleChunkedEncoding(res); err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Handling chunked encoding : %s", err.Error()))
			return fmt.Errorf("handling chunked encoding: %w", err)
		}

		// Should I do this?
		res.Request = addResponseTime(res.Request)
		// Extensions
		for name, ext := range proxy.Extensions {
			if name != "checkpoint" {
				extensionId, _ := res.Request.Context().Value(ExtensionKey).(string)
				if extensionId != ext.ID.String() {
					ext.mu.Lock()
					if ext.CheckGlobalFunction("processResponse") {
						err := ext.CallResponseHandler(res)
						if err != nil {
							proxy.WriteLog("ERROR", fmt.Sprintf("Running processResponse : %s", err.Error()), LogWithExtensionID(ext.ID))
							log.Print(err)
						}
						if doNotLog, ok := res.Request.Context().Value(DoNotLogKey).(bool); ok && doNotLog {
							ext.mu.Unlock()
							log.Println("Response marked as DoNotLog by extension:", name)
							return nil // Skip further processing and logging
						}
						if dropped, ok := res.Request.Context().Value(DropResponseKey).(bool); ok && dropped {
							ext.mu.Unlock()
							return fmt.Errorf("response dropped by extension %s", name)
						}
					}
					ext.mu.Unlock()
				}
			}
		}
		proxyResponse, err := NewProxyResponse(res)
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Creating new proxy response : %s", err.Error())) // TODO add the uuid
			return fmt.Errorf("creating new proxy response : %w", err)
		}
		// Intercept
		intercept, err := proxy.Extensions["checkpoint"].ShouldInterceptResponse(res)
		if err != nil {
			proxy.WriteLog("ERROR", fmt.Sprintf("Running shouldInterceptResponse : %s", err.Error()))
			log.Print(err)
		}
		if shouldIntercept, ok := res.Request.Context().Value(ShouldInterceptKey).(bool); (ok && shouldIntercept) || intercept || proxy.InterceptFlag {
			original := proxyResponse.Raw.ToString()
			intercepted := Intercepted{
				Type:     "response",
				Original: proxyResponse,
				Raw:      proxyResponse.Raw.ToString(),
				Channel:  make(chan InterceptionTuple),
			}
			proxy.InterceptedQueue = append(proxy.InterceptedQueue, &intercepted)
			proxy.OnIntercept(&intercepted)
			// Wait for the user to resume the request
			action := <-intercepted.Channel
			if !action.Resume {
				//martian.NewContext(res).SkipRoundTrip()
				return fmt.Errorf("response dropped")
			}
			// Now we need to rebuild the response
			rebuilt, err := RebuildResponse([]byte(intercepted.Raw), res.Request)
			if err != nil {
				return fmt.Errorf("rebuilding response : %w", err)
			}
			// Need to set the modifier request to the rebuilt one
			*res = *rebuilt
			//TODO METADATA
			metadata := proxyResponse.Metadata
			proxyResponse, err = NewProxyResponse(res)
			proxyResponse.Metadata = metadata
			proxyResponse.Metadata["intercepted"] = true
			proxyResponse.Metadata["original-response"] = original
			if err != nil {
				return fmt.Errorf("recreating proxy request : %w", err)
			}
		}
		proxy.DBWriteChannel <- proxyResponse
		proxy.OnResponse(*proxyResponse)
	}
	return nil
}

// Should be updatd
func (proxy *Proxy) CheckExtensionUpdates() map[string]bool {
	updateMap := make(map[string]bool)
	for _, extension := range proxy.Extensions {
		if extension.Name != "checkpoint" && extension.Name != "workshop" && extension.Name != "compass" {
			release, _, err := GetLatestRelease(extension.SourceURL)
			if err != nil {
				log.Print(err)
				return updateMap
			}
			if release.PublishedAt.After(extension.UpdatedAt) {
				log.Printf("%s has an update", extension.Name)
				log.Printf("%v", release.PublishedAt)
				log.Printf("%v", extension.UpdatedAt)
				updateMap[extension.Name] = true
			}
		}
	}
	return updateMap
}
func (proxy *Proxy) RemoveExtension(name string) error {
	err := proxy.Repo.RemoveExtension(name)
	if err != nil {
		return fmt.Errorf("removing extension : %w", err)
	}
	delete(proxy.Extensions, name)
	return nil
}
func (proxy *Proxy) InstallExtension(url string, direct bool) error {
	if !direct {
		release, config, err := GetLatestRelease(url)
		if err != nil {
			return fmt.Errorf("getting latest release %s : %w", url, err)
		}
		luaAsset, err := getAsset(release.Assets, "extension.lua")
		if err != nil {
			return fmt.Errorf("getting lua asset: %w", err)
		}
		luaCode, err := Get(luaAsset.BrowserDownloadURL)
		if err != nil {
			return fmt.Errorf("getting extension.lua : %w", err)
		}
		err = proxy.Repo.CreateExtension(config.Name, config.SourceURL, config.Author, luaCode, release.PublishedAt, config.Description)
		if err != nil {
			return fmt.Errorf("creating extension : %w", err)
		}
		/*
			extension, err := proxy.Repo.GetExtension(config.Name)
			if err != nil {
			return fmt.Errorf("getting new extension %s : %w", config.Name, err)
			}
			delete(proxy.Extensions, config.Name)
			WithExtension(extension)(proxy)
		*/
	}
	return nil
}

// RawField represents the raw HTTP request / response data stored as bytes in the DB
type RawField []byte

// Metadata represents a flexible key-value store for additional data associated with requests, responses, and extensions.
type Metadata map[string]any

// ToString returns the string representation of the raw field.
func (r RawField) ToString() string {
	return string(r)
}

func (m *Metadata) Scan(value interface{}) error {
	if value == nil {
		*m = make(Metadata)
		return nil
	}

	switch v := value.(type) {
	case []byte:
		json.Unmarshal(v, &m)
		return nil
	case string:
		json.Unmarshal([]byte(v), &m)
		return nil
	default:
		return fmt.Errorf(fmt.Sprintf("unsupported type %T", v))
	}
}
func (m Metadata) Value() (driver.Value, error) {
	// Your custom logic here, for example, converting email to uppercase before storing in DB
	if len(m) == 0 {
		return "{}", nil
	}
	return json.Marshal(m)
}
func (r *RawField) Scan(value interface{}) error {
	if value == nil {
		*r = nil
		return nil
	}

	if v, ok := value.([]byte); ok {
		*r = v
		return nil
	}

	return fmt.Errorf("unsupported type for RawField: %T", value)
}

func (r RawField) Value() (driver.Value, error) {
	return []byte(r), nil
}

func (r RawField) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(r))
}

// ProxyRequest represents an HTTP request processed by the proxy, containing all relevant
// request data and metadata for storage and analysis.
type ProxyRequest struct {
	ID          uuid.UUID `db:"request_id"`   // Unique identifier for the request
	Scheme      string    `db:"scheme"`       // URL scheme (http or https)
	Method      string    `db:"method"`       // HTTP method (GET, POST, etc.)
	Host        string    `db:"host"`         // Request host
	Path        string    `db:"path"`         // Request path including query parameters
	Raw         RawField  `db:"request_raw"`  // Complete raw HTTP request
	Metadata    Metadata  `db:"metadata"`     // Additional metadata and extension data
	RequestedAt time.Time `db:"requested_at"` // Timestamp when request was made
}

// ProxyResponse represents an HTTP response processed by the proxy, containing all relevant
// response data and metadata for storage and analysis.
type ProxyResponse struct {
	ID          uuid.UUID `db:"response_id"`  // Unique identifier matching the associated request
	Status      string    `db:"status"`       // HTTP status text (e.g., "200 OK")
	StatusCode  int       `db:"status_code"`  // HTTP status code (e.g., 200, 404)
	ContentType string    `db:"content_type"` // Response content type
	Length      string    `db:"length"`       // Content length
	Raw         RawField  `db:"response_raw"` // Complete raw HTTP response
	Metadata    Metadata  `db:"metadata"`     // Additional metadata and extension data
	RespondedAt time.Time `db:"responded_at"` // Timestamp when response was received
}

// Log represents a log entry in the system, capturing events, errors, and information
// from the proxy and extensions.
type Log struct {
	ID          uuid.UUID      `db:"id"`           // Unique identifier for the log entry
	Timestamp   time.Time      `db:"timestamp"`    // When the log entry was created
	Level       string         `db:"level"`        // Log level (DEBUG, INFO, WARN, ERROR, FATAL)
	Message     string         `db:"message"`      // Log message content
	Context     Metadata       `db:"context"`      // Additional context data
	RequestID   sql.NullString `db:"request_id"`   // Associated request ID if applicable
	ExtensionID sql.NullString `db:"extension_id"` // Associated extension ID if applicable
}

// Row represents a complete request-response pair with associated metadata,
// typically used when retrieving data from the database.
type Row struct {
	Request  ProxyRequest  // The HTTP request
	Response ProxyResponse // The corresponding HTTP response
	Metadata Metadata      // Combined metadata from request and response
}

// InterceptionTuple contains the user's decision when an intercepted item is resumed,
// indicating whether to continue and whether to intercept the corresponding response.
type InterceptionTuple struct {
	Resume                  bool // Whether to resume the intercepted item
	ShouldInterceptResponse bool // Whether to intercept the corresponding response
}

// Intercepted represents a request or response that has been intercepted for manual inspection
// and modification before being allowed to continue.
type Intercepted struct {
	Type     string                 // "request" or "response"
	Original ProxyItem              // The original request or response
	Raw      string                 // Raw HTTP data that can be modified
	Channel  chan InterceptionTuple // Channel for receiving user decisions
}

// Waypoint represents a hostname override mapping, allowing requests to specific hosts
// to be redirected to different destinations.
type Waypoint struct {
	Hostname string // The hostname to match
	Override string // The destination to redirect to
}

func (request ProxyRequest) GetType() string {
	return "request"
}

func (response ProxyResponse) GetType() string {
	return "response"
}

func (log Log) GetType() string {
	return "log"
}

func NewProxyRequest(req *http.Request, requestId uuid.UUID) (*ProxyRequest, error) {
	if metadata, ok := req.Context().Value(MetadataKey).(Metadata); ok {
		requestTime, ok := req.Context().Value(RequestTimeKey).(time.Time)
		if !ok {
			return nil, fmt.Errorf("timestamp not found for this context")
		}

		path := req.URL.Path
		if req.URL.RawQuery != "" {
			path = fmt.Sprintf("%s?%s", path, req.URL.RawQuery)
		}
		proxyRequest := &ProxyRequest{
			ID:          requestId,
			Scheme:      req.URL.Scheme,
			Method:      req.Method,
			Host:        req.Host,
			Path:        path,
			Metadata:    metadata,
			RequestedAt: requestTime,
		}
		// TODO Check prettified error
		rawReq, prettified, err := DumpRequest(req)
		if err != nil {
			return nil, fmt.Errorf("dumping request %d body : %w", requestId, err)
		}
		proxyRequest.Raw = RawField(rawReq)
		if prettified != "" {
			proxyRequest.Metadata["prettified-request"] = prettified
		}
		return proxyRequest, nil
	}
	return nil, fmt.Errorf("metadata not set")
}

// parseContentType tries to parse the content type header and returns an error if parsing fails
func parseContentType(header string) (string, error) {
	if header == "" {
		return "", fmt.Errorf("empty content type header")
	}

	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return "", fmt.Errorf("parsing content type '%s': %w", header, err)
	}

	return strings.ToLower(mediaType), nil
}

func NewProxyResponse(res *http.Response) (*ProxyResponse, error) {
	requestId, ok := res.Request.Context().Value(RequestIDKey).(uuid.UUID)
	if !ok {
		return nil, fmt.Errorf("request id not found in context")
	}

	responseTime, ok := res.Request.Context().Value(ResponseTimeKey).(time.Time)
	if !ok {
		return nil, fmt.Errorf("timestamp not found for this context")
	}

	rawRes, prettified, err := DumpResponse(res)
	if err != nil {
		return nil, fmt.Errorf("dumping response %s: %w", requestId, err)
	}

	// Handle redirects specifically
	var contentType string
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		contentType = "text/plain" // Redirects are just text
	} else {
		// Default for non-redirects
		contentType = "application/octet-stream"
		if ct := res.Header.Get("Content-Type"); ct != "" {
			if parsedType, err := parseContentType(ct); err == nil {
				contentType = parsedType
			} else {
				log.Printf("warning: %v, using default", err)
			}
		}
	}

	metadata, ok := res.Request.Context().Value(MetadataKey).(Metadata)
	if !ok {
		metadata = make(Metadata)
	}

	proxyResponse := &ProxyResponse{
		ID:          requestId,
		Status:      res.Status,
		StatusCode:  res.StatusCode,
		ContentType: contentType,
		Length:      res.Header.Get("Content-Length"),
		Raw:         RawField(rawRes),
		Metadata:    metadata,
		RespondedAt: responseTime,
	}

	if prettified != "" {
		proxyResponse.Metadata["prettified-response"] = prettified
	}
	return proxyResponse, nil
}

// Prettify attempts to format the body of either an *http.Request or *http.Response
// based on its detected content type. It returns the prettified string or an error.
func Prettify(bodyBytes []byte) (string, error) {
	if len(bodyBytes) == 0 {
		return "", nil
	}
	contentType := mimetype.Detect(bodyBytes).String()
	// Prettify based on the content type.
	switch {
	case strings.Contains(contentType, "application/json"):
		// Unmarshal to ensure valid JSON.
		var data interface{}
		if err := json.Unmarshal(bodyBytes, &data); err != nil {
			return "", fmt.Errorf("failed to unmarshal JSON: %w", err)
		}
		// MarshalIndent to create pretty-printed JSON.
		prettyBytes, err := json.MarshalIndent(data, "", "  ")
		log.Print("Len Original Bytes: ", len(bodyBytes))
		log.Print("Len Pretty Bytes: ", len(prettyBytes))
		if err != nil {
			return "", fmt.Errorf("failed to marshal JSON with indent: %w", err)
		}
		return string(prettyBytes), nil

	case strings.Contains(contentType, "application/xml"),
		strings.Contains(contentType, "text/xml"):
		// Use xml.Indent to prettify XML.
		var data interface{}
		if err := xml.Unmarshal(bodyBytes, &data); err != nil {
			return "", fmt.Errorf("failed to unmarshal XML: %w", err)
		}
		prettyBytes, err := xml.MarshalIndent(data, "", " ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal XML with indent: %w", err)
		}
		return string(prettyBytes), nil

	case strings.Contains(contentType, "text/html"):
		// Use the gohtml package to prettify HTML.
		pretty := gohtml.Format(string(bodyBytes))
		return pretty, nil

	default:
		// For other types (or if detection fails), return the body as a plain string.
		return "", nil
	}
}

func DumpResponse(res *http.Response) ([]byte, string, error) {
	responseDump, err := httputil.DumpResponse(res, false)
	if err != nil {
		return []byte{}, "", fmt.Errorf("dumping response : %w", err)
	}

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return []byte{}, "", fmt.Errorf("reading response body: %w", err)
	}
	res.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Combine the dumped headers and the read body for a complete dump equivalent.
	fullDump := append(responseDump, bodyBytes...)

	prettified, err := Prettify(bodyBytes)
	if prettified == "" {
		return fullDump, "", nil
	}

	prettifiedDump := string(responseDump) + prettified
	return fullDump, prettifiedDump, nil
}
func DumpRequest(req *http.Request) ([]byte, string, error) {
	requestDump, err := httputil.DumpRequest(req, false)
	if err != nil {
		return []byte{}, "", fmt.Errorf("dumping request : %w", err)
	}
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return []byte{}, "", fmt.Errorf("reading request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Combine the dumped headers and the read body for a complete dump equivalent.
	fullDump := append(requestDump, bodyBytes...)
	prettified, err := Prettify(bodyBytes)
	if prettified == "" {
		return fullDump, "", nil
	}
	prettifiedDump := string(requestDump) + prettified
	return fullDump, prettifiedDump, nil
}

func RebuildRequestWithCtx(raw []byte, originalRequest *http.Request) (req *http.Request, err error) {
	updated, err := RecalculateContentLength(raw)
	if err != nil {
		return nil, fmt.Errorf("recalculating content length : %w", err)
	}
	req, err = http.ReadRequest(bufio.NewReader(bytes.NewReader(updated)))
	if err != nil {
		return nil, fmt.Errorf("reading raw request %s : %w", raw, err)
	}
	req = req.WithContext(originalRequest.Context())
	req.URL.Host = req.Host
	req.URL.Scheme = originalRequest.URL.Scheme
	return req, nil
}

func RebuildResponse(raw []byte, req *http.Request) (res *http.Response, err error) {
	updated, err := RecalculateContentLength(raw)
	if err != nil {
		return nil, fmt.Errorf("recalculating content length : %w", err)
	}
	res, err = http.ReadResponse(bufio.NewReader(bytes.NewReader(updated)), req)
	if err != nil {
		return nil, fmt.Errorf("reading raw response %s : %w", raw, err)
	}
	return res, nil
}
func (proxy *Proxy) WriteToDB() {
	for proxyItem := range proxy.DBWriteChannel {
		switch castItem := proxyItem.(type) {
		case *ProxyRequest:
			// castItem.RequestedAt = time.Now()
			err := proxy.Repo.InsertRequest(*castItem)
			if err != nil {
				log.Println(err)
			}
		case *ProxyResponse:
			// castItem.RespondedAt = time.Now()
			err := proxy.Repo.InsertResponse(*castItem)
			if err != nil {
				log.Println(err)
			}
		case LaunchpadRequest:
			log.Print("Linking to Repeater")
			err := proxy.Repo.LinkRequestToLaunchpad(castItem.RequestID, castItem.LaunchpadID)
			if err != nil {
				log.Println(err)
			}
		case Log:
			log.Print("Log Detected")
			log.Print(castItem)
			err := proxy.Repo.InsertLog(castItem)
			if err != nil {
				log.Print(err)
			}
			proxy.OnLog(castItem)
		default:
			log.Print(castItem)
		}
	}
}
func (proxy *Proxy) WriteLog(level string, message string, options ...func(log *Log) error) error {
	switch level {
	case "DEBUG":
	case "INFO":
	case "WARN":
	case "ERROR":
	case "FATAL":
	default:
		return fmt.Errorf("level should be either: debug, info, warn, error, fatal")
	}
	uuid, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generating new uuid : %w", err)
	}
	log := Log{
		ID:        uuid,
		Level:     level,
		Message:   message,
		Timestamp: time.Now(),
	}
	for _, option := range options {
		err := option(&log)
		if err != nil {
			return fmt.Errorf("applying log option : %w", err)
		}
	}
	proxy.DBWriteChannel <- log
	return nil
}

type MarasiListener struct {
	net.Listener
	TLSConfig *tls.Config
	proxy     *Proxy // temp workaround
}

// connWrapper wraps a net.Conn and uses io.MultiReader to prepend the first byte
type connWrapper struct {
	net.Conn
	io.Reader
}

// Read calls the Reader's Read method
func (cw *connWrapper) Read(b []byte) (int, error) {
	return cw.Reader.Read(b)
}

func (l *MarasiListener) Accept() (net.Conn, error) {
	for {
		rawConn, err := l.Listener.Accept()
		if err != nil {
			if rawConn != nil {
				_ = rawConn.Close()
			}
			l.proxy.WriteLog("ERROR", fmt.Sprintf("Connection error: %v", err))
			return nil, fmt.Errorf("accepting connection: %w", err)
		}

		// Wrap in a buffered reader
		br := bufio.NewReader(rawConn)

		// Optional: Set a short deadline for peeking (to avoid hanging forever)
		deadline := time.Now().Add(10 * time.Second)
		_ = rawConn.SetReadDeadline(deadline)

		// Try to peek at the first 5 bytes
		peekSize := 5
		peeked, err := br.Peek(peekSize)
		// Clear the deadline after peek
		_ = rawConn.SetReadDeadline(time.Time{})

		if err != nil && err != bufio.ErrBufferFull {
			// If the error is anything other than needing a bigger buffer, bail out
			_ = rawConn.Close()
			l.proxy.WriteLog("ERROR", fmt.Sprintf("Failed to peek initial bytes: %v", err))
			continue
			//return nil, fmt.Errorf("reading initial bytes: %w", err)
		}

		// Possibly fewer than 5 bytes are available, so check length
		n := len(peeked)
		log.Printf("Initial peek (hex): % x", peeked)

		// Basic TLS detection:
		//   Byte 0: 0x16 (Handshake),
		//   Byte 1: 0x03 (TLS version)
		isTLS := (n >= 2 && peeked[0] == 0x16 && peeked[1] == 0x03)

		if isTLS {
			log.Printf("it's TLS")
			//	l.proxy.WriteLog("INFO", "TLS connection detected")

			// Wrap the raw connection in a TLS server,
			// but remember we still have 'br' as our buffered reader.
			// We can pass a wrapper that first reads from 'br'
			// to feed any peeked bytes to the TLS handshake.
			tlsConn := tls.Server(&connWrapper{
				Conn:   rawConn,
				Reader: br, // The handshake will read from br, which still has the peeked data.
			}, l.TLSConfig)

			rawConn.SetDeadline(time.Now().Add(10 * time.Second))
			err = tlsConn.Handshake()
			rawConn.SetDeadline(time.Time{}) // clear deadline after handshake

			if err != nil {
				_ = tlsConn.Close()
				l.proxy.WriteLog("ERROR", fmt.Sprintf("TLS handshake failed: %v", err))
				continue
				//	return nil, fmt.Errorf("TLS handshake failed: %w", err)
			}

			// Return the TLS-wrapped connection
			return tlsConn, nil
		}

		// Otherwise, treat it as a plain (non-TLS) connection
		//l.proxy.WriteLog("INFO", "Non-TLS connection detected")
		return &connWrapper{
			Conn:   rawConn,
			Reader: br,
		}, nil
	}
}

// Accept waits for and returns the next connection to the listener.
func (proxy *Proxy) GetListener(address string, port string) (net.Listener, error) {
	//listener, err := tls.Listen("tcp", fmt.Sprintf("%s:%s", address, port), proxy.TLSConfig)
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%s", address, port))
	marasiListener := &MarasiListener{
		Listener:  listener,
		TLSConfig: proxy.TLSConfig,
		proxy:     proxy,
	}
	if err != nil {
		return listener, fmt.Errorf("setting up listener on address:port %s:%s", address, port)
	}
	proxy.Addr = address
	proxy.Port = port
	proxy.WriteLog("INFO", fmt.Sprintf("Marasi Service Started on %s:%s", address, port))

	// Setup client
	parsedURL, err := url.Parse(fmt.Sprintf("http://%s:%s", proxy.Addr, proxy.Port))
	if err != nil {
		log.Fatal(fmt.Errorf("error parsing proxy URL: %w", err))
	}
	transport := &http.Transport{
		Proxy:           http.ProxyURL(parsedURL),
		TLSClientConfig: proxy.TLSConfig,
	}
	proxy.Client.Transport = transport
	return marasiListener, nil
}
func (proxy *Proxy) Serve(listener net.Listener) error {
	//defer proxy.martianProxy.Close()
	go proxy.WriteToDB()
	upstreamTLS := &tls.Config{
		MinVersion: tls.VersionTLS10,
		NextProtos: []string{"http/1.1"},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256, // Included for broader compatibility, but less preferred
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384, // Included for broader compatibility, but less preferred
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,    // Included for older clients, avoid if possible
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,    // Included for older clients, avoid if possible
		},
	}
	transport := &http.Transport{}
	transport.TLSClientConfig = upstreamTLS
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if metadata, ok := ctx.Value(MetadataKey).(Metadata); ok {
			log.Print("Metadata ", metadata)
			if override, ok := metadata["override_host"].(string); ok {
				log.Print("Overriding ", address, " with ", override)
				address = override
			}
		}
		return net.Dial(network, address)
	}
	proxy.martianProxy.SetRoundTripper(
		&customRoundTripper{
			cert: proxy.Cert,
			base: transport,
		},
	)
	return proxy.martianProxy.Serve(listener)
}

func (proxy *Proxy) Close() {
	proxy.martianProxy.Close()
}

// Takes a raw request / response and returns the fixed string with updated content length
func RecalculateContentLength(raw []byte) (updated []byte, err error) {
	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	parts := bytes.SplitN(normalized, []byte("\n\n"), 2)
	if len(parts) == 2 {
		headers := parts[0]
		body := parts[1]

		headerLines := bytes.Split(headers, []byte("\n"))
		newHeaders := make([][]byte, 0, len(headerLines)+1)
		for _, line := range headerLines {
			if !bytes.HasPrefix(bytes.ToLower(line), []byte("content-length:")) {
				newHeaders = append(newHeaders, line)
			}
		}
		newContentLength := fmt.Sprintf("Content-Length: %d", len(body))
		newHeaders = append(newHeaders, []byte(newContentLength))

		updatedHeaders := bytes.Join(newHeaders, []byte("\r\n"))
		// Reconstruct request with correct Content-Length
		updated := append(updatedHeaders, []byte("\r\n\r\n")...)
		updated = append(updated, body...)
		return updated, nil
	}
	return []byte{}, fmt.Errorf("malformed string : %s", normalized)
}

func (proxy *Proxy) Launch(raw string, launchpadId string, useHttps bool) error {
	updated, err := RecalculateContentLength([]byte(raw))
	if err != nil {
		return fmt.Errorf("recalculating content length : %w", err)
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(updated)))
	if err != nil {
		return fmt.Errorf("reading http request : %w", err)
	}

	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	// Construct Full URL
	if useHttps {
		scheme = "https"
	}
	host := req.Host
	if host == "" {
		return fmt.Errorf("host header not found or is empty")
	}

	req.RequestURI, req.URL.Scheme, req.URL.Host = "", scheme, host
	req.Header.Add("x-launchpad-id", launchpadId)
	_, err = proxy.Client.Do(req)
	if err != nil {
		return fmt.Errorf("client doing request : %w", err)
	}
	return nil
}
