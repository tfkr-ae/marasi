package marasi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/tfkr-ae/marasi/chrome"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	marasiws "github.com/tfkr-ae/marasi/websocket"
	"github.com/tfkr-ae/marasi/wordlist"
)

// RepositoryProvider defines the interface for a provider of all data repositories
// used by the proxy. This allows for easy injection of a database implementation.
// In the service, the proxy holds it wrapped in a log-publishing repository, so a
// type assertion to *db.Repository or to a wider interface fails silently there,
// even though it passes in tests that use a bare *db.Repository. Add the method
// here instead.
type RepositoryProvider interface {
	domain.TrafficRepository
	domain.ExtensionRepository
	domain.LaunchpadRepository
	domain.ArmoryRepository
	domain.WaypointRepository
	domain.StatsRepository
	domain.ConfigRepository
	domain.LogRepository
	domain.ReportingRepository
	domain.WebSocketRepository
	io.Closer
	// RefreshStatisticsIfDue refreshes the planner statistics when captures
	// have marked them due (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md). The database writer calls it when idle.
	RefreshStatisticsIfDue()
}

// WithOptions applies a series of configuration functions to the proxy instance.
// Each option function can modify the proxy configuration and return an error if it fails.
//
// Parameters:
//   - options: Variadic list of configuration functions
//
// Returns:
//   - error: First error encountered from any option function
func (proxy *Proxy) WithOptions(options ...func(*Proxy) error) error {
	for _, option := range options {
		err := option(proxy)
		if err != nil {
			return fmt.Errorf("applying option on marasi: %w", err)
		}
	}
	return nil
}

// WithConfigDir configures the proxy to use the specified configuration directory.
// It creates the directory if it doesn't exist and initializes the configuration file using Viper.
//
// Parameters:
//   - appConfigDir: Path to the configuration directory
//
// Returns:
//   - func(*Proxy) error: Configuration function that sets up the config directory
func WithConfigDir(appConfigDir string) func(*Proxy) error {
	return func(proxy *Proxy) error {
		_, err := os.ReadDir(appConfigDir)
		if err != nil {
			if os.IsNotExist(err) {
				proxy.Logger.Info("Creating config directory")
				err = os.MkdirAll(appConfigDir, 0700)
				if err != nil {
					return fmt.Errorf("creating config dir %s: %w", appConfigDir, err)
				}
			} else {
				return fmt.Errorf("checking if directory exists %s: %w", appConfigDir, err)
			}
		}
		// At this point, the directory exists or was created successfully
		proxy.ConfigDir = appConfigDir
		// VIPER
		viperInstance := viper.New()
		viperInstance.SetConfigName("marasi_config")
		viperInstance.SetConfigType("yaml")
		viperInstance.AddConfigPath(appConfigDir)
		viperInstance.SetDefault("chrome_dirs", []chrome.PathConfig{})
		viperInstance.SetDefault("chrome_profiles", []string{})
		// The file can come to hold upstream proxy credentials, so it is
		// created owner-only.
		viperInstance.SetConfigPermissions(configFileMode)
		err = viperInstance.ReadInConfig()
		if err != nil {
			// need to check if the error is config file doesn't exist
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				// Config file is not found
				err = viperInstance.SafeWriteConfig()
				if err != nil {
					if readErr := viperInstance.ReadInConfig(); readErr != nil {
						return errors.Join(
							fmt.Errorf("writing config file: %w", err),
							fmt.Errorf("reading concurrently created config file: %w", readErr),
						)
					}
				}
			} else {
				return fmt.Errorf("reading config file: %w", err)
			}
		}
		if err := viperInstance.Unmarshal(&proxy.Config); err != nil {
			return fmt.Errorf("unmarshalling config to struct: %w", err)
		}
		if err := proxy.Config.validate(); err != nil {
			return fmt.Errorf("invalid configuration: %w", err)
		}
		if err := restrictConfigWithCredentials(filepath.Join(appConfigDir, "marasi_config.yaml"), proxy.Config.UpstreamProxies); err != nil {
			return err
		}
		proxy.Config.viper = viperInstance

		proxy.Config.DesktopOS = runtime.GOOS
		proxy.Config.ConfigDir = appConfigDir
		return nil
	}
}

