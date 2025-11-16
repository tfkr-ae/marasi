package extensions

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/core"
)

// RequestBuilder provides a fluent interface for constructing and sending HTTP requests
// from within a Lua environment. It allows for method, URL, body, headers, and cookies
// to be set before sending the request.
type RequestBuilder struct {
	client      *http.Client      // client is the HTTP client used to send the request.
	method      string            // method is the HTTP method (e.g., "GET", "POST").
	url         string            // url is the request URL.
	body        string            // body is the request body.
	headers     http.Header       // headers are the HTTP headers for the request.
	cookies     map[string]string // cookies are the cookies to be sent with the request.
	contentType string            // contentType is the value of the "Content-Type" header.
}

// NewRequestBuilder creates and returns a new RequestBuilder instance.
// It is initialized with an HTTP client that will be used to send the request.
func NewRequestBuilder(client *http.Client) *RequestBuilder {
	return &RequestBuilder{
		client:  client,
		headers: make(http.Header),
		cookies: make(map[string]string),
	}
}

// RegisterType creates a new metatable in the Lua state and associates it with a name.
// It registers a set of functions as methods for the type and a `__tostring` metamethod.
// This is a generic helper for exposing Go types to Lua.
func RegisterType(l *lua.State, name string, functions map[string]lua.Function, toString func(l *lua.State) int) {
	lua.NewMetaTable(l, name)
	l.PushGoFunction(FunctionIndex(functions))
	l.SetField(-2, "__index")
	l.PushGoFunction(toString)
	l.SetField(-2, "__tostring")
	l.Pop(1)
}

// FunctionIndex returns a Lua function that acts as an `__index` metamethod.
// It looks up a field name in the provided functions map and pushes the corresponding
// function onto the stack if found.
func FunctionIndex(functions map[string]lua.Function) func(l *lua.State) int {
	return func(l *lua.State) int {
		field := lua.CheckString(l, 2)
		if function, ok := functions[field]; ok {
			l.PushGoFunction(function)
		} else {
			l.PushNil()
		}
		return 1
	}
}

// RegisterScopeType registers the `compass.Scope` type and its methods with the Lua state.
// This allows Lua scripts to interact with the proxy's scope, adding, removing, and checking rules.
func RegisterScopeType(extension *LuaExtension) {
	funcs := map[string]lua.Function{
		"add_rule": func(l *lua.State) int {
			scope, ok := l.ToUserData(1).(*compass.Scope)
			if !ok {
				l.PushString("Invalid scope")
				return 1
			}

			ruleStr, _ := l.ToString(2)
			matchType, _ := l.ToString(3)
			isExclude := strings.HasPrefix(ruleStr, "-")

			err := scope.AddRule(ruleStr, matchType, isExclude)
			if err != nil {
				l.PushString(fmt.Sprintf("Error adding rule: %v", err))
				return 1
			}

			l.PushBoolean(true)
			return 1
		},
		"remove_rule": func(l *lua.State) int {
			scope, ok := l.ToUserData(1).(*compass.Scope)
			if !ok {
				l.PushString("Invalid scope")
				return 1
			}

			ruleStr, _ := l.ToString(2)
			matchType, _ := l.ToString(3)
			isExclude := strings.HasPrefix(ruleStr, "-")

			err := scope.RemoveRule(ruleStr, matchType, isExclude)
			if err != nil {
				l.PushString(fmt.Sprintf("Error removing rule: %v", err))
				return 1
			}

			l.PushBoolean(true)
			return 1
		},
		"matches": func(l *lua.State) int {
			scope, ok := l.ToUserData(1).(*compass.Scope)
			if !ok {
				l.PushString("Invalid scope")
				return 1
			}

			// Get the input from Lua (should be a request or response)
			input := l.ToUserData(2)
			if input == nil {
				l.PushString("Invalid input; expected request or response")
				return 1
			}

			// Call the Matches function
			result := scope.Matches(input)
			l.PushBoolean(result)
			return 1
		},
		"set_default_allow": func(l *lua.State) int {
			scope, ok := l.ToUserData(1).(*compass.Scope)
			if !ok {
				l.PushString("Invalid scope")
				return 1
			}

			allow := l.ToBoolean(2)
			scope.DefaultAllow = allow
			l.PushBoolean(true) // Indicate success
			return 1
		},
		"matches_string": func(l *lua.State) int {
			scope, ok := l.ToUserData(1).(*compass.Scope)
			if !ok {
				l.PushString("Invalid scope")
				return 1
			}
			input, ok := l.ToString(2)
			if !ok {
				l.PushString("Invalid input")
				return 1
			}
			matchType, ok := l.ToString(3)
			if !ok {
				l.PushString("Invalid match type")
				return 1
			}
			result := scope.MatchesString(input, matchType)
			l.PushBoolean(result)
			return 1
		},
		"clear_rules": func(l *lua.State) int {
			scope, ok := l.ToUserData(1).(*compass.Scope)
			if !ok {
				l.PushString("Invalid scope")
				return 1
			}
			scope.ClearRules()
			l.PushBoolean(true)
			return 1
		},
	}

	RegisterType(extension.LuaState, "scope", funcs, func(l *lua.State) int {
		scope, ok := l.ToUserData(1).(*compass.Scope)
		if !ok {
			l.PushString("Invalid Scope")
			return 1
		}
		result := fmt.Sprintf(
			"Scope { IncludeRules: %d, ExcludeRules: %d, DefaultAllow: %t }",
			len(scope.IncludeRules),
			len(scope.ExcludeRules),
			scope.DefaultAllow,
		)
		l.PushString(result)
		return 1
	})
}

