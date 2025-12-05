package extensions

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Shopify/go-lua"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
)

// registerMarasiLibrary registers the `marasi` global library and its sub-libraries
// into the Lua state. This is the main entry point for exposing the proxy's
// functionality to Lua scripts.
func registerMarasiLibrary(l *lua.State, proxy ProxyService) {
	funcs := []lua.RegistryFunction{
		// log writes a message to the proxy's log.
		//
		// @param message string The message to log.
		// @param level string (optional) The log level (e.g., "INFO", "WARN", "ERROR").
		// Defaults to "INFO".
		{Name: "log", Function: func(l *lua.State) int {
			message := lua.CheckString(l, 2)
			level := lua.OptString(l, 3, "INFO")
			if extID := getExtensionID(l); extID != uuid.Nil {
				err := proxy.WriteLog(level, message, core.LogWithExtensionID(extID))
				if err != nil {
					lua.Errorf(l, fmt.Sprintf("writing log : %s", err.Error()))
					return 0
				}
			} else {
				err := proxy.WriteLog(level, message)
				if err != nil {
					lua.Errorf(l, fmt.Sprintf("writing log : %s", err.Error()))
					return 0
				}
			}
			return 0
		}},
		// config returns the path to the proxy's configuration directory.
		//
		// @return string The configuration directory path.
		{Name: "config", Function: func(l *lua.State) int {
			config, err := proxy.GetConfigDir()
			if err != nil {
				l.PushString("")
				return 1
			}
			l.PushString(config)
			return 1
		}},
		// scope returns the proxy's current scope.
		//
		// @return Scope The scope object.
		{Name: "scope", Function: func(l *lua.State) int {
			scope, err := proxy.GetScope()
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("getting scope : %s", err.Error()))
				return 0
			}
			l.PushUserData(scope)
			lua.SetMetaTableNamed(l, "scope")
			return 1
		}},
		// builder creates a new request builder.
		//
		// @param request Request (optional) An existing request object to use as a template.
		// @return RequestBuilder A new request builder.
		{Name: "builder", Function: func(l *lua.State) int {
			nargs := l.Top()
			client, err := proxy.GetClient()

			if err == nil {
				builder := NewRequestBuilder(client)

				if nargs >= 2 {
					if req, ok := l.ToUserData(2).(*http.Request); ok {
						builder.method = req.Method

						if req.URL != nil {
							u := *req.URL
							builder.url = &u
						} else {
							builder.url = &url.URL{}
						}

						for name, values := range req.Header {
							builder.headers[name] = values
							if strings.ToLower(name) == "content-type" {
								builder.contentType = values[0]
							}
						}

						for _, cookie := range req.Cookies() {
							builder.cookies = append(builder.cookies, cookie)
						}

						if req.Body != nil {
							bodyBytes, err := io.ReadAll(req.Body)
							if err != nil {
								lua.Errorf(l, fmt.Sprintf("reading request body : %s", err.Error()))
								return 0
							}
							builder.body = string(bodyBytes)
							req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
						}
					} else {
						lua.ArgumentError(l, 2, "expected request object")
						return 0
					}
				}

				l.PushUserData(builder)
				lua.SetMetaTableNamed(l, "RequestBuilder")
				return 1
			}
			lua.Errorf(l, fmt.Sprintf("getting marasi client : %s", err.Error()))
			return 0
		}},
	}

	lua.NewLibrary(l, funcs)
	l.SetGlobal("marasi")

	registerSettingsLibrary(l, proxy)
	registerEncodingLibrary(l)
	registerCryptoLibrary(l)
	registerUtilsLibrary(l)
	registerStringsLibrary(l)
	registerRandomLibrary(l)
}