// TODO: Need to fix this, currently if the extension exists it does not overrite it, this breaks opening new projects
// WithExtension loads a single extension into the proxy.
// It prepares the extension's Lua state and adds it to the proxy's extension list.
func WithExtension(extension *domain.Extension, options ...func(*extensions.Runtime) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		// Check if the map is nil and create if it is
		if proxy.Extensions == nil {
			proxy.Extensions = make([]*extensions.Runtime, 0)
		}
		// Check if the extension doesn't exist
		if _, ok := proxy.GetExtension(extension.Name); !ok {
			ext := &extensions.Runtime{Data: extension}
			err := ext.PrepareState(proxy, options)
			if err != nil {
				return fmt.Errorf("preparing extension %s: %w", extension.Name, err)
			}
			proxy.Extensions = append(proxy.Extensions, ext)
		}

		return nil
	}
}

// WithExtensions loads multiple extensions into the proxy.
// It iterates through the provided extensions and prepares each one.
func WithExtensions(exts []*domain.Extension, options ...func(*extensions.Runtime) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.Extensions = make([]*extensions.Runtime, 0)
		for _, extension := range exts {
			if _, ok := proxy.GetExtension(extension.Name); !ok {
				ext := &extensions.Runtime{Data: extension}
				// Extension does not exist
				// if it is enabled add it, if not keep it disabled
				ext.PrepareState(proxy, options)
				proxy.Extensions = append(proxy.Extensions, ext)
			}
		}

		return nil
	}
}

// WithInterceptHandler sets OnIntercept for pending HTTP Checkpoint items.
// The handler runs synchronously, and its return error is ignored.
// A blocking handler blocks the intercepted execution.
func WithInterceptHandler(handler func(item domain.CheckpointItem) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnIntercept != nil {
			return errors.New("proxy already has an intercept handler defined")
		}
		proxy.OnIntercept = handler
		return nil
	}
}

// WithLaunchStartedHandler sets OnLaunchStarted, called once a launch is accepted.
func WithLaunchStartedHandler(handler func(domain.Launch)) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnLaunchStarted != nil {
			return errors.New("proxy already has a launch started handler defined")
		}
		proxy.OnLaunchStarted = handler
		return nil
	}
}

// WithLaunchFinishedHandler sets OnLaunchFinished, called once per launch on its terminal state.
func WithLaunchFinishedHandler(handler func(domain.Launch)) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnLaunchFinished != nil {
			return errors.New("proxy already has a launch finished handler defined")
		}
		proxy.OnLaunchFinished = handler
		return nil
	}
}

// WithCheckpointAbandonedHandler sets OnCheckpointAbandoned for Checkpoint items
// dropped because their client left or their WebSocket connection closed. Its return
// error is ignored.
func WithCheckpointAbandonedHandler(handler func(item domain.CheckpointItem) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnCheckpointAbandoned != nil {
			return errors.New("proxy already has a checkpoint abandoned handler defined")
		}
		proxy.OnCheckpointAbandoned = handler
		return nil
	}
}

// WithResponseHandler takes a handler function that will be executed on each response
func WithResponseHandler(handler func(res domain.ProxyResponse) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnResponse != nil {
			return errors.New("proxy already has a response handler defined")
		}
		proxy.OnResponse = handler
		return nil
	}
}

// WithRequestHandler takes a handler function that will be executed on each Request
func WithRequestHandler(handler func(req domain.ProxyRequest) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnRequest != nil {
			return errors.New("proxy already has a request handler defined")
		}
		proxy.OnRequest = handler
		return nil
	}
}

// WithLogHandler takes a handler function that will be executed on each Log
func WithLogHandler(handler func(log domain.Log) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnLog != nil {
			return errors.New("proxy already has a log handler defined")
		}
		proxy.OnLog = handler
		return nil
	}
}

// WithWebSocketOpenHandler takes a handler function that will be executed when a WebSocket connection opens.
func WithWebSocketOpenHandler(handler func(domain.WebSocketConnection) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnWebSocketOpen != nil {
			return errors.New("proxy already has a websocket open handler defined")
		}
		proxy.OnWebSocketOpen = handler
		return nil
	}
}