// RegisterRegexType registers the `regexp.Regexp` type and its methods with the Lua state.
// This allows Lua scripts to perform regular expression matching, searching, and replacement.
func RegisterRegexType(extension *LuaExtension) {
	funcs := make(map[string]lua.Function)

	// Function to match a string
	funcs["match"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				matched := re.MatchString(text)
				util.DeepPush(l, matched)
				return 1
			}
		}
		return 0
	}
	funcs["is_anchored_match"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				isAnchored := re.MatchString("^" + text + "$")
				util.DeepPush(l, isAnchored)
				return 1
			}
		}
		l.PushNil()
		return 1
	}
	funcs["find_submatch_indices"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				indices := re.FindStringSubmatchIndex(text)
				util.DeepPush(l, indices)
				return 1
			}
		}
		l.PushNil()
		return 1
	}
	funcs["find_named_submatch"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				submatches := re.FindStringSubmatch(text)
				result := make(map[string]string)
				names := re.SubexpNames()
				for i, name := range names {
					if i > 0 && name != "" {
						result[name] = submatches[i]
					}
				}
				util.DeepPush(l, result)
				return 1
			}
		}
		l.PushNil()
		return 1
	}
	// Function to find all matches
	funcs["find_all"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				matches := re.FindAllString(text, -1)
				util.DeepPush(l, matches)
				return 1
			}
		}
		return 0
	}

	// Function to replace matches with a string
	funcs["replace"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			replacement, replaceOk := l.ToString(3)
			if textOk && replaceOk {
				result := re.ReplaceAllString(text, replacement)
				util.DeepPush(l, result)
				return 1
			}
		}
		return 0
	}

	// Function to split a string
	funcs["split"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				parts := re.Split(text, -1)
				util.DeepPush(l, parts)
				return 1
			}
		}
		return 0
	}

	// Function to get the string representation of the regex
	funcs["pattern"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			util.DeepPush(l, re.String())
			return 1
		}
		return 0
	}

	// Function to find the first match
	funcs["find"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				match := re.FindString(text)
				util.DeepPush(l, match)
				return 1
			}
		}
		return 0
	}

	// Function to find the first match and its submatches (groups)
	funcs["find_submatch"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				submatches := re.FindStringSubmatch(text)
				util.DeepPush(l, submatches)
				return 1
			}
		}
		return 0
	}

	// Function to find all matches and their submatches (groups)
	funcs["find_all_submatches"] = func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			text, textOk := l.ToString(2)
			if textOk {
				submatches := re.FindAllStringSubmatch(text, -1)
				util.DeepPush(l, submatches)
				return 1
			}
		}
		return 0
	}

	// Register the type
	RegisterType(extension.LuaState, "regexp", funcs, func(l *lua.State) int {
		if re, ok := l.ToUserData(1).(*regexp.Regexp); ok {
			util.DeepPush(l, fmt.Sprintf("Regexp { Pattern: %s }", re.String()))
			return 1
		}
		util.DeepPush(l, "Invalid Regexp")
		return 1
	})
}

