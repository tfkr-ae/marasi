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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/martian"
	"github.com/google/martian/fifo"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/chrome"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	"github.com/tfkr-ae/marasi/listener"
	"github.com/tfkr-ae/marasi/rawhttp"
	marasiws "github.com/tfkr-ae/marasi/websocket"
	"github.com/tfkr-ae/marasi/wordlist"
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
	// ErrLaunchpadRepoNotFound is returned when the launchpad repository is not found.
	ErrLaunchpadRepoNotFound = errors.New("launchpad repo not found")
	// ErrWaypointRepoNotFound is returned when the waypoint repository is not found.
	ErrWaypointRepoNotFound = errors.New("waypoint repo not found")
	// ErrReportingRepoNotFound is returned when the reporting repository is not found.
	ErrReportingRepoNotFound = errors.New("reporting repo not found")
	// ErrArmoryRepoNotFound is returned when the Armory repository is not found.
	ErrArmoryRepoNotFound = errors.New("armory repo not found")
	// ErrArmoryNotFound is returned when the Armory service is not found.
	ErrArmoryNotFound = errors.New("armory service not found")
	// ErrWordlistManagerNotSet is returned when the wordlist manager is not set.
	ErrWordlistManagerNotSet = errors.New("wordlist manager not set")
	// ErrWebSocketConnectionNotFound is returned when a live WebSocket cannot be located.
	ErrWebSocketConnectionNotFound = errors.New("websocket connection not found or closed")
	// ErrWebSocketRepositoryNotSet is returned when WebSocket persistence is unavailable.
	ErrWebSocketRepositoryNotSet = errors.New("websocket repository not set")
	// ErrWebSocketDirection is returned when an inject direction is not client or server.
	ErrWebSocketDirection = errors.New("websocket direction must be client or server")
)

const (
	certFile = "marasi_cert.pem" // Certificate File Name
	keyFile  = "marasi_key.pem"  // Private Key File Name
)