// WithWebSocketMessageHandler takes a handler function that will be executed for each WebSocket message.
func WithWebSocketMessageHandler(handler func(domain.WebSocketMessage) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnWebSocketMessage != nil {
			return errors.New("proxy already has a websocket message handler defined")
		}
		proxy.OnWebSocketMessage = handler
		return nil
	}
}

// WithWebSocketProgressHandler takes a handler function that will be executed
// while a WebSocket message's frames are still arriving. It must not block.
func WithWebSocketProgressHandler(handler func(domain.WebSocketMessageProgress) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnWebSocketProgress != nil {
			return errors.New("proxy already has a websocket progress handler defined")
		}
		proxy.OnWebSocketProgress = handler
		return nil
	}
}

// WithWebSocketCloseHandler takes a handler function that will be executed when a WebSocket connection closes.
func WithWebSocketCloseHandler(handler func(domain.WebSocketConnection) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnWebSocketClose != nil {
			return errors.New("proxy already has a websocket close handler defined")
		}
		proxy.OnWebSocketClose = handler
		return nil
	}
}

// WithWebSocketHandshakeEndedHandler sets OnWebSocketHandshakeEnded, called
// once an upgrade that failed or was dropped has its websocket.state stored.
func WithWebSocketHandshakeEndedHandler(handler func(requestID uuid.UUID, state string)) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnWebSocketHandshakeEnded != nil {
			return errors.New("proxy already has a websocket handshake ended handler defined")
		}
		proxy.OnWebSocketHandshakeEnded = handler
		return nil
	}
}

// WithWebSocketInterceptHandler takes a handler function that will be executed when a WebSocket message is intercepted.
func WithWebSocketInterceptHandler(handler func(domain.WebSocketMessage) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnWebSocketIntercept != nil {
			return errors.New("proxy already has a websocket intercept handler defined")
		}
		proxy.OnWebSocketIntercept = handler
		return nil
	}
}

// WithTLS will configure the proxy CA based on the proxy.ConfigDir
// It will also configure the http.Client that is used for the launchpad requests
// TODO - Check if the certificate expired
func WithTLS() func(*Proxy) error {
	return WithTLSContext(context.Background())
}

// WithTLSContext configures TLS like WithTLS and allows cancellation while
// waiting for another process to finish initializing the shared CA.
func WithTLSContext(ctx context.Context) func(*Proxy) error {
	return func(proxy *Proxy) error {
		certificate, privateKey, err := initializeCertificateAuthority(ctx, proxy.ConfigDir, proxy.Logger)
		if err != nil {
			return err
		}

		proxy.SPKIHash = getSPKIHash(certificate)
		proxy.Cert = certificate
		err = proxy.ConfigRepo.UpdateSPKI(proxy.SPKIHash)
		if err != nil {
			return fmt.Errorf("setting spki hash %s: %w", proxy.SPKIHash, err)
		}
		mitmConfig, err := proxy.engine.setMITM(certificate, privateKey)
		if err != nil {
			return err
		}
		proxy.mitmConfig = mitmConfig

		// Add system certificates + marasi cert
		systemPool, err := x509.SystemCertPool()
		if err != nil {
			return fmt.Errorf("fetching system cert pool: %w", err)
		}
		systemPool.AddCert(certificate)
		proxy.MarasiClientTLSConfig = &tls.Config{
			RootCAs: systemPool,
		}
		return nil
	}
}

// WithDefaultRepositories is a convenience option to apply all repository implementations
// from a single provider.
func WithDefaultRepositories(repo RepositoryProvider) func(*Proxy) error {
	return func(proxy *Proxy) error {
		return proxy.WithOptions(
			WithLogRepository(repo),
			WithTrafficRepository(repo),
			WithConfigRepository(repo),
			WithStatsRepository(repo),
			WithExtensionRepository(repo),
			WithLaunchpadRepository(repo),
			WithArmoryRepository(repo),
			WithWaypointRepository(repo),
			WithReportingRepository(repo),
			WithWebSocketRepository(repo),
			WithDBCloser(repo),
		)
	}
}

// WithDBCloser injects the database closer.
func WithDBCloser(closer io.Closer) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.DBCloser = closer
		return nil
	}
}

// WithExtensionRepository injects the extension repository implementation.
func WithExtensionRepository(repo domain.ExtensionRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.ExtensionRepo = repo
		return nil
	}
}