// RegisterCookieType registers the `http.Cookie` type and its methods with the Lua state.
// This allows Lua scripts to read and modify HTTP cookies.
func RegisterCookieType(extension *LuaExtension) {
	funcs := make(map[string]lua.Function)

	// Function to get the cookie's name
	funcs["Name"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			l.PushString(cookie.Name)
			return 1
		}
		return 0
	}

	// Function to set the cookie's name
	funcs["set_name"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			name, ok := l.ToString(2)
			if ok {
				cookie.Name = name
			}
		}
		return 0
	}

	// Function to get the cookie's value
	funcs["Value"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			l.PushString(cookie.Value)
			return 1
		}
		return 0
	}

	// Function to set the cookie's value
	funcs["set_value"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			value, ok := l.ToString(2)
			if ok {
				cookie.Value = value
			}
		}
		return 0
	}

	// Function to get the cookie's domain
	funcs["Domain"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			l.PushString(cookie.Domain)
			return 1
		}
		return 0
	}

	// Function to set the cookie's domain
	funcs["set_domain"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			domain, ok := l.ToString(2)
			if ok {
				cookie.Domain = domain
			}
		}
		return 0
	}

	// Function to get the cookie's path
	funcs["Path"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			l.PushString(cookie.Path)
			return 1
		}
		return 0
	}

	// Function to set the cookie's path
	funcs["set_path"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			path, ok := l.ToString(2)
			if ok {
				cookie.Path = path
			}
		}
		return 0
	}

	// Function to get the cookie's expiration time
	funcs["Expires"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			l.PushString(cookie.Expires.Format(time.RFC3339))
			return 1
		}
		return 0
	}

	// Function to set the cookie's expiration time
	funcs["set_expiry"] = func(l *lua.State) int {
		if cookie, ok := l.ToUserData(1).(*http.Cookie); ok {
			expiresStr, ok := l.ToString(2)
			if ok {
				if expires, err := time.Parse(time.RFC3339, expiresStr); err == nil {
					cookie.Expires = expires
				}
			}
		}
		return 0
	}

	// Register the cookie type
	RegisterType(extension.LuaState, "cookie", funcs, func(l *lua.State) int {
		// Retrieve the user data and cast it to *http.Cookie
		cookie, ok := l.ToUserData(1).(*http.Cookie)
		if !ok {
			l.PushString("Invalid Cookie")
			return 1
		}
		// Create a formatted string representation of the cookie
		result := fmt.Sprintf("Cookie { Name: %s, Value: %s, Domain: %s, Path: %s, Expires: %s }",
			cookie.Name, cookie.Value, cookie.Domain, cookie.Path, cookie.Expires.Format(time.RFC3339))

		// Push the result string onto the Lua stack
		l.PushString(result)
		return 1 // Returning 1 since we are pushing 1 value onto the Lua stack
	})
}

