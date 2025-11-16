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
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	"github.com/tfkr-ae/marasi/listener"
	"github.com/tfkr-ae/marasi/rawhttp"
)

var (
	// ErrConfigDirNotSet is returned when the configuration directory is not set.
	ErrConfigDirNotSet = errors.New("config dir not set")
	// ErrScopeNotFound is returned when the scope is not found in the proxy.
	ErrScopeNotFound = errors.New("scope field is not found")
	// ErrClientNotFound is returned when the HTTP client is not found in the proxy.
	ErrClientNotFound = errors.New("http client field not found")
	// ErrExtensionRepoNotFound is returned when the extension repository is not found.
	ErrExtensionRepoNotFound = errors.New("extension repo not found")
)

const (
	certFile = "marasi_cert.pem" // Certificate File Name
	keyFile  = "marasi_key.pem"  // Private Key File Name
)

// Proxy is the main struct that orchestrates all proxy functionality including request/response processing,
// extension management, database operations, and TLS handling. It serves as the central coordinator
// for the Marasi proxy server.
type Proxy struct {
	martianProxy          *martian.Proxy                       // The underlying martian.Proxy
	ConfigDir             string                               // The configuration directory (defaults to the marasi folder under the user configuration directory)
	Config                *Config                              // The marasi proxy configuration (separate from the GUI config)
	Modifiers             *fifo.Group                          // Modifier group pipeline
	DBWriteChannel        chan any                             // DB Write Channel
	InterceptedQueue      []*Intercepted                       // Queue of intercepted requests / responses
	OnRequest             func(req domain.ProxyRequest) error  // Function to be ran on each request - used by the GUI application to handle the new requests
	OnResponse            func(res domain.ProxyResponse) error // Function to be ran on each response - used by the GUI application to handle the new responses
	OnIntercept           func(intercepted *Intercepted) error // Function to be ran on each intercept - used by the GUI application to handle the new intercepted items
	OnLog                 func(log domain.Log) error           // Function to be ran on each log event - used by the GUI application to handle new log entries
	Addr                  string                               // IP Address of the proxy
	Port                  string                               // Port of the proxy
	Client                *http.Client                         // HTTP Client that is used by the repeater functionality (autoconfigured to use the proxy)
	Extensions            []*extensions.LuaExtension           // Slice of loaded extensions
	SPKIHash              string                               // SPKI Hash of the current certificate
	Cert                  *x509.Certificate                    // The proxy's TLS certificate.
	mitmConfig            *tls.Config                          // Martian Proxy MITM config
	MarasiClientTLSConfig *tls.Config                          // TLSConfig for the proxy.Client
	Scope                 *compass.Scope                       // Proxy scope configuration through Compass
	Waypoints             map[string]string                    // Map of host:port overrides
	InterceptFlag         bool                                 // Global intercept flag

	TrafficRepo   domain.TrafficRepository   // Repository for traffic data.
	LaunchpadRepo domain.LaunchpadRepository // Repository for launchpad data.
	WaypointRepo  domain.WaypointRepository  // Repository for waypoint data.
	StatsRepo     domain.StatsRepository     // Repository for statistics data.
	ConfigRepo    domain.ConfigRepository    // Repository for configuration data.
	LogRepo       domain.LogRepository       // Repository for log data.
	ExtensionRepo domain.ExtensionRepository // Repository for extension data.
	DBCloser      io.Closer                  // Closer for the database connection.
}

// GetConfigDir returns the configuration directory path.
// It returns an error if the configuration directory is not set.
func (proxy *Proxy) GetConfigDir() (string, error) {
	if proxy.ConfigDir == "" {
		return "", ErrConfigDirNotSet
	}
	return proxy.ConfigDir, nil
}

// GetScope returns the current scope configuration.
// It returns an error if the scope is not set.
func (proxy *Proxy) GetScope() (*compass.Scope, error) {
	if proxy.Scope == nil {
		return nil, ErrScopeNotFound
	}
	return proxy.Scope, nil
}

// GetClient returns the proxy's HTTP client.
// It returns an error if the client is not set.
func (proxy *Proxy) GetClient() (*http.Client, error) {
	if proxy.Client == nil {
		return nil, ErrClientNotFound
	}
	return proxy.Client, nil
}