// WithTrafficRepository injects the traffic repository implementation.
func WithTrafficRepository(repo domain.TrafficRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.TrafficRepo = repo
		return nil
	}
}

// WithLaunchpadRepository injects the launchpad repository implementation.
func WithLaunchpadRepository(repo domain.LaunchpadRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.LaunchpadRepo = repo
		return nil
	}
}

// WithArmoryRepository injects the Armory repository implementation.
func WithArmoryRepository(repo domain.ArmoryRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.ArmoryRepo = repo
		return nil
	}
}

// WithWaypointRepository injects the waypoint repository implementation.
func WithWaypointRepository(repo domain.WaypointRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.WaypointRepo = repo
		err := proxy.SyncWaypoints()
		if err != nil {
			proxy.WriteLog("INFO", err.Error())
		}
		return nil
	}
}

// WithStatsRepository injects the stats repository implementation.
func WithStatsRepository(repo domain.StatsRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.StatsRepo = repo
		return nil
	}
}

// WithUpstreamProxyTimeout bounds how long reaching an upstream proxy and
// opening a tunnel through it may take before the request fails. The
// default is DefaultUpstreamProxyTimeout.
func WithUpstreamProxyTimeout(timeout time.Duration) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if timeout <= 0 {
			return fmt.Errorf("upstream proxy timeout must be positive, got %s", timeout)
		}
		proxy.routes.setTimeout(timeout)
		return nil
	}
}

// WithConfigRepository injects the config repository implementation.
func WithConfigRepository(repo domain.ConfigRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.ConfigRepo = repo
		return nil
	}
}

// WithLogRepository injects the log repository implementation.
func WithLogRepository(repo domain.LogRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.LogRepo = repo
		return nil
	}
}

// WithReportingRepository injects the reporting repository implementation.
func WithReportingRepository(repo domain.ReportingRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.ReportingRepo = repo
		return nil
	}
}

// WithWebSocketRepository injects the WebSocket repository implementation.
func WithWebSocketRepository(repo domain.WebSocketRepository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.WebSocketRepo = repo
		return nil
	}
}

// WithArmory sets the Armory service exposed by the proxy.
func WithArmory(service ArmoryService) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if service == nil {
			return errors.New("armory service cannot be nil")
		}

		proxy.Armory = service
		return nil
	}
}

// WithReportGenerator injects the report generator implementation.
func WithReportGenerator(generator domain.ReportGenerator) func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.ReportGenerator = generator
		return nil
	}
}

// WithWordlistManager injects the wordlist provider implementation.
func WithWordlistManager(manager wordlist.Provider) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if manager == nil {
			return errors.New("wordlist manager cannot be nil")
		}
		proxy.WordlistManager = manager
		return nil
	}
}

// WithBrowserViewExpiry sets how long a pending browser view waits for its
// redirect to be followed. The default is 10 seconds (docs/adr/2026-10-20-a-browser-view-serves-the-stored-response-once-at-its-original-url.md).
func WithBrowserViewExpiry(expiry time.Duration) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if expiry <= 0 {
			return errors.New("browser view expiry must be positive")
		}
		proxy.browserViews.expiry = expiry
		return nil
	}
}

// WithWorkAdmission coordinates proxy traffic with service lifecycle changes.
// The boolean identifies requests made by the proxy's own client, including CONNECT.
// admit returns the work's release and the generation of the open project it
// runs in. A request is admitted for its request-side work and again for its
// response-side work, and a response admitted in another generation than its
// request skips every project-bound step (docs/adr/2026-10-17-project-switch-never-waits-on-the-network.md).
func WithWorkAdmission(admit func(context.Context, bool) (func(), uint64, error)) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if admit == nil {
			return errors.New("work admission cannot be nil")
		}
		proxy.admitWork = admit
		return nil
	}
}

// WithBasePipeline will setup the base modifier pipeline for marasi
// It will define the main Request & Response steps that will execute the
// attached modifiers and carry out the control a modifier set on the pair.
// Work is admitted for the request modifiers and again, once the response body
// has been read, for the response modifiers, never for the wait on the origin.
// If a response is dropped the client connection is taken over and closed. A
// 101 that the response chain passes is handed to the upgrade handler.
func WithBasePipeline() func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.engine.setPipeline(proxy.baseRequest, proxy.baseResponse)
		proxy.pipelineSet = true
		return nil
	}
}