// RegisterHeaderType registers the `http.Header` type and its methods with the Lua state.
// This allows Lua scripts to read and modify HTTP headers.
func RegisterHeaderType(extension *LuaExtension) {
	funcs := make(map[string]lua.Function)

	// Function to get a header value
	funcs["Get"] = func(l *lua.State) int {
		if header, ok := l.ToUserData(1).(*http.Header); ok {
			key, keyOk := l.ToString(2)
			if keyOk {
				values := header.Get(key)
				if values == "" {
					l.PushNil()
					return 1
				}
				l.PushString(values)
				return 1
			}
		}
		return 0
	}

	// Function to set a header value
	funcs["Set"] = func(l *lua.State) int {
		if header, ok := l.ToUserData(1).(*http.Header); ok {
			key, keyOk := l.ToString(2)
			value, valueOk := l.ToString(3)
			if keyOk && valueOk {
				header.Set(key, value)
			}
		}
		return 0
	}

	// Function to add a header value (for headers with multiple values)
	funcs["Add"] = func(l *lua.State) int {
		if header, ok := l.ToUserData(1).(*http.Header); ok {
			key, keyOk := l.ToString(2)
			value, valueOk := l.ToString(3)
			if keyOk && valueOk {
				header.Add(key, value)
			}
		}
		return 0
	}

	// Function to delete a header
	funcs["Del"] = func(l *lua.State) int {
		if header, ok := l.ToUserData(1).(*http.Header); ok {
			key, keyOk := l.ToString(2)
			if keyOk {
				header.Del(key)
			}
		}
		return 0
	}

	// Register the type with Lua
	RegisterType(extension.LuaState, "header", funcs, func(l *lua.State) int {
		header, ok := l.ToUserData(1).(*http.Header)
		if !ok {
			l.PushString("Invalid Header")
			return 1
		}
		result := fmt.Sprintf("Header { %v }", *header)
		l.PushString(result)
		return 1
	})
}
// RegisterRequestType registers the `http.Request` type and its methods with the Lua state.
// This allows Lua scripts to read and modify incoming HTTP requests.
func RegisterRequestType(extension *LuaExtension) {
	funcs := make(map[string]lua.Function)
	funcs["ID"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			if requestId, ok := core.RequestIDFromContext(req.Context()); ok {
				l.PushString(requestId.String())
				return 1
			}
		}
		return 0
	}
	funcs["URL"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			l.PushString(req.URL.String())
			return 1
		}
		return 0
	}
	funcs["Scheme"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			l.PushString(req.URL.Scheme)
			return 1
		}
		return 0
	}
	funcs["Method"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			l.PushString(req.Method)
			return 1
		}
		return 0
	}
	funcs["Host"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			l.PushString(req.Host)
			return 1
		}
		return 0
	}
	funcs["Path"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			l.PushString(req.URL.Path)
			return 1
		}
		return 0
	}

	funcs["Body"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			bodyBytes, err := io.ReadAll(req.Body)
			if err != nil {
				return 0
			}
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			l.PushString(string(bodyBytes))
			return 1
		}
		return 0
	}

	funcs["Headers"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			l.PushUserData(&req.Header)
			lua.SetMetaTableNamed(l, "header")
			return 1
		}
		return 0
	}
	funcs["Cookie"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			if cookieName, ok := l.ToString(2); ok {
				cookie, err := req.Cookie(cookieName)
				if err != nil {
					l.PushNil()
					return 1
				}
				l.PushUserData(cookie)
				lua.SetMetaTableNamed(l, "cookie")
				return 1
			}
		}
		return 0
	}
	funcs["Metadata"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			if metadata, ok := core.MetadataFromContext(req.Context()); ok {
				util.DeepPush(l, metadata)
				return 1
			}
		}
		return 0
	}
	funcs["set_headers"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			if headersInterface, err := util.PullTable(l, 2); err == nil {
				// Check if headersInterface is a map and convert it to http.Header
				if headersMap, ok := headersInterface.(map[string]interface{}); ok {
					// Create a new http.Header
					headers := http.Header{}

					// Iterate over the map and populate the http.Header
					for key, value := range headersMap {
						if values, ok := value.([]interface{}); ok {
							// Convert []interface{} to []string
							stringValues := make([]string, len(values))
							for i, v := range values {
								if str, ok := v.(string); ok {
									stringValues[i] = str
								}
							}
							headers[key] = stringValues
						}
					}
					req.Header = headers
				}
			}
		}
		return 0
	}
	funcs["set_metadata"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			if metadata, ok := core.MetadataFromContext(req.Context()); ok {
				if extensionMetadata, err := util.PullTable(l, 2); err == nil {
					metadata[extension.Data.Name] = extensionMetadata
					*req = *core.ContextWithMetadata(req, metadata)
				} else {
					log.Print(err)
				}
			}
		}
		return 0
	}
	funcs["set_body"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			if newBody, ok := l.ToString(2); ok {
				req.Body = io.NopCloser(bytes.NewBufferString(newBody))
				req.ContentLength = int64(len(newBody))
			}
		}
		return 0
	}
	funcs["Drop"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			*req = *core.ContextWithDropFlag(req, true)
		}
		return 0
	}
	funcs["Skip"] = func(l *lua.State) int {
		if req, ok := l.ToUserData(1).(*http.Request); ok {
			*req = *core.ContextWithSkipFlag(req, true)
			l.PushBoolean(true)
			return 1
		}
		l.PushString("Invalid Request")
		return 1
	}
	RegisterType(extension.LuaState, "req", funcs, func(l *lua.State) int {
		// Retrieve the user data and cast it to *ProxyRequest
		req, ok := l.ToUserData(1).(*http.Request)
		if !ok {
			l.PushString("Invalid Request")
			return 1
		}
		result := fmt.Sprintf("Request { ID: %s, Method: %s, Host: %s, Path: %s}",
			req.Context().Value(core.RequestIDKey).(uuid.UUID).String(), req.Method, req.Host, req.URL.Path)

		// Push the result string onto the Lua stack
		l.PushString(result)
		return 1 // Returning 1 since we are pushing 1 value onto the Lua stack
	})
}

