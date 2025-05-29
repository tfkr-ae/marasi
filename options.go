package marasi

import (
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"runtime"
	"time"

	"github.com/google/martian/mitm"
	"github.com/google/martian/parse"
	"github.com/google/uuid"
	"github.com/spf13/viper"
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
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(appConfigDir)
		viper.SetDefault("first_run", true)
		viper.SetDefault("vim_enabled", true)
		viper.SetDefault("default_address", "127.0.0.1")
		viper.SetDefault("default_port", "8080")
		err = viper.ReadInConfig()
		if err != nil {
			// need to check if the error is config file doesn't exist
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				// Config file is not found
				err = viper.SafeWriteConfig()
				if err != nil {
					return fmt.Errorf("writing config file : %w", err)
				}
			} else {
				return fmt.Errorf("reading config file : %w", err)
			}
		}
		if err := viper.Unmarshal(&proxy.Config); err != nil {
			return fmt.Errorf("unmarshalling config to struct : %w", err)
		}

		proxy.Config.DesktopOS = runtime.GOOS
		// Rewrite entire file from struct
		err = viper.WriteConfig()
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
			proxy.Extensions = make(map[string]*Extension)
		}
		// Check if the extension doesn't exist
		if _, ok := proxy.Extensions[extension.Name]; !ok {
			err := extension.PrepareState(proxy, options)
			if err != nil {
				return fmt.Errorf("preparing extension %s : %w", extension.Name, err)
			}
			proxy.Extensions[extension.Name] = extension
		}
		return nil
	}
}
func WithExtensions(extensions []*Extension, options ...func(*Extension) error) func(*Proxy) error {
	return func(proxy *Proxy) error {
		// TODO LOOK INTO THIS
		proxy.Extensions = make(map[string]*Extension)
		// extensions, err := proxy.Repo.GetExtensions()
		// if err != nil {
		// 	return fmt.Errorf("getting all extensions : %w", err)
		// }
		for _, extension := range extensions {
			if _, ok := proxy.Extensions[extension.Name]; !ok {
				// Extension does not exist
				// if it is enabled add it, if not keep it disabled
				extension.PrepareState(proxy, options)
				proxy.Extensions[extension.Name] = extension
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

// WithLogModifer sets both the request and response modifiers for the proxy
func WithLogModifer() func(*Proxy) error {
	return func(proxy *Proxy) error {
		if proxy.martianProxy == nil {
			return errors.New("proxy has no martianProxy")
		}
		proxy.martianProxy.SetRequestModifier(proxy)
		proxy.martianProxy.SetResponseModifier(proxy)
		parse.Register("logModifier", func(b []byte) (*parse.Result, error) {
			return parse.NewResult(new(any), []parse.ModifierType{parse.Request, parse.Response})
		})
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

		log.Print(getSPKIHash(x509c))
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
		tlsConfig := tlsc.TLS()

		// Add system certificates + marasi cert
		systemPool, err := x509.SystemCertPool()
		if err != nil {
			return fmt.Errorf("fetching system cert pool : %w", err)
		}
		tlsConfig.RootCAs = systemPool
		tlsConfig.RootCAs.AddCert(x509c)
		proxy.TLSConfig = tlsConfig
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