// errNoClientConnection is returned when the proxy cannot take over the client
// connection of a pair.
var errNoClientConnection = errors.New("no client connection for the pair")

// baseRequest is the base pipeline's request step, run for every request the
// engine reads from client, including a CONNECT.
func (proxy *Proxy) baseRequest(req *http.Request, client clientHijacker, wire *wireRequest) error {
	// Proxy credentials must not reach the origin or extension scripts.
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("marasi:"+proxy.clientWorkToken))
	internal := req.Header.Get("Proxy-Authorization") == expected
	req.Header.Del("Proxy-Authorization")
	if req.Method == http.MethodConnect {
		if err := proxy.admitTunnel(req, internal); err != nil {
			return refuseConnection(client, err)
		}
		return nil
	}
	internal = internal || proxy.isSelfClientConnection(req.RemoteAddr)
	// Only the proxy's own client may name a launch. The header never leaves the proxy.
	launchID, launchErr := uuid.Parse(req.Header.Get(launchIDHeader))
	req.Header.Del(launchIDHeader)
	// The body is the wait on the client, so it is read before
	// the request side is admitted.
	bodyEnding := readRequestBody(req)
	release, generation, err := proxy.admitWork(req.Context(), internal)
	if err != nil {
		return refuseConnection(client, err)
	}
	// The wait on the origin is not project work, so the request
	// side releases its admission before the round trip (docs/adr/2026-10-17-project-switch-never-waits-on-the-network.md).
	defer release()
	pair := &Pair{requestState: requestState{
		admission: &projectAdmission{internal: internal, generation: generation},
		client:    client,
	}, RequestBodyEnding: bodyEnding, requestWire: wire}
	if internal && launchErr == nil {
		pair.requestedLaunch = launchID
	}
	*req = *req.WithContext(context.WithValue(req.Context(), pairKey{}, pair))
	proxy.watchClient(req, bodyEnding)
	pair.Request = req
	pair.ctx = req.Context()
	err = proxy.requestModifiers.Modify(proxy, pair)
	switch pair.control {
	case controlDrop:
		// The round trip answers a dropped request with an empty 200, as before.
		proxy.recordLaunchDropped(pair)
	case controlStop:
		// A stop ends only the request chain.
		pair.control = controlNone
	}
	if err != nil {
		// A Go modifier failed: the request fails closed and
		// never goes upstream half-modified.
		proxy.failRequestChain(pair, err)
	}
	return nil
}

// baseResponse is the base pipeline's response step, run for every response
// the engine is about to write, including the 200 that opens a tunnel.
func (proxy *Proxy) baseResponse(res *http.Response, wire *wireResponse) error {
	// The tunnel was admitted with its CONNECT; the 200 that
	// opens it is not a response of any pair.
	if res.Request.Method == http.MethodConnect {
		return nil
	}
	// A request the proxy answered itself (the round trip
	// returned its response) or dropped is sent as it is, with
	// no response modifier and no admission, unless the answer
	// is a proxy error response to a captured request.
	pair := requestPair(res.Request)
	if pair != nil && (pair.control == controlRespond || pair.Dropped()) {
		if !pair.capturesProxyError() {
			return nil
		}
		// A proxy error response is stored: the response chain
		// runs, and its hooks skip what Marasi made.
		pair.control = controlNone
	}
	// A launch whose client left was cancelled or interrupted. Its
	// round trip was abandoned, so it has no response to process or store.
	if _, launch := core.LaunchpadIDFromContext(res.Request.Context()); launch && res.Request.Context().Err() != nil {
		return nil
	}
	if pair == nil {
		pair = &Pair{}
	}
	loadPair(pair, res.Request, res)
	pair.responseWire = wire
	// The body is still the wait on the origin, so it is read
	// before the response side is admitted (docs/adr/2026-10-17-project-switch-never-waits-on-the-network.md).
	if err := readResponseBody(pair); err != nil {
		log.Printf("response pipeline: %v", err)
		return err
	}
	if admission := pair.admission; admission != nil {
		release, generation, err := proxy.admitWork(res.Request.Context(), admission.internal)
		if err != nil {
			// The client left while a handoff paused admission, or
			// the instance handed its project to a restart's successor.
			return nil
		}
		defer release()
		if generation != admission.generation {
			// The pair was captured in a project that is no longer
			// open. It stays response-less there (docs/adr/2026-10-17-project-switch-never-waits-on-the-network.md).
			return proxy.passStaleResponse(res)
		}
		pair.release = release
	}
	err := proxy.responseModifiers.Modify(proxy, pair)
	if pair.Dropped() {
		proxy.recordLaunchDropped(pair)
		proxy.markDroppedWebSocketHandshake(pair)
		if pair.client == nil {
			return errNoClientConnection
		}
		conn, _, err := pair.client.Hijack()
		if err != nil {
			return fmt.Errorf("hijacking client connection: %w", err)
		}
		if err := conn.Close(); err != nil {
			return fmt.Errorf("closing connection: %w", err)
		}
	}
	// A 101 goes to the upgrade handler only once the whole chain
	// has run, so the pair is captured.
	if err == nil && pair.control == controlNone {
		return proxy.handOffUpgrade(pair)
	}
	if err == nil {
		return nil
	}
	// TODO this should be handled through logging
	log.Printf("response pipeline: %v", err)
	return err
}

