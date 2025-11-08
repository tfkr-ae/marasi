package marasi

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"runtime"
	"time"

	"github.com/google/martian/mitm"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

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

// WithExtensions will get all the extensions from the project and load each extension if enabled
func WithExtension(extension *Extension, options ...func(*Extension) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		// Check if the map is nil and create if it is
		if proxy.Extensions == nil {
			proxy.Extensions = make([]*Extension, 0)
		}
		// Check if the extension doesn't exist
		if _, ok := proxy.GetExtension(extension.Name); !ok {
			err := extension.PrepareState(proxy, options)
			if err != nil {
				return fmt.Errorf("preparing extension %s : %w", extension.Name, err)
			}
			proxy.Extensions = append(proxy.Extensions, extension)
		}
		return nil
	}
}
func WithExtensions(extensions []*Extension, options ...func(*Extension) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.Extensions == nil {
			proxy.Extensions = make([]*Extension, 0)
		}
		// extensions, err := proxy.Repo.GetExtensions()
		// if err != nil {
		// 	return fmt.Errorf("getting all extensions : %w", err)
		// }
		for _, extension := range extensions {
			if _, ok := proxy.GetExtension(extension.Name); !ok {
				// Extension does not exist
				// if it is enabled add it, if not keep it disabled
				extension.PrepareState(proxy, options)
				proxy.Extensions = append(proxy.Extensions, extension)
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
func WithResponseHandler(handler func(res ProxyResponse) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnResponse != nil {
			return errors.New("proxy already has a response handler defined")
		}
		proxy.OnResponse = handler
		return nil
	}
}

// WithRequestHandler takes a handler function that will be executed on each Request
func WithRequestHandler(handler func(req ProxyRequest) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.OnRequest != nil {
			return errors.New("proxy already has a request handler defined")
		}
		proxy.OnRequest = handler
		return nil
	}
}

// WithLogHandler takes a handler function that will be executed on each Log
func WithLogHandler(handler func(log Log) error) func(*Proxy) error {
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
		var priv interface{}
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
		err = proxy.Repo.UpdateSPKI(proxy.SPKIHash)
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

// WithRepo will take the ProxyRepository interface, set the index value and load the extensions
func WithRepo(repo Repository) func(*Proxy) error {
	return func(proxy *Proxy) error {
		// First we need to check if there is a repo
		if proxy.Repo != nil {
			if err := proxy.Repo.Close(); err != nil {
				return err
			}
			proxy.Repo = nil
		}
		proxy.Repo = repo
		err := proxy.SyncWaypoints()
		if err != nil {
			proxy.WriteLog("INFO", err.Error())
		}
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
					if session, ok := SessionFromContext(res.Request.Context()); ok {
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

// LOG OPTIONS
func LogWithContext(context Metadata) func(log *Log) error {
	return func(log *Log) error {
		log.Context = context
		return nil
	}
}

// LOG OPTIONS
func LogWithReqResID(id uuid.UUID) func(log *Log) error {
	return func(log *Log) error {
		log.RequestID = sql.NullString{Valid: true, String: id.String()}
		return nil
	}
}
func LogWithExtensionID(id uuid.UUID) func(log *Log) error {
	return func(log *Log) error {
		log.ExtensionID = sql.NullString{Valid: true, String: id.String()}
		return nil
	}
}