// Proxy is the main struct that orchestrates all proxy functionality including request/response processing,
// extension management, database operations, and TLS handling. It serves as the central coordinator
// for the Marasi proxy server.
type Proxy struct {
	martianProxy   *martian.Proxy                       // The underlying martian.Proxy
	ConfigDir      string                               // The configuration directory (defaults to the marasi folder under the user configuration directory)
	Config         *Config                              // The marasi proxy configuration (separate from the GUI config)
	Modifiers      *fifo.Group                          // Modifier group pipeline
	DBWriteChannel chan any                             // DB Write Channel
	OnRequest      func(req domain.ProxyRequest) error  // Function to be ran on each request - used by the GUI application to handle the new requests
	OnResponse     func(res domain.ProxyResponse) error // Function to be ran on each response - used by the GUI application to handle the new responses
	// OnIntercept runs synchronously after an HTTP Checkpoint item becomes pending.
	// Its return error is ignored. A blocking callback blocks the intercepted execution.
	OnIntercept func(item domain.CheckpointItem) error
	OnLog       func(log domain.Log) error // Function to be ran on each log event - used by the GUI application to handle new log entries
	// OnWebSocketOpen is called when a WebSocket connection opens.
	OnWebSocketOpen func(domain.WebSocketConnection) error
	// OnWebSocketMessage is called for each processed WebSocket message.
	OnWebSocketMessage func(domain.WebSocketMessage) error
	// OnWebSocketClose is called when a WebSocket connection closes.
	OnWebSocketClose func(domain.WebSocketConnection) error
	// OnWebSocketIntercept is called when a WebSocket message is paused for inspection.
	OnWebSocketIntercept  func(domain.WebSocketMessage) error
	Addr                  string                // IP Address of the proxy
	Port                  string                // Port of the proxy
	Client                *http.Client          // HTTP Client that is used by the repeater functionality (autoconfigured to use the proxy)
	Extensions            []*extensions.Runtime // Slice of loaded extensions
	WordlistManager       wordlist.Provider     // Provider for available wordlists.
	SPKIHash              string                // SPKI Hash of the current certificate
	Cert                  *x509.Certificate     // The proxy's TLS certificate.
	mitmConfig            *tls.Config           // Martian Proxy MITM config
	MarasiClientTLSConfig *tls.Config           // TLSConfig for the proxy.Client
	Scope                 *compass.Scope        // Proxy scope configuration through Compass
	Waypoints             map[string]string     // Published host:port overrides. Replaced, never mutated.
	waypointMu            sync.RWMutex
	// WebSocketRegistry tracks live WebSocket connections.
	WebSocketRegistry *marasiws.Registry
	// WebSocketInterceptor pauses WebSocket messages for manual inspection.
	WebSocketInterceptor *marasiws.Interceptor
	checkpoint           *checkpointState
	httpIntercept        atomic.Bool
	webSocketIntercept   atomic.Bool
	webSocketLifecycleMu sync.RWMutex
	webSocketSessions    sync.WaitGroup
	webSocketsClosing    bool
	martianCloseOnce     sync.Once
	martianCloseDone     chan struct{}
	connectionMu         sync.Mutex
	connectionSessions   sync.WaitGroup
	connections          map[net.Conn]struct{}
	clientConnections    map[string]*listener.WatchedConn
	connectionsClosing   bool
	transportContext     context.Context
	cancelTransport      context.CancelFunc
	listenerMu           sync.Mutex
	activeListener       net.Listener
	activeServeDone      chan struct{}
	dbWriterStarted      atomic.Bool
	launchpadWSMu        sync.Mutex
	launchpadWS          map[io.Closer]struct{}

	TrafficRepo   domain.TrafficRepository   // Repository for traffic data.
	LaunchpadRepo domain.LaunchpadRepository // Repository for launchpad data.
	ArmoryRepo    domain.ArmoryRepository    // Repository for Armory data.
	WaypointRepo  domain.WaypointRepository  // Repository for waypoint data.
	StatsRepo     domain.StatsRepository     // Repository for statistics data.
	ConfigRepo    domain.ConfigRepository    // Repository for configuration data.
	LogRepo       domain.LogRepository       // Repository for log data.
	ExtensionRepo domain.ExtensionRepository // Repository for extension data.
	ReportingRepo domain.ReportingRepository // Repository for reporting data.
	WebSocketRepo domain.WebSocketRepository // Repository for websocket data

	Armory          ArmoryService          // Provides persistence and execution operations for request fuzzing.
	ReportGenerator domain.ReportGenerator // Generator for report templates and exports.
	DBCloser        io.Closer              // Closer for the database connection.
	Logger          *slog.Logger           // Logger for Marasi
	admitWork       func(context.Context, bool) (func(), error)
	clientWorkToken string
}

// ProjectResources is the complete set of dependencies owned by one open project.
type ProjectResources struct {
	Repository      RepositoryProvider
	Extensions      []*extensions.Runtime
	Scope           *compass.Scope
	Waypoints       map[string]string
	Armory          ArmoryService
	ReportGenerator domain.ReportGenerator
}

type dbWriteBarrier struct {
	done chan struct{}
}

// GetConfigDir returns the configuration directory path.
// It returns an error if the configuration directory is not set.
func (proxy *Proxy) GetConfigDir() (string, error) {
	if proxy.ConfigDir == "" {
		return "", ErrConfigDirNotSet
	}
	return proxy.ConfigDir, nil
}