// refuseConnection ends the client connection of a refused request. The
// engine sends a request upstream, or opens a tunnel, even when the request
// step fails, so a refusal closes the connection instead.
func refuseConnection(client clientHijacker, err error) error {
	conn, _, hijackErr := client.Hijack()
	if hijackErr != nil {
		return errors.Join(err, fmt.Errorf("hijacking client connection: %w", hijackErr))
	}
	return errors.Join(err, conn.Close())
}

// projectAdmission is how a request was admitted: whether by the proxy's own
// client, and in which generation of the open project.
type projectAdmission struct {
	internal   bool
	generation uint64
}

// passStaleResponse passes a response whose pair belongs to a project that is
// no longer open straight to the client. A WebSocket upgrade cannot be relayed
// without recording it, so it is refused instead.
func (proxy *Proxy) passStaleResponse(res *http.Response) error {
	if !marasiws.IsUpgradeResponse(res) {
		return nil
	}
	if res.Body != nil {
		res.Body.Close()
	}
	body := "the open project changed while the WebSocket handshake was in flight\n"
	*res = http.Response{
		Status:        http.StatusText(http.StatusBadGateway),
		StatusCode:    http.StatusBadGateway,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
		Request:       res.Request,
	}
	return nil
}

// WithDefaultModifierPipeline adds the default request and response modifiers.
// They run in the order they are added below. The base pipeline has already
// buffered the response body.
func WithDefaultModifierPipeline() func(*Proxy) error {
	return func(proxy *Proxy) error {
		// Request Modifiers
		proxy.AddRequestPairModifier(MarasiPagesModifier)
		proxy.AddRequestPairModifier(PreventLoopModifier)
		proxy.AddRequestPairModifier(CompassRequestModifier)
		proxy.AddRequestPairModifier(SetupRequestModifier)
		proxy.AddRequestPairModifier(OverrideWaypointsModifier)
		proxy.AddRequestPairModifier(ExtensionsRequestModifier)
		proxy.AddRequestPairModifier(CheckpointRequestModifier)
		proxy.AddRequestPairModifier(WebSocketOffersModifier)
		proxy.AddRequestPairModifier(WriteRequestModifier)

		// Response Modifiers
		proxy.AddResponsePairModifier(ResponseFilterModifier)
		proxy.AddResponsePairModifier(WebSocketPrepareModifier)
		proxy.AddResponsePairModifier(CompressedResponseModifier)
		proxy.AddResponsePairModifier(CompassResponseModifier)
		proxy.AddResponsePairModifier(ExtensionsResponseModifier)
		proxy.AddResponsePairModifier(CheckpointResponseModifier)
		proxy.AddResponsePairModifier(WebSocketNegotiationModifier)
		proxy.AddResponsePairModifier(WriteResponseModifier)
		proxy.SetUpgradeHandler(WebSocketUpgradeHandler)
		return nil
	}

}

// WithLogger sets the structured logger for the proxy.
// It performs a nil check to ensure the proxy always has a valid logger.
func WithLogger(logger *slog.Logger) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if logger != nil {
			proxy.Logger = logger
		}
		return nil
	}
}