// RegisterResponseType registers the `http.Response` type and its methods with the Lua state.
// This allows Lua scripts to read and modify outgoing HTTP responses.
func RegisterResponseType(extension *LuaExtension) {
	funcs := make(map[string]lua.Function)
	funcs["ID"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			if requestId, ok := core.RequestIDFromContext(res.Request.Context()); ok {
				l.PushString(requestId.String())
				return 1
			}
		}
		return 0
	}
	funcs["URL"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			l.PushString(res.Request.URL.String())
			return 1
		}
		return 0
	}
	funcs["Status"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			l.PushString(res.Status)
			return 1
		}
		return 0
	}
	funcs["StatusCode"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			l.PushInteger(res.StatusCode)
			return 1
		}
		return 0
	}
	funcs["Length"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			l.PushInteger(int(res.ContentLength))
			return 1
		}
		return 0
	}
	funcs["Body"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			bodyBytes, err := io.ReadAll(res.Body)
			if err != nil {
				return 0
			}
			res.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			l.PushString(string(bodyBytes))
			return 1
		}
		return 0
	}
	funcs["Headers"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			l.PushUserData(&res.Header)
			lua.SetMetaTableNamed(l, "header")
			return 1
		}
		return 0
	}
	funcs["Cookie"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			if cookieName, ok := l.ToString(2); ok {
				for _, cookie := range res.Cookies() {
					if cookie.Name == cookieName {
						l.PushUserData(cookie)
						lua.SetMetaTableNamed(l, "cookie")
						return 1
					}
				}
				l.PushNil()
				return 1
			}
		}
		return 0
	}
	funcs["Metadata"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			if metadata, ok := core.MetadataFromContext(res.Request.Context()); ok {
				util.DeepPush(l, metadata)
				return 1
			}
		}
		return 0
	}
	funcs["set_headers"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			if headersInterface, err := util.PullTable(l, 2); err == nil {
				// Check if headersInterface is a map and convert it to http.Header
				if headersMap, ok := headersInterface.(map[string]interface{}); ok {
					// Create a new http.Header
					headers := http.Header{}

					// Iterate over the map and populate the http.Header
					for key, value := range headersMap {
						if values, ok := value.([]interface{}); ok {
							// Convert []interface{} to []string
							stringValues := make([]string, len(values))
							for i, v := range values {
								if str, ok := v.(string); ok {
									stringValues[i] = str
								}
							}
							headers[key] = stringValues
						}
					}
					res.Header = headers
				}
			}
		}
		return 0
	}
	funcs["set_metadata"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			if metadata, ok := core.MetadataFromContext(res.Request.Context()); ok {
				if extensionMetadata, err := util.PullTable(l, 2); err == nil {
					metadata[extension.Data.Name] = extensionMetadata
					*res.Request = *core.ContextWithMetadata(res.Request, metadata)
				} else {
					log.Print(err)
				}
			}
		}
		return 0
	}
	funcs["set_body"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			if newBody, ok := l.ToString(2); ok {
				res.Body = io.NopCloser(bytes.NewBufferString(newBody))
				res.ContentLength = int64(len(newBody))
			}
		}
		return 0
	}
	funcs["Drop"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			res.Request = core.ContextWithDropFlag(res.Request, true)
		}
		return 0
	}
	funcs["Skip"] = func(l *lua.State) int {
		if res, ok := l.ToUserData(1).(*http.Response); ok {
			res.Request = core.ContextWithSkipFlag(res.Request, true)
			l.PushBoolean(true) // Indicate success
			return 1
		}
		l.PushString("Invalid Response")
		return 1
	}
	RegisterType(extension.LuaState, "res", funcs, func(l *lua.State) int {
		// Retrieve the user data and cast it to *ProxyRequest
		res, ok := l.ToUserData(1).(*http.Response)
		if !ok {
			l.PushString("Invalid Request")
			return 1
		}
		result := fmt.Sprintf("Response { ID: %s, Status: %s, Code: %d}",
			res.Request.Context().Value(core.RequestIDKey).(uuid.UUID).String(), res.Status, res.StatusCode)

		// Push the result string onto the Lua stack
		l.PushString(result)
		return 1 // Returning 1 since we are pushing 1 value onto the Lua stack
	})

}