// GetWordlistManager returns the proxy's wordlist provider.
// It returns an error if the provider is not set.
func (proxy *Proxy) GetWordlistManager() (wordlist.Provider, error) {
	if proxy.WordlistManager == nil {
		return nil, ErrWordlistManagerNotSet
	}
	return proxy.WordlistManager, nil
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

// GetTrafficRepo returns the traffic repository.
// It returns an error if the repository is not set.
func (proxy *Proxy) GetTrafficRepo() (domain.TrafficRepository, error) {
	if proxy.TrafficRepo == nil {
		return nil, ErrExtensionRepoNotFound
	}
	return proxy.TrafficRepo, nil
}

// GetLaunchpadRepo returns the launchpad repository.
func (proxy *Proxy) GetLaunchpadRepo() (domain.LaunchpadRepository, error) {
	if proxy.LaunchpadRepo == nil {
		return nil, ErrLaunchpadRepoNotFound
	}
	return proxy.LaunchpadRepo, nil
}

// GetWaypointRepo returns the waypoint repository.
func (proxy *Proxy) GetWaypointRepo() (domain.WaypointRepository, error) {
	if proxy.WaypointRepo == nil {
		return nil, ErrWaypointRepoNotFound
	}
	return proxy.WaypointRepo, nil
}

// GetReportingRepo returns the reporting repository.
// It returns an error if the repository is not set.
func (proxy *Proxy) GetReportingRepo() (domain.ReportingRepository, error) {
	if proxy.ReportingRepo == nil {
		return nil, ErrReportingRepoNotFound
	}
	return proxy.ReportingRepo, nil
}

// GetArmoryRepo returns the Armory repository.
// It returns an error if the repository is not set.
func (proxy *Proxy) GetArmoryRepo() (domain.ArmoryRepository, error) {
	if proxy.ArmoryRepo == nil {
		return nil, ErrArmoryRepoNotFound
	}
	return proxy.ArmoryRepo, nil
}

// GetArmory returns the Armory service.
// It returns an error if the service is not set.
func (proxy *Proxy) GetArmory() (ArmoryService, error) {
	if proxy.Armory == nil {
		return nil, ErrArmoryNotFound
	}
	return proxy.Armory, nil
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
	transportContext, cancelTransport := context.WithCancel(context.Background())
	proxy := &Proxy{
		transportContext:     transportContext,
		cancelTransport:      cancelTransport,
		connections:          make(map[net.Conn]struct{}),
		clientConnections:    make(map[string]*listener.WatchedConn),
		martianProxy:         martian.NewProxy(),
		Modifiers:            fifo.NewGroup(),
		DBWriteChannel:       make(chan any, 10),
		Extensions:           make([]*extensions.Runtime, 0),
		Client:               &http.Client{},
		Scope:                compass.NewScope(true),
		Waypoints:            make(map[string]string),
		Logger:               slog.Default(),
		WebSocketRegistry:    marasiws.NewRegistry(),
		WebSocketInterceptor: marasiws.NewInterceptor(),
		checkpoint:           newCheckpointState(),
		launchpadWS:          make(map[io.Closer]struct{}),
		clientWorkToken:      uuid.NewString(),
		admitWork: func(context.Context, bool) (func(), error) {
			return func() {}, nil
		},
	}
	err := proxy.WithOptions(options...)
	if err != nil {
		return nil, err
	}
	return proxy, nil
}

// SetProjectResources replaces every project-owned dependency. Callers must
// block project work before calling it.
func (proxy *Proxy) SetProjectResources(resources ProjectResources) {
	proxy.TrafficRepo = resources.Repository
	proxy.LaunchpadRepo = resources.Repository
	proxy.ArmoryRepo = resources.Repository
	proxy.WaypointRepo = resources.Repository
	proxy.StatsRepo = resources.Repository
	proxy.ConfigRepo = resources.Repository
	proxy.LogRepo = resources.Repository
	proxy.ExtensionRepo = resources.Repository
	proxy.ReportingRepo = resources.Repository
	proxy.WebSocketRepo = resources.Repository
	proxy.DBCloser = resources.Repository
	proxy.Extensions = resources.Extensions
	proxy.Scope = resources.Scope
	proxy.waypointMu.Lock()
	proxy.Waypoints = cloneWaypointRoutes(resources.Waypoints)
	proxy.waypointMu.Unlock()
	proxy.Armory = resources.Armory
	proxy.ReportGenerator = resources.ReportGenerator
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

// ApplyWaypointChange persists a waypoint change and publishes the resulting
// routes as one snapshot. change reports whether that snapshot should replace
// the live routes. change must not call back into the proxy.
func (proxy *Proxy) ApplyWaypointChange(change func(domain.WaypointRepository) (publish bool, err error)) (bool, error) {
	proxy.waypointMu.Lock()
	defer proxy.waypointMu.Unlock()
	if proxy.WaypointRepo == nil {
		return false, fmt.Errorf("WaypointRepository not set")
	}
	publish, err := change(proxy.WaypointRepo)
	if err != nil || !publish {
		return false, err
	}
	return true, proxy.publishWaypointSnapshot()
}

// SyncWaypoints reloads waypoints and publishes them as the live routing snapshot.
func (proxy *Proxy) SyncWaypoints() error {
	proxy.waypointMu.Lock()
	defer proxy.waypointMu.Unlock()
	return proxy.publishWaypointSnapshot()
}

// publishWaypointSnapshot reloads waypoints and replaces the live snapshot.
// The caller holds waypointMu.
func (proxy *Proxy) publishWaypointSnapshot() error {
	if proxy.WaypointRepo == nil {
		return fmt.Errorf("WaypointRepository not set")
	}
	waypointSlice, err := proxy.WaypointRepo.GetWaypoints()
	if err != nil {
		log.Printf("syncing waypoints: %v", err)
		return err
	}

	waypointsMap := make(map[string]string, len(waypointSlice))
	for _, waypoint := range waypointSlice {
		waypointsMap[waypoint.Hostname] = waypoint.Override
	}
	proxy.Waypoints = waypointsMap
	return nil
}

func (proxy *Proxy) waypointOverride(hostport string) (string, bool) {
	proxy.waypointMu.RLock()
	defer proxy.waypointMu.RUnlock()
	if proxy.Waypoints == nil {
		return "", false
	}
	override, ok := proxy.Waypoints[hostport]
	return override, ok
}

func cloneWaypointRoutes(routes map[string]string) map[string]string {
	if routes == nil {
		return nil
	}
	snapshot := make(map[string]string, len(routes))
	for hostname, override := range routes {
		snapshot[hostname] = override
	}
	return snapshot
}

// GetExtension retrieves a loaded extension by its name.
// It returns the extension and true if found, otherwise nil and false.
func (proxy *Proxy) GetExtension(name string) (*extensions.Runtime, bool) {
	for _, ext := range proxy.Extensions {
		if ext.Data.Name == name {
			return ext, true
		}
	}
	return nil, false
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

		currentHost := req.Host
		currentURLHost := req.URL.Host
		originalHost := ""

		if host, ok := metadata["original_host"].(string); ok && host != "" { // Lua heading override
			originalHost = host
		} else if host, ok := metadata["original_host_header"].(string); ok && host != "" { // Waypoint redirect
			originalHost = host
		}

		if originalHost != "" {
			if originalHost != currentHost {
				req.Host = originalHost
			}
			if currentURLHost != "" && currentURLHost != originalHost {
				req.URL.Host = originalHost
			}
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

		req.Host = currentHost
		req.URL.Host = currentURLHost

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
// It handles proxy traffic, websocket traffic, and log items.
func (proxy *Proxy) WriteToDB() {
	for proxyItem := range proxy.DBWriteChannel {
		switch castItem := proxyItem.(type) {
		case *domain.ProxyRequest:
			err := proxy.TrafficRepo.InsertRequest(castItem)
			if err != nil {
				log.Println(err)
				continue
			}

			if val, ok := castItem.Metadata["launchpad_id"]; ok {
				if launchpadID, ok := val.(uuid.UUID); ok {
					err := proxy.LaunchpadRepo.LinkRequestToLaunchpad(castItem.ID, launchpadID)
					if err != nil {
						log.Printf("linking request to launchpad: %v", err)
					}
				}
			}

			if runID, ok := castItem.Metadata["armory_run_id"].(uuid.UUID); ok {
				if proxy.Armory == nil {
					log.Printf("linking request to armory run: armory service is not configured")
				} else if repo := proxy.Armory.Repo(); repo == nil {
					log.Printf("linking request to armory run: armory repository is not configured")
				} else {
					if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: runID, RequestID: castItem.ID}); err != nil {
						log.Printf("linking request to armory run: %v", err)
					}
				}
			}
		case *domain.ProxyResponse:
			err := proxy.TrafficRepo.InsertResponse(castItem)
			if err != nil {
				log.Println(err)
			}
		case *domain.WebSocketConnection:
			err := proxy.WebSocketRepo.InsertConnection(castItem)
			if err != nil {
				log.Println(err)
			}
		case *domain.WebSocketConnectionUpdate:
			err := proxy.WebSocketRepo.UpdateConnection(&castItem.Connection)
			if err != nil {
				log.Println(err)
			}
		case *domain.WebSocketMessage:
			err := proxy.WebSocketRepo.InsertMessage(castItem)
			if err != nil {
				log.Println(err)
			}
		case *domain.Log:
			err := proxy.LogRepo.InsertLog(castItem)
			if err != nil {
				log.Print(err)
			}
			if proxy.OnLog != nil {
				proxy.OnLog(*castItem)
			}
		case *dbWriteBarrier:
			close(castItem.done)
		default:
			log.Print(castItem)
		}
	}
}

func (proxy *Proxy) flushDBWrites() error {
	if !proxy.dbWriterStarted.Load() || proxy.DBWriteChannel == nil {
		return nil
	}

	barrier := &dbWriteBarrier{done: make(chan struct{})}
	proxy.DBWriteChannel <- barrier
	<-barrier.done
	return nil
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

func (proxy *Proxy) GetListener(address string, port string) (net.Listener, error) {
	rawListener, err := net.Listen("tcp", net.JoinHostPort(address, port))
	if err != nil {
		return nil, fmt.Errorf("setting up listener on address:port %s:%s: %w", address, port, err)
	}
	addr := rawListener.Addr().(*net.TCPAddr)

	if addr.IP.IsUnspecified() {
		proxy.Addr = "127.0.0.1"
	} else {
		proxy.Addr = addr.IP.String()
	}
	proxy.Port = fmt.Sprintf("%d", addr.Port)

	muxListener := listener.NewProtocolMuxListener(rawListener, proxy.mitmConfig)
	muxListener.WrapConn = func(conn net.Conn) net.Conn { return proxy.trackConnection(conn, true) }
	marasiListener := listener.NewMarasiListener(muxListener)

	proxy.WriteLog("INFO", fmt.Sprintf("Marasi Service Started on %s", rawListener.Addr().String()))

	hostPort := net.JoinHostPort(proxy.Addr, proxy.Port)
	parsedURL, err := url.Parse(fmt.Sprintf("http://%s", hostPort))
	if err != nil {
		log.Fatal(fmt.Errorf("error parsing proxy URL: %w", err))
	}

	proxy.Logger.Info("Proxy Client Configured", "url", parsedURL.String())
	parsedURL.User = url.UserPassword("marasi", proxy.clientWorkToken)

	transport := &http.Transport{
		Proxy:           http.ProxyURL(parsedURL),
		TLSClientConfig: proxy.MarasiClientTLSConfig,
	}
	proxy.Client.Transport = transport
	return marasiListener, nil
}

// ActiveListenerAddress returns the bound address of the listener currently
// served by the proxy.
func (proxy *Proxy) ActiveListenerAddress() (string, bool) {
	proxy.listenerMu.Lock()
	defer proxy.listenerMu.Unlock()
	if proxy.activeListener == nil {
		return "", false
	}
	return proxy.activeListener.Addr().String(), true
}

// Serve starts the proxy and begins accepting connections on the provided listener.
// It also starts the database writer goroutine.
func (proxy *Proxy) Serve(activeListener net.Listener) error {
	if proxy.dbWriterStarted.CompareAndSwap(false, true) {
		go proxy.WriteToDB()
	}
	// Binding queues "Marasi Service Started" before the writer runs. Publish
	// it before the listener reports ready, or a subscriber can see it late.
	if err := proxy.flushDBWrites(); err != nil {
		return err
	}
	roundTripper := newMarasiTransport(proxy.Cert, proxy.dialTransport)
	proxy.martianProxy.SetRoundTripper(roundTripper)
	proxy.listenerMu.Lock()
	proxy.activeListener = activeListener
	serveDone := make(chan struct{})
	proxy.activeServeDone = serveDone
	proxy.listenerMu.Unlock()
	defer func() {
		proxy.listenerMu.Lock()
		close(serveDone)
		if proxy.activeListener == activeListener {
			proxy.activeListener = nil
		}
		proxy.listenerMu.Unlock()
	}()

	return proxy.martianProxy.Serve(listener.NewClosingListener(
		activeListener,
		proxy.martianProxy.Closing,
	))
}

// Close shuts down the proxy and closes the database connection.
func (proxy *Proxy) Close() error {
	return proxy.close(context.Background(), true)
}

// CloseTransport stops listeners and live connections without closing the
// open project's database. The service project lifecycle closes that resource.
func (proxy *Proxy) CloseTransport() error {
	return proxy.CloseTransportContext(context.Background())
}

// CloseTransportContext allows traffic to finish until ctx expires, then
// interrupts network work and joins its handlers before flushing persistence.
func (proxy *Proxy) CloseTransportContext(ctx context.Context) error {
	return proxy.close(ctx, false)
}

func (proxy *Proxy) close(ctx context.Context, closeDatabase bool) error {
	stopForceClose := context.AfterFunc(ctx, proxy.ForceCloseTransport)
	defer stopForceClose()
	proxy.webSocketLifecycleMu.Lock()
	proxy.webSocketsClosing = true
	proxy.webSocketLifecycleMu.Unlock()

	proxy.martianCloseOnce.Do(func() {
		proxy.martianCloseDone = make(chan struct{})
		go func() {
			proxy.martianProxy.Close()
			close(proxy.martianCloseDone)
		}()
	})
	for !proxy.martianProxy.Closing() {
		time.Sleep(time.Millisecond)
	}

	var listenerErr error
	proxy.listenerMu.Lock()
	serveDone := proxy.activeServeDone
	if proxy.activeListener != nil {
		listenerErr = proxy.activeListener.Close()
		if errors.Is(listenerErr, net.ErrClosed) {
			listenerErr = nil
		}
	}
	proxy.listenerMu.Unlock()

	proxy.closeLaunchpadWebSockets()
	proxy.DropAllCheckpoint()
	webSocketErr := proxy.CloseWebSocketsAndFlush()
	if proxy.Armory != nil {
		proxy.Armory.Shutdown()
	}
	if serveDone != nil {
		<-serveDone
	}
	<-proxy.martianCloseDone
	// Martian increments its handler wait count inside the new goroutine.
	// Join accepted sockets too, including not-yet-scheduled handlers.
	proxy.connectionSessions.Wait()
	proxy.ForceCloseTransport()
	flushErr := proxy.flushDBWrites()
	var databaseErr error
	if closeDatabase && proxy.DBCloser != nil {
		if proxy.Logger != nil {
			proxy.Logger.Info("Closing database connection")
		}
		databaseErr = proxy.DBCloser.Close()
	}

	return errors.Join(listenerErr, webSocketErr, flushErr, databaseErr)
}

// Track sockets before protocol inspection or TLS handshakes so forced shutdown
// also interrupts clients and upstreams that have not sent HTTP headers yet.
// Inbound sockets are watched and indexed by client address so a client disconnect can cancel its request.
func (proxy *Proxy) trackConnection(conn net.Conn, inbound bool) net.Conn {
	var watched *listener.WatchedConn
	clientAddress := conn.RemoteAddr().String()
	if inbound {
		watched = listener.NewWatchedConnection(conn)
		conn = watched
	}
	proxy.connectionMu.Lock()
	if inbound {
		proxy.connectionSessions.Add(1)
		proxy.clientConnections[clientAddress] = watched
	}
	proxy.connections[conn] = struct{}{}
	closing := proxy.connectionsClosing
	proxy.connectionMu.Unlock()
	if closing {
		conn.Close()
	}
	return listener.NewTrackedConnection(conn, func() {
		proxy.connectionMu.Lock()
		delete(proxy.connections, conn)
		if inbound && proxy.clientConnections[clientAddress] == watched {
			delete(proxy.clientConnections, clientAddress)
		}
		proxy.connectionMu.Unlock()
		if inbound {
			proxy.connectionSessions.Done()
		}
	})
}

// maxEagerBody bounds the request body watchClient reads before the pipeline runs.
const maxEagerBody = 1 << 20

// watchClient cancels the request context when its client disconnects while the request
// is in flight, which interrupts its hooks. The socket is watched only after the body has
// been read, so the watch never takes body bytes. A small body of known length is read now,
// so hooks are watched. A larger, chunked, or Expect: 100-continue body is watched once it
// has been read through.
func (proxy *Proxy) watchClient(req *http.Request) {
	proxy.connectionMu.Lock()
	conn, ok := proxy.clientConnections[req.RemoteAddr]
	proxy.connectionMu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	*req = *req.WithContext(ctx)

	if req.Body == nil || req.Body == http.NoBody {
		conn.Watch(cancel)
		return
	}
	if req.ContentLength > 0 && req.ContentLength <= maxEagerBody && req.Header.Get("Expect") == "" {
		body, err := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil {
			cancel()
			return
		}
		conn.Watch(cancel)
		return
	}
	req.Body = &watchAtEOF{ReadCloser: req.Body, watch: func() { conn.Watch(cancel) }}
}

// watchAtEOF starts watching the client once the request body has been read through.
type watchAtEOF struct {
	io.ReadCloser
	once  sync.Once
	watch func()
}

func (body *watchAtEOF) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if err == io.EOF {
		body.once.Do(body.watch)
	}
	return n, err
}

func (proxy *Proxy) dialTransport(ctx context.Context, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(proxy.transportContext, cancel)
	defer stop()
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return proxy.trackConnection(conn, false), nil
}

// ForceCloseTransport interrupts network work without releasing handler ownership.
// Full shutdown still joins those handlers before closing persistence.
func (proxy *Proxy) ForceCloseTransport() {
	proxy.cancelTransport()
	proxy.connectionMu.Lock()
	proxy.connectionsClosing = true
	connections := make([]net.Conn, 0, len(proxy.connections))
	for conn := range proxy.connections {
		connections = append(connections, conn)
	}
	proxy.connectionMu.Unlock()
	for _, conn := range connections {
		// Interrupt I/O without releasing the handler's wait count.
		conn.Close()
	}
}

// StartChrome launches Chrome with proxy configuration and security settings.
// It configures Chrome to use the proxy server, creates an isolated user profile,
// and disables various Chrome features that might interfere with testing.
//
// Returns:
//   - error: Chrome launch error if executable not found or process fails to start
func (proxy *Proxy) StartChrome(profile string) error {
	launchProfile := "default-profile"
	if profile != "" {
		if !slices.Contains(proxy.Config.ChromeProfiles, profile) {
			return fmt.Errorf("invalid launch request: profile %q is not configured", profile)
		}
		launchProfile = profile
	}

	launcher := chrome.NewLauncher(
		chrome.WithProxy(proxy.Addr, proxy.Port),
		chrome.WithSPKIHash(proxy.SPKIHash),
		chrome.WithConfigDir(proxy.ConfigDir),
		chrome.WithProfile(launchProfile),
		chrome.WithCustomPaths(proxy.Config.ChromeDirs),
	)

	return launcher.Start()
}
