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
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/martian"
	"github.com/google/martian/fifo"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/listener"
	"github.com/tfkr-ae/marasi/rawhttp"
)

const (
	certFile = "marasi_cert.pem" // Certificate File Name
	keyFile  = "marasi_key.pem"  // Private Key File Name
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
	martianProxy     *martian.Proxy                       // The underlying martian.Proxy
	ConfigDir        string                               // The configuration directory (defaults to the marasi folder under the user configuration directory)
	Config           *Config                              // The marasi proxy configuration (separate from the GUI config)
	Repo             Repository                           // DB Repository Interface
	Modifiers        *fifo.Group                          // Modifier group pipeline
	DBWriteChannel   chan ProxyItem                       // DB Write Channel
	InterceptedQueue []*Intercepted                       // Queue of intercepted requests / responses
	OnRequest        func(req ProxyRequest) error         // Function to be ran on each request - used by the GUI application to handle the new requests
	OnResponse       func(res ProxyResponse) error        // Function to be ran on each response - used by the GUI application to handle the new responses
	OnIntercept      func(intercepted *Intercepted) error // Function to be ran on each intercept - used by the GUI application to handle the new intercepted items
	OnLog            func(log Log) error
	Addr             string       // IP Address of the proxy
	Port             string       // Port of the proxy
	Client           *http.Client // HTTP Client that is used by the repeater functionality (autoconfigured to use the proxy)
	Extensions       []*Extension // Slice of loaded extensions
	SPKIHash         string       // SPKI Hash of the current certificate
	Cert             *x509.Certificate
	TLSConfig        *tls.Config
	Scope            *Scope            // Proxy scope configuration through Compass
	Waypoints        map[string]string // Map of host:port overrides
	InterceptFlag    bool              // Global intercept flag
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
		Modifiers:      fifo.NewGroup(),
		DBWriteChannel: make(chan ProxyItem, 10),
		Extensions:     make([]*Extension, 0),
		Client:         &http.Client{},
		Scope:          NewScope(true),
		Waypoints:      make(map[string]string),
		InterceptFlag:  false,
	}
	err := proxy.WithOptions(options...)
	if err != nil {
		return nil, err
	}
	return proxy, nil
}

// AddRequestModifier accepts RequestModifierFunc and wraps it in a reqAdapter
func (proxy *Proxy) AddRequestModifier(modifier RequestModifierFunc) {
	adapter := &reqAdapter{proxy: proxy, modifier: modifier}
	proxy.Modifiers.AddRequestModifier(adapter)
}

// AddResponseModifier accepts ResponseModifierFunc and wraps it in a resAdapter
func (proxy *Proxy) AddResponseModifier(modifier ResponseModifierFunc) {
	adapter := &resAdapter{proxy: proxy, modifier: modifier}
	proxy.Modifiers.AddResponseModifier(adapter)
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

//	func (proxy *Proxy) CheckExtensionUpdates() map[string]bool {
//		updateMap := make(map[string]bool)
//		for _, extension := range proxy.Extensions {
//			if extension.Name != "checkpoint" && extension.Name != "workshop" && extension.Name != "compass" {
//				release, _, err := GetLatestRelease(extension.SourceURL)
//				if err != nil {
//					log.Print(err)
//					return updateMap
//				}
//				if release.PublishedAt.After(extension.UpdatedAt) {
//					log.Printf("%s has an update", extension.Name)
//					log.Printf("%v", release.PublishedAt)
//					log.Printf("%v", extension.UpdatedAt)
//					updateMap[extension.Name] = true
//				}
//			}
//		}
//		return updateMap
//	}
func (proxy *Proxy) GetExtension(name string) (*Extension, bool) {
	for _, ext := range proxy.Extensions {
		if ext.Name == name {
			return ext, true
		}
	}
	return nil, false
}

// func (proxy *Proxy) InstallExtension(url string, direct bool) error {
// 	if !direct {
// 		release, config, err := GetLatestRelease(url)
// 		if err != nil {
// 			return fmt.Errorf("getting latest release %s : %w", url, err)
// 		}
// 		luaAsset, err := getAsset(release.Assets, "extension.lua")
// 		if err != nil {
// 			return fmt.Errorf("getting lua asset: %w", err)
// 		}
// 		luaCode, err := Get(luaAsset.BrowserDownloadURL)
// 		if err != nil {
// 			return fmt.Errorf("getting extension.lua : %w", err)
// 		}
// 		err = proxy.Repo.CreateExtension(config.Name, config.SourceURL, config.Author, luaCode, release.PublishedAt, config.Description)
// 		if err != nil {
// 			return fmt.Errorf("creating extension : %w", err)
// 		}
// 		/*
// 			extension, err := proxy.Repo.GetExtension(config.Name)
// 			if err != nil {
// 			return fmt.Errorf("getting new extension %s : %w", config.Name, err)
// 			}
// 			delete(proxy.Extensions, config.Name)
// 			WithExtension(extension)(proxy)
// 		*/
// 	}
// 	return nil
// }

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
		return fmt.Errorf("unsupported type %T", v)
	}
}
func (m Metadata) Value() (driver.Value, error) {
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
	Type    string                 // "request" or "response"
	Raw     string                 // Raw HTTP data that can be modified
	Channel chan InterceptionTuple // Channel for receiving user decisions
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
		rawReq, prettified, err := rawhttp.DumpRequest(req)
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

	rawRes, prettified, err := rawhttp.DumpResponse(res)
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

// Accept waits for and returns the next connection to the listener.
func (proxy *Proxy) GetListener(address string, port string) (net.Listener, error) {
	rawListener, err := net.Listen("tcp", fmt.Sprintf("%s:%s", address, port))
	if err != nil {
		return rawListener, fmt.Errorf("setting up listener on address:port %s:%s", address, port)
	}
	muxListener := listener.NewProtocolMuxListener(rawListener, proxy.TLSConfig)
	marasiListener := listener.NewMarasiListener(muxListener)
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

func (proxy *Proxy) Launch(raw string, launchpadId string, useHttps bool) error {
	updated, err := rawhttp.RecalculateContentLength([]byte(raw))
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