// RegisterRequestBuilderType registers the `RequestBuilder` type and its methods with the Lua state.
// This allows Lua scripts to construct and send new HTTP requests from within an extension.
func RegisterRequestBuilderType(extension *LuaExtension) {
	funcs := map[string]lua.Function{
		"Method": func(l *lua.State) int {
			builder, ok := l.ToUserData(1).(*RequestBuilder)
			if !ok {
				l.PushString("Error: Invalid RequestBuilder object")
				return 1
			}
			method, ok := l.ToString(2)
			if !ok || method == "" {
				l.PushString("Error: HTTP method cannot be empty")
				return 1
			}
			builder.method = strings.ToUpper(method)
			return 0
		},
		"URL": func(l *lua.State) int {
			builder, ok := l.ToUserData(1).(*RequestBuilder)
			if !ok {
				l.PushString("Error: Invalid RequestBuilder object")
				return 1
			}
			urlStr, ok := l.ToString(2)
			if !ok || urlStr == "" {
				l.PushString("Error: URL cannot be empty")
				return 1
			}
			builder.url = urlStr
			return 0
		},
		"Body": func(l *lua.State) int {
			builder, ok := l.ToUserData(1).(*RequestBuilder)
			if !ok {
				l.PushString("Error: Invalid RequestBuilder object")
				return 1
			}
			body, ok := l.ToString(2)
			if !ok {
				l.PushString("Error: Invalid body")
				return 1
			}
			builder.body = body
			return 0
		},
		"Headers": func(l *lua.State) int {
			if builder, ok := l.ToUserData(1).(*RequestBuilder); ok {
				l.PushUserData(&builder.headers)
				lua.SetMetaTableNamed(l, "header")
				return 1
			}
			return 0
		},
		"Header": func(l *lua.State) int {
			builder, ok := l.ToUserData(1).(*RequestBuilder)
			if !ok {
				l.PushString("Error: Invalid RequestBuilder object")
				return 1
			}
			name, nameOk := l.ToString(2)
			value, valueOk := l.ToString(3)
			if !nameOk || name == "" {
				l.PushString("Error: Header name cannot be empty")
				return 1
			}
			if !valueOk {
				l.PushString("Error: Invalid header value")
				return 1
			}
			builder.headers[name] = []string{value}
			if strings.ToLower(name) == "content-type" {
				builder.contentType = value
			}
			return 0
		},
		"Cookie": func(l *lua.State) int {
			builder, ok := l.ToUserData(1).(*RequestBuilder)
			if !ok {
				l.PushString("Error: Invalid RequestBuilder object")
				return 1
			}
			name, nameOk := l.ToString(2)
			value, valueOk := l.ToString(3)
			if !nameOk || name == "" {
				l.PushString("Error: Cookie name cannot be empty")
				return 1
			}
			if !valueOk {
				l.PushString("Error: Invalid cookie value")
				return 1
			}
			builder.cookies[name] = value
			return 0
		},
		"Send": func(l *lua.State) int {
			builder, ok := l.ToUserData(1).(*RequestBuilder)
			if !ok {
				l.PushString("Invalid RequestBuilder")
				return 1
			}

			// Validate method and URL
			if builder.method == "" || builder.url == "" {
				l.PushString("Error: Method and URL must be set before sending the request")
				return 1
			}

			// Debug: Print method and URL
			fmt.Println("Sending request with method:", builder.method, "URL:", builder.url)

			// Create the body based on content type
			var reqBody *bytes.Buffer
			switch builder.contentType {
			case "application/json":
				reqBody = bytes.NewBuffer([]byte(builder.body))
			case "application/x-www-form-urlencoded":
				formData := url.Values{}
				for _, pair := range strings.Split(builder.body, "&") {
					parts := strings.SplitN(pair, "=", 2)
					if len(parts) == 2 {
						formData.Add(parts[0], parts[1])
					}
				}
				reqBody = bytes.NewBufferString(formData.Encode())
			default:
				reqBody = bytes.NewBuffer([]byte(builder.body))
			}

			// Debug: Print body content
			fmt.Println("Request body:", reqBody.String())

			// Create the HTTP request
			req, err := http.NewRequest(builder.method, builder.url, reqBody)
			if err != nil {
				l.PushString(fmt.Sprintf("Error creating request: %v", err))
				return 1
			}

			req.Header = builder.headers
			// // Set headers
			// for name, value := range builder.headers {
			// 	req.Header.Add(name, value)
			// }
			// Debug: Print headers
			fmt.Println("Headers:", builder.headers)

			// Set cookies
			for name, value := range builder.cookies {
				req.AddCookie(&http.Cookie{Name: name, Value: value})
			}
			// Debug: Print cookies
			fmt.Println("Cookies:", builder.cookies)

			// Debug: Indicate that the request is about to be sent
			fmt.Printf("Request : { %v }", *req)
			fmt.Println("Sending request...")

			// Send the request using the builder's client
			req.Header.Set("x-extension-id", extension.Data.ID.String())
			resp, err := builder.client.Do(req)
			if err != nil {
				l.PushString(fmt.Sprintf("Error sending request: %v", err))
				return 1
			}
			defer resp.Body.Close()

			// Debug: Indicate that a response was received
			fmt.Println("Response received")

			// Read the response body
			responseBody, err := io.ReadAll(resp.Body)
			if err != nil {
				l.PushString(fmt.Sprintf("Error reading response: %v", err))
				return 1
			}

			// Debug: Print response status
			fmt.Println("Response status:", resp.Status)

			// Push the response body onto the Lua stack
			log.Print(string(responseBody))
			l.PushString(string(responseBody))
			return 1 // Returning the response body
		},
	}

	// Register the RequestBuilder type and its methods in Lua
	RegisterType(extension.LuaState, "RequestBuilder", funcs, func(l *lua.State) int {
		requestBuilder, ok := l.ToUserData(1).(*RequestBuilder)
		if !ok {
			l.PushString(("Invalid Request Builder"))
		}
		result := fmt.Sprintf("Request Builder { %v }", *requestBuilder)
		l.PushString(result)
		return 1
	})
}

