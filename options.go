package marasi

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"runtime"
	"time"

	"github.com/google/martian/mitm"
	"github.com/spf13/viper"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
)

// RepositoryProvider defines the interface for a provider of all data repositories
// used by the proxy. This allows for easy injection of a database implementation.
type RepositoryProvider interface {
	domain.TrafficRepository
	domain.ExtensionRepository
	domain.LaunchpadRepository
	domain.WaypointRepository
	domain.StatsRepository
	domain.ConfigRepository
	domain.LogRepository
	io.Closer
}

var (
	// ErrSessionContext is returned when the session could not be fetch from context
	ErrSessionContext = errors.New("failed to get session from context")
)

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
			return fmt.Errorf("applying option on marasi : %w", err)
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
				log.Println("[*] creating config dir")
				err := os.MkdirAll(appConfigDir, 0700)
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
		viperInstance.SetDefault("chrome_dirs", []ChromePathConfig{})
		err = viperInstance.ReadInConfig()
		if err != nil {
			// need to check if the error is config file doesn't exist
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				// Config file is not found
				err = viperInstance.SafeWriteConfig()
				if err != nil {
					return fmt.Errorf("writing config file : %w", err)
				}
			} else {
				return fmt.Errorf("reading config file : %w", err)
			}
		}
		if err := viperInstance.Unmarshal(&proxy.Config); err != nil {
			return fmt.Errorf("unmarshalling config to struct : %w", err)
		}
		proxy.Config.viper = viperInstance

		proxy.Config.DesktopOS = runtime.GOOS
		log.Print(proxy.Config.ChromeDirs)
		// Rewrite entire file from struct
		err = viperInstance.WriteConfig()
		if err != nil {
			return fmt.Errorf("writing config after unmarshalling : %w", err)
		}
		return nil
	}
}

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
				return fmt.Errorf("preparing extension %s : %w", extension.Name, err)
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
		if proxy.Extensions == nil {
			proxy.Extensions = make([]*extensions.Runtime, 0)
		}
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

// WithInterceptHandler takes a handler function that will be executed on each intercept (Request / Response)
func WithInterceptHandler(handler func(intercepted *Intercepted) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnIntercept != nil {
			return errors.New("proxy already has an intercept handler defined")
		}
		proxy.OnIntercept = handler
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

// WithTLS will configure the proxy CA based on the proxy.ConfigDir
// It will also configure the http.Client that is used for the launchpad requests
// TODO - Check if the certificate expired
func WithTLS() func(*Proxy) error {
	return func(proxy *Proxy) error {
		var x509c *x509.Certificate
		var priv any
		var err error
		certPath := path.Join(proxy.ConfigDir, certFile)
		if _, err = os.Stat(certPath); os.IsNotExist(err) {
			log.Println("[*] Certificate does not exist, creating a new one ")
			// Certificate and key do not exist, create new ones
			x509c, priv, err = mitm.NewAuthority("Marasi", "Marasi Authority", 365*3*24*time.Hour)
			if err != nil {
				return fmt.Errorf("creating new mitm authority : %w", err)
			}

			// Save certificate and private key to disk
			if err := saveCertAndKey(x509c, priv, proxy.ConfigDir); err != nil {
				return fmt.Errorf("saving cert and key to disk: %w", err)
			}
		} else {
			log.Println("[*] Loading existing cert")
			// Load existing certificate and key from disk
			x509c, priv, err = loadCertAndKey(proxy.ConfigDir)
			if err != nil {
				return fmt.Errorf("loading cert and key from disk: %w", err)
			}
		}

		proxy.SPKIHash = getSPKIHash(x509c)
		proxy.Cert = x509c
		err = proxy.ConfigRepo.UpdateSPKI(proxy.SPKIHash)
		if err != nil {
			return fmt.Errorf("setting spki hash %s : %w", proxy.SPKIHash, err)
		}
		tlsc, err := mitm.NewConfig(x509c, priv)
		if err != nil {
			return fmt.Errorf("creating new mitm config : %w", err)
		}
		proxy.martianProxy.SetMITM(tlsc)
		proxy.mitmConfig = tlsc.TLS()

		// Add system certificates + marasi cert
		systemPool, err := x509.SystemCertPool()
		if err != nil {
			return fmt.Errorf("fetching system cert pool : %w", err)
		}
		systemPool.AddCert(x509c)
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
			WithWaypointRepository(repo),
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

// WithBasePipeline will setup the base modifier pipeline for marasi
// It will define the main Request & Response modifiers that will execute the
// attached modifiers and hande `ErrDropped` and `ErrSkipPipeline`.
// If a response is dropped the `martian.Session` is read from the context and hijacked to
// close the `conn`
func WithBasePipeline() func(*Proxy) error {
	return func(proxy *Proxy) error {
		proxy.martianProxy.SetRequestModifier(
			martianReqModifierFunc(func(req *http.Request) error {
				err := proxy.Modifiers.ModifyRequest(req)
				if err == nil || errors.Is(err, ErrDropped) || errors.Is(err, ErrSkipPipeline) {
					return nil
				}
				// TODO this should be handled through logging
				log.Printf("request pipeline: %v", err)
				return err
			}),
		)
		proxy.martianProxy.SetResponseModifier(
			martianResModifierFunc(func(res *http.Response) error {
				err := proxy.Modifiers.ModifyResponse(res)
				if err == nil || errors.Is(err, ErrSkipPipeline) {
					return nil
				}
				if errors.Is(err, ErrDropped) {
					if session, ok := core.SessionFromContext(res.Request.Context()); ok {
						conn, _, err := session.Hijack()
						if err != nil {
							return fmt.Errorf("hijacking session : %w", err)
						}
						err = conn.Close()
						if err != nil {
							return fmt.Errorf("closing connection : %w", err)
						}
					} else {
						return ErrSessionContext
					}
				}
				// TODO this should be handled through logging
				log.Printf("response pipeline: %v", err)
				return err
			}),
		)
		return nil
	}
}

// WithDefaultPipeline will apply the default modifier pipelines
// The default processing order is: waypoint overrides → extensions → interception → database storage.
// WithDefaultModifierPipeline will apply the default modifier pipelines for Requests & Responses.
// The processing order is:
// (Request): Compass -> Waypoint -> Extensions -> Checkpoint -> Database Write
// (Response): Buffer Streaming -> Decompress -> Compass -> Extensions -> Checkpoint -> Database Write
func WithDefaultModifierPipeline() func(*Proxy) error {
	return func(proxy *Proxy) error {
		// Request Modifiers
		proxy.AddRequestModifier(PreventLoopModifier)
		proxy.AddRequestModifier(SkipConnectRequestModifier)
		proxy.AddRequestModifier(CompassRequestModifier)
		proxy.AddRequestModifier(SetupRequestModifier)
		proxy.AddRequestModifier(OverrideWaypointsModifier)
		proxy.AddRequestModifier(ExtensionsRequestModifier)
		proxy.AddRequestModifier(CheckpointRequestModifier)
		proxy.AddRequestModifier(WriteRequestModifier)

		// Response Modifiers
		proxy.AddResponseModifier(ResponseFilterModifier)
		proxy.AddResponseModifier(BufferStreamingBodyModifier)
		proxy.AddResponseModifier(CompressedResponseModifier)
		proxy.AddResponseModifier(CompassResponseModifier)
		proxy.AddResponseModifier(ExtensionsResponseModifier)
		proxy.AddResponseModifier(CheckpointResponseModifier)
		proxy.AddResponseModifier(WriteResponseModifier)
		return nil
	}

}
