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
func registerMarasiLibrary(extension *Runtime, proxy ProxyService) {
	l := extension.LuaState
	funcs := []APIFunction{
		{Name: "log", Signature: "(message: string, level?: string)", Summary: "Writes a message to the proxy's log. level is \"INFO\" (the default), \"WARN\", or \"ERROR\".", Function: func(l *lua.State) int {
			message := lua.CheckString(l, 2)
			level := lua.OptString(l, 3, "INFO")
			if extID := GetExtensionID(l); extID != uuid.Nil {
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
		{Name: "config", Signature: "() -> string", Summary: "Returns the path to the proxy's configuration directory.", Function: func(l *lua.State) int {
			config, err := proxy.GetConfigDir()
			if err != nil {
				l.PushString("")
				return 1
			}
			l.PushString(config)
			return 1
		}},
		{Name: "scope", Signature: "() -> scope", Summary: "Returns the proxy's current scope.", Function: func(l *lua.State) int {
			scope, err := proxy.GetScope()
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("getting scope : %s", err.Error()))
				return 0
			}
			l.PushUserData(scope)
			lua.SetMetaTableNamed(l, "scope")
			return 1
		}},
		{Name: "builder", Signature: "(request?: req) -> RequestBuilder", Summary: "Creates a new request builder.", Function: func(l *lua.State) int {
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

	newLibrary(extension, "marasi", funcs)
	l.SetGlobal("marasi")

	registerSettingsLibrary(extension, proxy)
	registerEncodingLibrary(extension)
	registerCryptoLibrary(extension)
	registerUtilsLibrary(extension)
	registerStringsLibrary(extension)
	registerRandomLibrary(extension)
	registerRepoLibrary(extension, proxy)
}