// MarasiLibrary returns a list of Lua registry functions that form the `marasi` global library.
// This library provides core functionalities like scope access, request building, and utility functions.
func MarasiLibrary(l *lua.State, proxy ProxyService, extensionID uuid.UUID) []lua.RegistryFunction {
	return []lua.RegistryFunction{
		{Name: "config", Function: func(l *lua.State) int {
			config, err := proxy.GetConfigDir()
			if err != nil {
				l.PushString(config)
				return 1
			}
			return 0
		}},
		{Name: "scope", Function: func(l *lua.State) int {
			scope, err := proxy.GetScope()
			if err == nil {
				l.PushUserData(scope)
				lua.SetMetaTableNamed(l, "scope")
				return 1
			}
			return 0
		}},
		{Name: "request_builder", Function: func(l *lua.State) int {
			// Check number of arguments (including the implicit self)
			nargs := l.Top()

			// Create a new builder
			client, err := proxy.GetClient()
			if err == nil {
				builder := NewRequestBuilder(client)

				// If a request was provided, populate the builder with its values
				if nargs >= 2 {
					// Get the request parameter
					if l.IsUserData(2) {
						if req, ok := l.ToUserData(2).(*http.Request); ok {
							// Populate builder with request values
							builder.method = req.Method
							builder.url = req.URL.String()

							// Copy headers
							for name, values := range req.Header {
								builder.headers[name] = values
								if strings.ToLower(name) == "content-type" {
									builder.contentType = values[0]
								}
							}

							// Copy cookies
							for _, cookie := range req.Cookies() {
								builder.cookies[cookie.Name] = cookie.Value
							}

							// Copy body if present
							if req.Body != nil {
								// Read the body - note this will consume the body
								bodyBytes, err := io.ReadAll(req.Body)
								if err == nil {
									builder.body = string(bodyBytes)
									// Reset the body for future use
									req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
								}
							}
						} else {
							// If it's a different user data type, it might be your custom request structure
							// Adjust as needed based on your actual request structure
							l.PushString("Error: Provided argument is not a valid request")
							return 1
						}
					} else {
						l.PushString("Error: Expected request object")
						return 1
					}
				}

				// Return the builder
				l.PushUserData(builder)
				lua.SetMetaTableNamed(l, "RequestBuilder")
				return 1

			}
			return 0
		}},
		{Name: "sha256", Function: func(l *lua.State) int {
			message := lua.CheckString(l, 2)
			h := sha256.New()
			h.Write([]byte(message))
			l.PushString(string(h.Sum(nil)))
			return 1
		}},
		{Name: "compile", Function: func(l *lua.State) int {
			// Compile a regex pattern
			pattern, ok := l.ToString(2)
			if !ok {
				l.PushNil()
				l.PushString("Expected regex pattern")
				return 2
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				l.PushNil()
				l.PushString(err.Error())
				return 2
			}
			l.PushUserData(re)
			lua.SetMetaTableNamed(l, "regexp")
			return 1
		}},
		{Name: "quoteMeta", Function: func(l *lua.State) int {
			// Escape special regex characters in a string
			text, ok := l.ToString(2)
			if ok {
				l.PushString(regexp.QuoteMeta(text))
				return 1
			}
			l.PushNil()
			l.PushString("Expected text")
			return 2
		}},
		{Name: "match", Function: func(l *lua.State) int {
			// Match a pattern directly against a string
			pattern, patternOk := l.ToString(2)
			text, textOk := l.ToString(3)
			if patternOk && textOk {
				re, err := regexp.Compile(pattern)
				if err != nil {
					l.PushNil()
					l.PushString(fmt.Sprintf("Error compiling regex: %s", err.Error()))
					return 2
				}
				matched := re.MatchString(text)
				l.PushBoolean(matched)
				return 1
			}
			l.PushNil()
			l.PushString("Invalid arguments: expected pattern and text")
			return 2
		}},
	}
}