// GetExtensionRepo returns the extension repository.
// It returns an error if the repository is not set.
func (proxy *Proxy) GetExtensionRepo() (domain.ExtensionRepository, error) {
	if proxy.ExtensionRepo == nil {
		return nil, ErrExtensionRepoNotFound
	}
	return proxy.ExtensionRepo, nil
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
		DBWriteChannel: make(chan any, 10),
		Extensions:     make([]*extensions.LuaExtension, 0),
		Client:         &http.Client{},
		Scope:          compass.NewScope(true),
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

// SyncWaypoints fetches the latest waypoints from the repository and updates the proxy's in-memory map.
func (proxy *Proxy) SyncWaypoints() error {
	if proxy.WaypointRepo == nil {
		return fmt.Errorf("WaypointRepository not set")
	}
	waypointSlice, err := proxy.WaypointRepo.GetWaypoints()
	if err != nil {
		log.Printf("syncing waypoints: %v", err)
		return err
	}

	waypointsMap := make(map[string]string)
	for _, waypoint := range waypointSlice {
		waypointsMap[waypoint.Hostname] = waypoint.Override
	}

	proxy.Waypoints = waypointsMap
	return nil

}

// GetExtension retrieves a loaded extension by its name.
// It returns the extension and true if found, otherwise nil and false.
func (proxy *Proxy) GetExtension(name string) (*extensions.LuaExtension, bool) {
	for _, ext := range proxy.Extensions {
		if ext.Data.Name == name {
			return ext, true
		}
	}
	return nil, false
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

// NewProxyRequest creates a new domain.ProxyRequest from an http.Request.
// It extracts metadata from the request context and dumps the raw request.
func NewProxyRequest(req *http.Request, requestId uuid.UUID) (*domain.ProxyRequest, error) {
	if metadata, ok := core.MetadataFromContext(req.Context()); ok {
		requestTime, ok := core.RequestTimeFromContext(req.Context())
		if !ok {
			return nil, fmt.Errorf("timestamp not found for this context")
		}

		path := req.URL.Path
		if req.URL.RawQuery != "" {
			path = fmt.Sprintf("%s?%s", path, req.URL.RawQuery)
		}
		proxyRequest := &domain.ProxyRequest{
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
		proxyRequest.Raw = domain.RawField(rawReq)
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

// NewProxyResponse creates a new domain.ProxyResponse from an http.Response.
// It extracts metadata from the response context and dumps the raw response.
func NewProxyResponse(res *http.Response) (*domain.ProxyResponse, error) {
	requestId, ok := core.RequestIDFromContext(res.Request.Context())
	if !ok {
		return nil, fmt.Errorf("request id not found in context")
	}

	responseTime, ok := core.ResponseTimeFromContext(res.Request.Context())
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

	metadata, ok := core.MetadataFromContext(res.Request.Context())
	if !ok {
		metadata = make(map[string]any)
	}

	proxyResponse := &domain.ProxyResponse{
		ID:          requestId,
		Status:      res.Status,
		StatusCode:  res.StatusCode,
		ContentType: contentType,
		Length:      res.Header.Get("Content-Length"),
		Raw:         domain.RawField(rawRes),
		Metadata:    metadata,
		RespondedAt: responseTime,
	}

	if prettified != "" {
		proxyResponse.Metadata["prettified-response"] = prettified
	}
	return proxyResponse, nil
}

// WriteToDB reads from the DBWriteChannel and writes items to their respective repositories.
// It handles ProxyRequest, ProxyResponse, LaunchpadRequest, and Log items.
func (proxy *Proxy) WriteToDB() {
	for proxyItem := range proxy.DBWriteChannel {
		switch castItem := proxyItem.(type) {
		case *domain.ProxyRequest:
			err := proxy.TrafficRepo.InsertRequest(castItem)
			if err != nil {
				log.Println(err)
			}
		case *domain.ProxyResponse:
			err := proxy.TrafficRepo.InsertResponse(castItem)
			if err != nil {
				log.Println(err)
			}
		case *domain.LaunchpadRequest:
			err := proxy.LaunchpadRepo.LinkRequestToLaunchpad(castItem.RequestID, castItem.LaunchpadID)
			if err != nil {
				log.Println(err)
			}
		case *domain.Log:
			log.Print(castItem)
			err := proxy.LogRepo.InsertLog(castItem)
			if err != nil {
				log.Print(err)
			}
			proxy.OnLog(*castItem)
		default:
			log.Print(castItem)
		}
	}
}

// WriteLog creates a new log entry and sends it to the DBWriteChannel.
// It accepts a level, a message, and optional functions to modify the log entry.
func (proxy *Proxy) WriteLog(level string, message string, options ...func(log *domain.Log) error) error {
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
	log := domain.Log{
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
	proxy.DBWriteChannel <- &log
	return nil
}

// Accept waits for and returns the next connection to the listener.
func (proxy *Proxy) GetListener(address string, port string) (net.Listener, error) {
	rawListener, err := net.Listen("tcp", fmt.Sprintf("%s:%s", address, port))
	if err != nil {
		return rawListener, fmt.Errorf("setting up listener on address:port %s:%s", address, port)
	}
	muxListener := listener.NewProtocolMuxListener(rawListener, proxy.mitmConfig)
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
		TLSClientConfig: proxy.MarasiClientTLSConfig,
	}
	proxy.Client.Transport = transport
	return marasiListener, nil
}

// Serve starts the proxy and begins accepting connections on the provided listener.
// It also starts the database writer goroutine.
func (proxy *Proxy) Serve(listener net.Listener) error {
	go proxy.WriteToDB()
	roundTripper := newMarasiTransport(proxy.Cert)
	proxy.martianProxy.SetRoundTripper(roundTripper)
	return proxy.martianProxy.Serve(listener)
}

// Close shuts down the proxy and closes the database connection.
func (proxy *Proxy) Close() {
	proxy.martianProxy.Close()
	if proxy.DBCloser != nil {
		log.Println("Closing database connection...")
		proxy.DBCloser.Close()
	}

}

// Launch sends a raw HTTP request through the proxy client.
// It is used for the launchpad functionality to replay and test requests.
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