// ExtensionLibrary returns a list of Lua registry functions for the `extension` global library.
// This library provides functions specific to the currently running extension, such as logging
// and managing settings.
func ExtensionLibrary(l *lua.State, proxy ProxyService, extensionID uuid.UUID) []lua.RegistryFunction {
	return []lua.RegistryFunction{
		{Name: "log", Function: func(l *lua.State) int {
			if message, ok := l.ToString(2); ok {
				if level, ok := l.ToString(3); ok {
					err := proxy.WriteLog(level, message, core.LogWithExtensionID(extensionID))
					if err != nil {
						l.PushString(err.Error())
						return 1
					}
					l.PushString("log-written")
					return 1
				}
			}
			l.PushString("invalid-argument")
			return 1
		}},
		{Name: "settings", Function: func(l *lua.State) int {
			if repo, err := proxy.GetExtensionRepo(); err != nil {
				settings, err := repo.GetExtensionSettingsByUUID(extensionID)
				if err != nil {
					log.Print(err)
					util.DeepPush(l, make(map[string]any))
					return 0
				}
				util.DeepPush(l, settings)
				return 1
			}
			return 0
		}},
		{Name: "set_settings", Function: func(l *lua.State) int {
			// TODO: look into this
			if luaTable, err := util.PullTable(l, 2); err == nil {
				if metadata, ok := luaTable.(map[string]any); ok {
					if repo, err := proxy.GetExtensionRepo(); err != nil {
						// Call SetExtensionSettings with the metadata map
						err := repo.SetExtensionSettingsByUUID(extensionID, metadata)
						if err != nil {
							log.Print(err)
							return 0
						}
						return 0
					}
					return 0
				} else {
					log.Print("Conversion failed, expected map[string]any")
					return 0
				}
			} else {
				log.Print(err)
				return 0
			}
		}},
	}
}

// RegisterCustomPrint overrides the default Lua `print` function.
// The new function captures the output and sends it to the extension's log,
// making it visible in the Marasi UI.
func RegisterCustomPrint(extension *LuaExtension) {
	printFunc := func(l *lua.State) int {
		n := l.Top()
		parts := make([]string, 0, n)
		for i := 1; i <= n; i++ {
			if l.IsString(i) {
				parts = append(parts, lua.CheckString(l, i))
			} else if l.IsUserData(i) {
				if str, ok := lua.ToStringMeta(l, i); ok {
					parts = append(parts, str)
				} else {
					// Handle the case when ToStringMeta fails
					// Fall back to type name and pointer for values that can't be converted
					parts = append(parts, "Hello")
				}
			} else {
				parts = append(parts, "Not hello")
			}
		}
		message := strings.Join(parts, "\t")
		logEntry := ExtensionLog{Time: time.Now(), Text: message}
		extension.Logs = append(extension.Logs, logEntry)
		err := extension.OnLog(logEntry)
		if err != nil {
			log.Print(err)
		}
		return 0
	}
	extension.LuaState.Register("print", printFunc)
}
