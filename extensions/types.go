package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/core"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

var globalCallbackCounter uint64

// RegisterType creates a new metatable in the Lua state and associates it with a name.
// It registers methods for the type and a `__tostring` metamethod, and records
// them in the reference under summary. This is a generic helper for exposing Go types to Lua.
func RegisterType(extension *Runtime, name, summary string, methods []APIFunction, toString func(l *lua.State) int) {
	l := extension.LuaState
	functions := make(map[string]lua.Function, len(methods))
	for _, method := range methods {
		functions[method.Name] = method.Function
	}
	lua.NewMetaTable(l, name)
	l.PushGoFunction(FunctionIndex(functions))
	l.SetField(-2, "__index")
	l.PushGoFunction(toString)
	l.SetField(-2, "__tostring")
	l.Pop(1)
	extension.recordType(name, summary, methods)
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

// RequestBuilder provides a fluent interface for constructing and sending HTTP requests
// from within a Lua environment. It allows for method, URL, body, headers, and cookies
// to be set before sending the request.
type RequestBuilder struct {
	// client is the HTTP client used to send the request.
	client *http.Client
	// method is the HTTP method (e.g., "GET", "POST").
	method string
	// url is the request URL.
	url *url.URL
	// body is the request body.
	body string
	// headers are the HTTP headers for the request.
	headers http.Header
	// cookies are the cookies to be sent with the request.
	cookies []*http.Cookie
	// contentType is the value of the "Content-Type" header.
	contentType string
	metadata    map[string]any
}

// NewRequestBuilder creates and returns a new RequestBuilder instance.
// It is initialized with an HTTP client that will be used to send the request.
func NewRequestBuilder(client *http.Client) *RequestBuilder {
	return &RequestBuilder{
		client:   client,
		headers:  make(http.Header),
		cookies:  make([]*http.Cookie, 0),
		metadata: make(map[string]any),
		url:      &url.URL{},
	}
}

// RegisterScopeType registers the `compass.Scope` type and its methods with the Lua state.
// This allows Lua scripts to interact with the proxy's scope, adding, removing, and checking rules.
func RegisterScopeType(extension *Runtime) {
	funcs := []APIFunction{
		{Name: "add_rule", Signature: "(rule: string, matchType: string)", Summary: "Adds a regular expression rule. matchType is \"host\" or \"url\". A rule that starts with - excludes.", Function: func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			ruleSring := lua.CheckString(l, 2)
			matchType := lua.CheckString(l, 3)
			isExclude := strings.HasPrefix(ruleSring, "-")

			err := scope.AddRule(ruleSring, matchType, isExclude)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("adding rule : %s", err.Error()))
				return 0
			}

			return 0
		}},
		{Name: "remove_rule", Signature: "(rule: string, matchType: string)", Summary: "Removes a rule added with the same rule and matchType.", Function: func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			ruleSring := lua.CheckString(l, 2)
			matchType := lua.CheckString(l, 3)
			isExclude := strings.HasPrefix(ruleSring, "-")

			err := scope.RemoveRule(ruleSring, matchType, isExclude)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("removing rule : %s", err.Error()))
				return 0
			}
			return 0
		}},
		{Name: "matches", Signature: "(input: req|res) -> boolean", Summary: "Checks if a request or response matches the scope.", Function: func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			input := l.ToUserData(2)

			switch input.(type) {
			case *http.Request, *http.Response:
			default:
				lua.ArgumentError(l, 2, "expected request / response object")
			}

			result := scope.Matches(input)

			l.PushBoolean(result)
			return 1
		}},
		{Name: "set_default_allow", Signature: "(allow: boolean)", Summary: "Sets the default scope policy.", Function: func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			allow := l.ToBoolean(2)

			scope.SetDefaultAllow(allow)
			return 0
		}},
		{Name: "matches_string", Signature: "(input: string, matchType: string) -> boolean", Summary: "Checks if a string matches a specific rule type in the scope.", Function: func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			input := lua.CheckString(l, 2)
			matchType := lua.CheckString(l, 3)
			result := scope.MatchesString(input, matchType)

			l.PushBoolean(result)
			return 1
		}},
		{Name: "clear_rules", Signature: "()", Summary: "Removes all rules from the scope.", Function: func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			scope.ClearRules()
			return 0
		}},
	}

	RegisterType(extension, "scope", "The proxy scope, from marasi:scope(). Rules decide which traffic is in scope.", funcs, func(l *lua.State) int {
		scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
		includeRules, excludeRules, defaultAllow := scope.Snapshot()

		policy := "Block"
		if defaultAllow {
			policy = "Allow"
		}

		formatRules := func(rules map[string]compass.Rule) string {
			if len(rules) == 0 {
				return " [None]"
			}
			var parts []string
			for _, r := range rules {
				parts = append(parts, fmt.Sprintf("%s (%s)", r.Pattern.String(), r.MatchType))
			}
			slices.Sort(parts)

			return "\n    - " + strings.Join(parts, "\n    - ")
		}

		result := fmt.Sprintf(
			"Scope (Default: %s)\n  Include Rules:%s\n  Exclude Rules:%s",
			policy,
			formatRules(includeRules),
			formatRules(excludeRules),
		)

		l.PushString(result)
		return 1
	})
}

// RegisterRegexType registers the `regexp.Regexp` type and its methods with the Lua state.
// This allows Lua scripts to perform regular expression matching, searching, and replacement.
func RegisterRegexType(extension *Runtime) {
	var funcs []APIFunction

	funcs = append(funcs, APIFunction{Name: "match", Signature: "(input: string) -> boolean", Summary: "Checks if the regex matches a string.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		matched := re.MatchString(input)

		l.PushBoolean(matched)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "is_anchored_match", Signature: "(input: string) -> boolean", Summary: "Checks if the regex matches the entire string.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)

		loc := re.FindStringIndex(input)
		isAnchored := loc != nil && loc[0] == 0 && loc[1] == len(input)

		l.PushBoolean(isAnchored)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "find_submatch_indices", Signature: "(input: string) -> table|nil", Summary: "Returns the indices of the first match and its submatches.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		indices := re.FindStringSubmatchIndex(input)

		if indices == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, indices)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "find_named_submatch", Signature: "(input: string) -> table|nil", Summary: "Returns a table of named submatches.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		submatches := re.FindStringSubmatch(input)

		if submatches == nil {
			l.PushNil()
			return 1
		}

		result := make(map[string]string)
		names := re.SubexpNames()

		for i, name := range names {
			if i > 0 && name != "" {
				result[name] = submatches[i]
			}
		}

		util.DeepPush(l, result)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "find_all", Signature: "(input: string) -> table|nil", Summary: "Returns all non-overlapping matches in a string.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		matches := re.FindAllString(input, -1)

		if matches == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, matches)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "replace", Signature: "(input: string, replacement: string) -> string", Summary: "Replaces all matches in a string with a replacement string.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		replacement := lua.CheckString(l, 3)
		result := re.ReplaceAllString(input, replacement)

		l.PushString(result)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "split", Signature: "(input: string) -> table", Summary: "Splits a string by the regex.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		parts := re.Split(input, -1)
		util.DeepPush(l, parts)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "pattern", Signature: "() -> string", Summary: "Returns the regex pattern as a string.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		l.PushString(re.String())
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "find", Signature: "(input: string) -> string", Summary: "Returns the first match in a string.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		match := re.FindString(input)

		l.PushString(match)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "find_submatch", Signature: "(input: string) -> table|nil", Summary: "Returns the first match and its submatches.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		submatches := re.FindStringSubmatch(input)

		if submatches == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, submatches)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "find_all_submatches", Signature: "(input: string) -> table|nil", Summary: "Returns all matches and their submatches.", Function: func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		submatches := re.FindAllStringSubmatch(input, -1)

		if submatches == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, submatches)
		return 1
	}})

	RegisterType(extension, "regexp", "A compiled Go regular expression. No Lua function returns one yet.", funcs, func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		l.PushString(fmt.Sprintf("Regexp { Pattern: %s, Subexpressions: %d }", re.String(), re.NumSubexp()))
		return 1
	})
}

// RegisterCookieType registers the `http.Cookie` type and its methods with the Lua state.
// This allows Lua scripts to read and modify HTTP cookies.
func RegisterCookieType(extension *Runtime) {
	var funcs []APIFunction

	funcs = append(funcs, APIFunction{Name: "name", Signature: "() -> string", Summary: "Returns the cookie's name.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Name)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_name", Signature: "(name: string)", Summary: "Sets the cookie's name.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Name = lua.CheckString(l, 2)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "value", Signature: "() -> string", Summary: "Returns the cookie's value.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Value)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_value", Signature: "(value: string)", Summary: "Sets the cookie's value.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Value = lua.CheckString(l, 2)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "domain", Signature: "() -> string", Summary: "Returns the cookie's domain.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Domain)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_domain", Signature: "(domain: string)", Summary: "Sets the cookie's domain.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Domain = lua.CheckString(l, 2)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "path", Signature: "() -> string", Summary: "Returns the cookie's path.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Path)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_path", Signature: "(path: string)", Summary: "Sets the cookie's path.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Path = lua.CheckString(l, 2)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "expires", Signature: "() -> number|nil", Summary: "Returns the cookie's expiration time as a Unix timestamp.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		if cookie.Expires.IsZero() {
			l.PushNil()
			return 1
		}
		l.PushNumber(float64(cookie.Expires.Unix()))
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_expires", Signature: "(timestamp: number)", Summary: "Sets the cookie's expiration time from a Unix timestamp.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		timestamp := lua.CheckNumber(l, 2)

		cookie.Expires = time.Unix(int64(timestamp), 0)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "secure", Signature: "() -> boolean", Summary: "Returns the cookie's Secure flag.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushBoolean(cookie.Secure)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_secure", Signature: "(secure: boolean)", Summary: "Sets the cookie's Secure flag.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Secure = l.ToBoolean(2)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "http_only", Signature: "() -> boolean", Summary: "Returns the cookie's HttpOnly flag.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushBoolean(cookie.HttpOnly)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_http_only", Signature: "(httpOnly: boolean)", Summary: "Sets the cookie's HttpOnly flag.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.HttpOnly = l.ToBoolean(2)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "max_age", Signature: "() -> number", Summary: "Returns the cookie's Max-Age value.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushNumber(float64(cookie.MaxAge))
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_max_age", Signature: "(maxAge: number)", Summary: "Sets the cookie's Max-Age value.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		maxAge := lua.CheckNumber(l, 2)

		cookie.MaxAge = int(maxAge)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "same_site", Signature: "() -> string", Summary: "Returns the cookie's SameSite attribute.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)

		switch cookie.SameSite {
		case http.SameSiteLaxMode:
			l.PushString("lax")
		case http.SameSiteStrictMode:
			l.PushString("strict")
		case http.SameSiteNoneMode:
			l.PushString("none")
		default:
			l.PushString("default")
		}

		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_same_site", Signature: "(sameSite: string)", Summary: "Sets the cookie's SameSite attribute. sameSite is \"lax\", \"strict\", \"none\", or \"default\".", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		sameSite := strings.ToLower(lua.CheckString(l, 2))

		switch sameSite {
		case "lax":
			cookie.SameSite = http.SameSiteLaxMode
		case "strict":
			cookie.SameSite = http.SameSiteStrictMode
		case "none":
			cookie.SameSite = http.SameSiteNoneMode
		default:
			cookie.SameSite = http.SameSiteDefaultMode
		}
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "serialize", Signature: "() -> string", Summary: "Returns the cookie as a string.", Function: func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.String())
		return 1
	}})

	RegisterType(extension, "cookie", "An HTTP cookie, from request:cookie(name), response:cookies(), or marasi.utils:cookie(name, value).", funcs, func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.String())
		return 1
	})
}

// RegisterHeaderType registers the `http.Header` type and its methods with the Lua state.
// This allows Lua scripts to read and modify HTTP headers.
func RegisterHeaderType(extension *Runtime) {
	var funcs []APIFunction

	funcs = append(funcs, APIFunction{Name: "get", Signature: "(key: string) -> string|nil", Summary: "Returns the first value associated with the given key.", Function: func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)

		value := header.Get(key)
		if value == "" {
			l.PushNil()
			return 1
		}
		l.PushString(value)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "values", Signature: "(key: string) -> table|nil", Summary: "Returns all values associated with the given key.", Function: func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)

		values := header.Values(key)

		if values == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, values)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "to_table", Signature: "() -> table", Summary: "Returns the headers as a Lua table.", Function: func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		util.DeepPush(l, *header)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set", Signature: "(key: string, value: string)", Summary: "Sets the header entries associated with key to the single element value. It replaces any existing values associated with key.", Function: func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		value := lua.CheckString(l, 3)

		if key == "" {
			lua.ArgumentError(l, 2, "header key cannot be empty")
			return 0
		}

		header.Set(key, value)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "add", Signature: "(key: string, value: string)", Summary: "Adds the key, value pair to the header. It appends to any existing values associated with key.", Function: func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		value := lua.CheckString(l, 3)

		if key == "" {
			lua.ArgumentError(l, 2, "header key cannot be empty")
			return 0
		}

		header.Add(key, value)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "delete", Signature: "(key: string)", Summary: "Deletes the values associated with key.", Function: func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		header.Del(key)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "has", Signature: "(key: string) -> boolean", Summary: "Checks if a header with the given key exists.", Function: func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		if value := header.Get(key); value != "" {
			l.PushBoolean(true)
		} else {
			l.PushBoolean(false)
		}
		return 1
	}})

	RegisterType(extension, "header", "HTTP headers, from request:headers() or response:headers().", funcs, func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		var builder strings.Builder

		err := header.Write(&builder)
		if err != nil {
			l.PushString(fmt.Sprintf("%v", *header))
		} else {
			l.PushString(strings.TrimSpace(builder.String()))
		}
		return 1
	})
}

// RegisterURLType registers the `url.URL` type and its methods with the Lua state.
// This allows direct modification of the request URL without needing to re-set it.
func RegisterURLType(extension *Runtime) {
	var funcs []APIFunction

	funcs = append(funcs, APIFunction{Name: "string", Signature: "() -> string", Summary: "Returns the URL as a string.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.String())
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "scheme", Signature: "() -> string", Summary: "Returns the URL's scheme.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.Scheme)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_scheme", Signature: "(scheme: string)", Summary: "Sets the URL's scheme.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		scheme := lua.CheckString(l, 2)
		u.Scheme = scheme
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "host", Signature: "() -> string", Summary: "Returns the URL's host.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.Host)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_host", Signature: "(host: string)", Summary: "Sets the URL's host.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		host := lua.CheckString(l, 2)
		u.Host = host
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "path", Signature: "() -> string", Summary: "Returns the URL's path.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.Path)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_path", Signature: "(path: string)", Summary: "Sets the URL's path.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		path := lua.CheckString(l, 2)
		u.Path = path
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "query", Signature: "() -> string", Summary: "Returns the URL's raw query string.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.RawQuery)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_query", Signature: "(query: string)", Summary: "Sets the URL's raw query string.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		query := lua.CheckString(l, 2)
		u.RawQuery = query
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "get_param", Signature: "(key: string) -> string|nil", Summary: "Returns the first value for the given query parameter.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		key := lua.CheckString(l, 2)
		val := u.Query().Get(key)
		if val == "" {
			l.PushNil()
			return 1
		}
		l.PushString(val)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_param", Signature: "(key: string, value: string)", Summary: "Sets a query parameter to a single value.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		key := lua.CheckString(l, 2)
		value := lua.CheckString(l, 3)

		q := u.Query()
		q.Set(key, value)
		u.RawQuery = q.Encode()
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "del_param", Signature: "(key: string)", Summary: "Deletes a query parameter.", Function: func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		key := lua.CheckString(l, 2)

		q := u.Query()
		q.Del(key)
		u.RawQuery = q.Encode()
		return 0
	}})

	RegisterType(extension, "url", "A parsed URL, from request:url() or marasi.utils:url(raw).", funcs, func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.String())
		return 1
	})
}

// RegisterRequestType registers the `http.Request` type and its methods with the Lua state.
// This allows Lua scripts to read and modify incoming HTTP requests.
func RegisterRequestType(extension *Runtime) {
	var funcs []APIFunction

	funcs = append(funcs, APIFunction{Name: "id", Signature: "() -> string", Summary: "Returns the request's unique ID.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		if requestId, ok := core.RequestIDFromContext(req.Context()); ok {
			l.PushString(requestId.String())
			return 1
		}
		l.PushNil()
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "method", Signature: "() -> string", Summary: "Returns the request's method.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.Method)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "url", Signature: "() -> url", Summary: "Returns the request's URL object.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushUserData(req.URL)
		lua.SetMetaTableNamed(l, "url")
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "path", Signature: "() -> string", Summary: "Returns the request's path.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.URL.Path)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "host", Signature: "() -> string", Summary: "Returns the request's host.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.Host)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_host", Signature: "(host: string)", Summary: "Sets the request's host.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		newHost := lua.CheckString(l, 2)

		if metadata, ok := core.MetadataFromContext(req.Context()); ok {
			if _, exists := metadata["original_host_header"]; !exists {
				metadata["original_host_header"] = req.Host
			}
			metadata["override_host_header"] = newHost
			*req = *core.ContextWithMetadata(req, metadata)
		}

		req.Host = newHost
		return 0

	}})

	funcs = append(funcs, APIFunction{Name: "scheme", Signature: "() -> string", Summary: "Returns the request's scheme.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.URL.Scheme)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "proto", Signature: "() -> string", Summary: "Returns the request's protocol.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.Proto)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "remote_addr", Signature: "() -> string", Summary: "Returns the remote address of the client.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.RemoteAddr)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "body", Signature: "() -> string", Summary: "Returns the request's body as a string.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		if req.Body == nil {
			l.PushString("")
			return 1
		}

		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			lua.Errorf(l, fmt.Sprintf("reading body : %s", err.Error()))
			return 0
		}

		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		l.PushString(string(bodyBytes))
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_body", Signature: "(body: string)", Summary: "Sets the request's body.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		body := lua.CheckString(l, 2)

		req.Body = io.NopCloser(bytes.NewBufferString(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "headers", Signature: "() -> header", Summary: "Returns the request's headers.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		l.PushUserData(&req.Header)
		lua.SetMetaTableNamed(l, "header")
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "content_type", Signature: "() -> string", Summary: "Returns the request's Content-Type.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		ct := req.Header.Get("Content-Type")
		if ct == "" {
			l.PushString("")
			return 1
		}

		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil {
			l.PushString(ct)
		} else {
			l.PushString(mediaType)
		}
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "cookie", Signature: "(name: string) -> cookie|nil", Summary: "Returns a specific cookie from the request.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		name := lua.CheckString(l, 2)

		cookie, err := req.Cookie(name)
		if err != nil {
			lua.Errorf(l, fmt.Sprintf("getting cookie : %s", err.Error()))
			return 0
		}

		l.PushUserData(cookie)
		lua.SetMetaTableNamed(l, "cookie")
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_cookie", Signature: "(cookie: cookie)", Summary: "Adds or replaces a cookie in the request.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		newCookie := lua.CheckUserData(l, 2, "cookie").(*http.Cookie)

		cookies := req.Cookies()

		idx := slices.IndexFunc(cookies, func(c *http.Cookie) bool {
			return c.Name == newCookie.Name
		})

		if idx != -1 {
			cookies[idx] = newCookie
		} else {
			cookies = append(cookies, newCookie)
		}

		req.Header.Del("Cookie")
		for _, c := range cookies {
			req.AddCookie(c)
		}

		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "cookies", Signature: "() -> table", Summary: "Returns all cookies from the request.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		cookies := req.Cookies()

		l.CreateTable(len(cookies), 0)

		for i, c := range cookies {
			l.PushInteger(i + 1)
			l.PushUserData(c)
			lua.SetMetaTableNamed(l, "cookie")
			l.SetTable(-3)
		}

		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_cookies", Signature: "(cookies: table)", Summary: "Replaces all cookies in the request.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		if l.TypeOf(2) != lua.TypeTable {
			lua.ArgumentError(l, 2, "expected table")
			return 0
		}

		req.Header.Del("Cookie")

		l.PushNil()
		for l.Next(2) {
			if l.IsUserData(-1) {
				if c, ok := l.ToUserData(-1).(*http.Cookie); ok {
					req.AddCookie(c)
				}
			}
			l.Pop(1)
		}
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "metadata", Signature: "() -> table", Summary: "Returns the request's metadata.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		if metadata, ok := core.MetadataFromContext(req.Context()); ok {
			util.DeepPush(l, metadata)
			return 1
		}

		l.PushNil()
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_metadata", Signature: "(metadata: table)", Summary: "Replaces this extension's metadata on the request.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		metadata, ok := core.MetadataFromContext(req.Context())
		if !ok {
			lua.Errorf(l, "request context missing metadata")
			return 0
		}

		val := ParseTable(l, 2, GoValue)

		extensionMetadata, ok := val.(map[string]any)
		if !ok {
			lua.ArgumentError(l, 2, "metadata must be a key-value table, not an array")
			return 0
		}

		metadata[extension.Data.Name] = dropNilMembers(extensionMetadata)
		*req = *core.ContextWithMetadata(req, metadata)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "drop", Signature: "()", Summary: "Marks the request to be dropped by the proxy.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		*req = *core.ContextWithDropFlag(req, true)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "skip", Signature: "()", Summary: "Marks the request to be skipped by other extensions.", Function: func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		*req = *core.ContextWithSkipFlag(req, true)
		return 0
	}})

	RegisterType(extension, "req", "An HTTP request, passed to processRequest and interceptRequest.", funcs, func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		id := "unknown"
		if val := req.Context().Value(core.RequestIDKey); val != nil {
			if uuidVal, ok := val.(uuid.UUID); ok {
				id = uuidVal.String()
			}
		}

		l.PushString(fmt.Sprintf(
			"Request { ID: %s, Method: %s, URL: %s, Proto: %s, Remote: %s, Length: %d }",
			id,
			req.Method,
			req.URL.String(),
			req.Proto,
			req.RemoteAddr,
			req.ContentLength,
		))
		return 1
	})
}

// RegisterWebSocketMessageType registers the websocket Message type and its methods with the Lua state.
func RegisterWebSocketMessageType(extension *Runtime) {
	var funcs []APIFunction

	funcs = append(funcs, APIFunction{Name: "id", Signature: "() -> string", Summary: "Returns the message's UUID.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushString(message.ID.String())
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "connection_id", Signature: "() -> string", Summary: "Returns the UUID of the WebSocket connection that carried the message.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushString(message.ConnectionID.String())
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "request_id", Signature: "() -> string", Summary: "Returns the UUID of the upgrade request that opened the connection.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushString(message.RequestID.String())
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "direction", Signature: "() -> string", Summary: "Returns the side that sent the message: \"client\" or \"server\".", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushString(message.Direction)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "opcode", Signature: "() -> integer", Summary: "Returns the WebSocket opcode, such as 1 for text or 2 for binary.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushInteger(message.Opcode)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_opcode", Signature: "(opcode: integer)", Summary: "Sets the WebSocket opcode.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		message.Opcode = lua.CheckInteger(l, 2)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "payload", Signature: "() -> string", Summary: "Returns the message payload.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushString(string(message.Payload))
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_payload", Signature: "(payload: string)", Summary: "Replaces the message payload.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		message.Payload = []byte(lua.CheckString(l, 2))
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "length", Signature: "() -> integer", Summary: "Returns the payload length in bytes.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushInteger(len(message.Payload))
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "frame_count", Signature: "() -> integer", Summary: "Returns how many frames carried the message on the wire.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushInteger(message.FrameCount)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "compressed", Signature: "() -> boolean", Summary: "Reports whether the message was compressed with permessage-deflate on the wire. payload() is always uncompressed.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushBoolean(message.Compressed)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "is_binary", Signature: "() -> boolean", Summary: "Reports whether the message is binary.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushBoolean(message.IsBinary())
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "metadata", Signature: "() -> table", Summary: "Returns the message metadata, keyed by extension name.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		util.DeepPush(l, message.Metadata)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_metadata", Signature: "(metadata: table)", Summary: "Merges a key-value table into this extension's metadata on the message.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		value := ParseTable(l, 2, GoValue)
		metadata, ok := value.(map[string]any)
		if !ok {
			lua.ArgumentError(l, 2, "metadata must be a key-value table, not an array")
			return 0
		}
		if message.Metadata == nil {
			message.Metadata = make(map[string]any)
		}
		extensionMetadata, _ := message.Metadata[extension.Data.Name].(map[string]any)
		if extensionMetadata == nil {
			extensionMetadata = make(map[string]any)
		}
		maps.Copy(extensionMetadata, metadata)
		message.Metadata[extension.Data.Name] = extensionMetadata
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "drop", Signature: "()", Summary: "Marks the message to be dropped instead of forwarded.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		message.Dropped = true
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "skip", Signature: "()", Summary: "Marks the message to skip later extensions while still forwarding it.", Function: func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		message.Skipped = true
		return 0
	}})

	RegisterType(extension, "wsmsg", "A WebSocket message, passed to processWebSocketMessage and interceptWebSocketMessage.", funcs, func(l *lua.State) int {
		message := lua.CheckUserData(l, 1, "wsmsg").(*marasiws.Message)
		l.PushString(fmt.Sprintf(
			"WebSocketMessage { ID: %s, ConnectionID: %s, Direction: %s, Opcode: %d, Length: %d }",
			message.ID,
			message.ConnectionID,
			message.Direction,
			message.Opcode,
			len(message.Payload),
		))
		return 1
	})
}

// RegisterResponseType registers the `http.Response` type and its methods with the Lua state.
// This allows Lua scripts to read and modify outgoing HTTP responses.
func RegisterResponseType(extension *Runtime) {
	var funcs []APIFunction

	funcs = append(funcs, APIFunction{Name: "id", Signature: "() -> string", Summary: "Returns the response's associated request ID.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		if requestId, ok := core.RequestIDFromContext(res.Request.Context()); ok {
			l.PushString(requestId.String())
			return 1
		}
		l.PushNil()
		return 0
	}})
	funcs = append(funcs, APIFunction{Name: "method", Signature: "() -> string", Summary: "Returns the method of the original request.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		if res.Request != nil {
			l.PushString(res.Request.Method)
			return 1
		}
		l.PushNil()
		return 0
	}})
	funcs = append(funcs, APIFunction{Name: "url", Signature: "() -> url", Summary: "Returns the URL of the original request.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushUserData(res.Request.URL)
		lua.SetMetaTableNamed(l, "url")
		return 1
	}})
	funcs = append(funcs, APIFunction{Name: "status", Signature: "() -> string", Summary: "Returns the response's status line.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushString(res.Status)
		return 1
	}})
	funcs = append(funcs, APIFunction{Name: "status_code", Signature: "() -> number", Summary: "Returns the response's status code.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushInteger(res.StatusCode)
		return 1
	}})
	funcs = append(funcs, APIFunction{Name: "set_status_code", Signature: "(code: number)", Summary: "Sets the response's status code.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		code := lua.CheckInteger(l, 2)

		res.StatusCode = code
		res.Status = fmt.Sprintf("%d %s", code, http.StatusText(code))
		return 0
	}})
	funcs = append(funcs, APIFunction{Name: "length", Signature: "() -> number", Summary: "Returns the response's content length.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushInteger(int(res.ContentLength))
		return 1
	}})
	funcs = append(funcs, APIFunction{Name: "body", Signature: "() -> string", Summary: "Returns the response's body as a string.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		if res.Body == nil {
			l.PushString("")
			return 1
		}

		bodyBytes, err := io.ReadAll(res.Body)
		if err != nil {
			lua.Errorf(l, fmt.Sprintf("reading body : %s", err.Error()))
			return 0
		}

		res.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		l.PushString(string(bodyBytes))
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_body", Signature: "(body: string)", Summary: "Sets the response's body.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		body := lua.CheckString(l, 2)

		res.Body = io.NopCloser(bytes.NewBufferString(body))
		res.ContentLength = int64(len(body))
		res.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "headers", Signature: "() -> header", Summary: "Returns the response's headers.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		l.PushUserData(&res.Header)
		lua.SetMetaTableNamed(l, "header")
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "content_type", Signature: "() -> string", Summary: "Returns the response's Content-Type.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		ct := res.Header.Get("Content-Type")
		if ct == "" {
			l.PushString("")
			return 1
		}

		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil {
			l.PushString(ct)
		} else {
			l.PushString(mediaType)
		}
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "cookie", Signature: "(name: string) -> cookie|nil", Summary: "Returns a specific cookie from the response.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		name := lua.CheckString(l, 2)
		for _, cookie := range res.Cookies() {
			if cookie.Name == name {
				l.PushUserData(cookie)
				lua.SetMetaTableNamed(l, "cookie")
				return 1
			}
		}
		l.PushNil()
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_cookie", Signature: "(cookie: cookie)", Summary: "Adds or replaces a cookie in the response.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		newCookie := lua.CheckUserData(l, 2, "cookie").(*http.Cookie)

		cookies := res.Cookies()

		idx := slices.IndexFunc(cookies, func(c *http.Cookie) bool {
			return c.Name == newCookie.Name
		})

		if idx != -1 {
			cookies[idx] = newCookie
		} else {
			cookies = append(cookies, newCookie)
		}

		res.Header.Del("Set-Cookie")
		for _, c := range cookies {
			res.Header.Add("Set-Cookie", c.String())
		}
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "cookies", Signature: "() -> table", Summary: "Returns all cookies from the response.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		cookies := res.Cookies()

		l.CreateTable(len(cookies), 0)
		for i, c := range cookies {
			l.PushInteger(i + 1)
			l.PushUserData(c)
			lua.SetMetaTableNamed(l, "cookie")
			l.SetTable(-3)
		}
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_cookies", Signature: "(cookies: table)", Summary: "Replaces all cookies in the response.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		if l.TypeOf(2) != lua.TypeTable {
			lua.ArgumentError(l, 2, "expected table")
			return 0
		}

		res.Header.Del("Set-Cookie")

		l.PushNil()
		for l.Next(2) {
			if l.IsUserData(-1) {
				if c, ok := l.ToUserData(-1).(*http.Cookie); ok {
					res.Header.Add("Set-Cookie", c.String())
				}
			}
			l.Pop(1)
		}

		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "metadata", Signature: "() -> table", Summary: "Returns the response's metadata.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		if metadata, ok := core.MetadataFromContext(res.Request.Context()); ok {
			util.DeepPush(l, metadata)
			return 1
		}

		l.PushNil()
		return 1
	}})
	funcs = append(funcs, APIFunction{Name: "set_metadata", Signature: "(metadata: table)", Summary: "Replaces this extension's metadata on the response.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		metadata, ok := core.MetadataFromContext(res.Request.Context())
		if !ok {
			lua.Errorf(l, "response context missing metadata")
			return 0
		}

		val := ParseTable(l, 2, GoValue)

		extensionMetadata, ok := val.(map[string]any)
		if !ok {
			lua.ArgumentError(l, 2, "metadata must be a key-value table, not an array")
			return 0
		}

		metadata[extension.Data.Name] = dropNilMembers(extensionMetadata)
		*res.Request = *core.ContextWithMetadata(res.Request, metadata)
		return 0
	}})

	funcs = append(funcs, APIFunction{Name: "request", Signature: "() -> req", Summary: "Returns the original request object associated with this response.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		if res.Request != nil {
			l.PushUserData(res.Request)
			lua.SetMetaTableNamed(l, "req")
			return 1
		}
		l.PushNil()
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "drop", Signature: "()", Summary: "Marks the response to be dropped by the proxy.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		res.Request = core.ContextWithDropFlag(res.Request, true)
		return 0
	}})
	funcs = append(funcs, APIFunction{Name: "skip", Signature: "()", Summary: "Marks the response to be skipped by other extensions.", Function: func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		res.Request = core.ContextWithSkipFlag(res.Request, true)
		return 0
	}})
	RegisterType(extension, "res", "An HTTP response, passed to processResponse and interceptResponse.", funcs, func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		id := "unknown"
		if val := res.Request.Context().Value(core.RequestIDKey); val != nil {
			if uuidVal, ok := val.(uuid.UUID); ok {
				id = uuidVal.String()
			}
		}

		result := fmt.Sprintf(
			"Response { ID: %s, Status: %s (%d), Proto: %s, Length: %d, Headers: %d }",
			id,
			res.Status,
			res.StatusCode,
			res.Proto,
			res.ContentLength,
			len(res.Header),
		)
		l.PushString(result)
		return 1
	})
}

// RegisterRequestBuilderType registers the `RequestBuilder` type and its methods with the Lua state.
// This allows Lua scripts to construct and send new HTTP requests from within an extension.
func RegisterRequestBuilderType(extension *Runtime) {
	var funcs []APIFunction
	funcs = append(funcs, APIFunction{Name: "method", Signature: "() -> string", Summary: "Returns the request builder's method.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushString(builder.method)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_method", Signature: "(method: string) -> RequestBuilder", Summary: "Sets the request builder's method.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		method := lua.CheckString(l, 2)

		if method == "" {
			lua.ArgumentError(l, 2, "HTTP method cannot be empty")
			return 0
		}
		builder.method = strings.ToUpper(method)
		l.PushValue(1)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "url", Signature: "() -> url", Summary: "Returns the request builder's URL object.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushUserData(builder.url)
		lua.SetMetaTableNamed(l, "url")
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_url", Signature: "(url: string|url) -> RequestBuilder", Summary: "Sets the request builder's URL.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		if l.IsString(2) {
			urlStr := lua.CheckString(l, 2)
			if urlStr == "" {
				lua.ArgumentError(l, 2, "URL cannot be empty")
				return 0
			}
			parsed, err := url.Parse(urlStr)
			if err != nil {
				lua.ArgumentError(l, 2, fmt.Sprintf("invalid URL: %v", err))
				return 0
			}
			builder.url = parsed
			l.PushValue(1)
			return 1
		}
		u := lua.CheckUserData(l, 2, "url").(*url.URL)

		newUrl := *u
		builder.url = &newUrl
		l.PushValue(1)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "body", Signature: "() -> string", Summary: "Returns the request builder's body.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushString(builder.body)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_body", Signature: "(body: string) -> RequestBuilder", Summary: "Sets the request builder's body.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		builder.body = lua.CheckString(l, 2)
		l.PushValue(1)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "headers", Signature: "() -> header", Summary: "Returns the request builder's headers.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushUserData(&builder.headers)
		lua.SetMetaTableNamed(l, "header")
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_headers", Signature: "(headers: header) -> RequestBuilder", Summary: "Sets the request builder's headers.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		header := lua.CheckUserData(l, 2, "header").(*http.Header)
		builder.headers = header.Clone()
		l.PushValue(1)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "add_header", Signature: "(name: string, value: string) -> RequestBuilder", Summary: "Adds a header to the request builder.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		name := lua.CheckString(l, 2)
		value := lua.CheckString(l, 3)

		if name == "" {
			lua.ArgumentError(l, 2, "header name cannot be empty")
			return 0
		}

		builder.headers.Add(name, value)
		if strings.ToLower(name) == "content-type" {
			builder.contentType = value
		}
		l.PushValue(1)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "cookies", Signature: "() -> table", Summary: "Returns the request builder's cookies.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		l.CreateTable(len(builder.cookies), 0)

		for i, c := range builder.cookies {
			l.PushInteger(i + 1)
			l.PushUserData(c)
			lua.SetMetaTableNamed(l, "cookie")
			l.SetTable(-3)
		}

		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_cookie", Signature: "(cookie: cookie) -> RequestBuilder", Summary: "Adds or replaces a cookie in the request builder.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		c := lua.CheckUserData(l, 2, "cookie").(*http.Cookie)

		cookieCopy := *c

		idx := slices.IndexFunc(builder.cookies, func(existing *http.Cookie) bool {
			return existing.Name == cookieCopy.Name
		})

		if idx != -1 {
			builder.cookies[idx] = &cookieCopy
		} else {
			builder.cookies = append(builder.cookies, &cookieCopy)
		}

		l.PushValue(1)
		return 1
	}})
	funcs = append(funcs, APIFunction{Name: "set_cookies", Signature: "(cookies: table) -> RequestBuilder", Summary: "Replaces all cookies in the request builder.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		if l.TypeOf(2) != lua.TypeTable {
			lua.ArgumentError(l, 2, "expected table")
			return 0
		}

		builder.cookies = make([]*http.Cookie, 0)

		l.PushNil()
		for l.Next(2) {
			if l.IsUserData(-1) {
				if c, ok := l.ToUserData(-1).(*http.Cookie); ok {
					cookieCopy := *c
					builder.cookies = append(builder.cookies, &cookieCopy)
				}
			}
			l.Pop(1)
		}

		l.PushValue(1)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "metadata", Signature: "() -> table", Summary: "Returns the request builder's metadata.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		util.DeepPush(l, builder.metadata)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "set_metadata", Signature: "(metadata: table) -> RequestBuilder", Summary: "Sets the request builder's metadata.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		val := ParseTable(l, 2, GoValue)

		metadataMap, ok := val.(map[string]any)
		if !ok {
			lua.ArgumentError(l, 2, "Metadata must be a table with string keys")
			return 0
		}

		builder.metadata = metadataMap
		l.PushValue(1)
		return 1
	}})

	funcs = append(funcs, APIFunction{Name: "send", Signature: "() -> res|nil, string|nil", Summary: "Sends the request and waits for the response. On failure, returns nil and an error message.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		if builder.method == "" || builder.url.String() == "" || builder.url == nil {
			lua.Errorf(l, "method and url must be set before sending the request")
			return 0
		}

		// Request Body
		reqBody := bytes.NewBuffer([]byte(builder.body))

		req, err := http.NewRequestWithContext(extension.executionContext, builder.method, builder.url.String(), reqBody)
		if err != nil {
			lua.Errorf(l, "creating new request : %s", err.Error())
			return 0
		}

		// Headers
		req.Header = builder.headers

		// Metadata
		builder.metadata["request_builder"] = true
		builder.metadata["marasi_extension_id"] = extension.Data.ID.String()
		if len(builder.metadata) > 0 {
			if jsonBytes, err := json.Marshal(builder.metadata); err == nil {
				req.Header.Set("x-marasi-metadata", string(jsonBytes))
			}
		}

		// Cookies
		for _, c := range builder.cookies {
			req.AddCookie(c)
		}

		// x-extension-id
		req.Header.Set("x-extension-id", extension.Data.ID.String())

		resp, err := builder.client.Do(req)
		if err != nil {
			l.PushNil()
			l.PushString(fmt.Sprintf("sending request: %v", err))
			return 2
		}
		defer resp.Body.Close()

		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			l.PushNil()
			l.PushString(fmt.Sprintf("reading response: %v", err))
			return 2
		}
		resp.Body = io.NopCloser(bytes.NewReader(responseBody))

		l.PushUserData(resp)
		lua.SetMetaTableNamed(l, "res")
		l.PushNil()
		return 2
	}})

	funcs = append(funcs, APIFunction{Name: "send_async", Signature: "(callback?: function)", Summary: "Sends the request without waiting. The callback receives the response and nil, or nil and an error message.", Function: func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		if builder.method == "" || builder.url.String() == "" || builder.url == nil {
			lua.Errorf(l, "method and url must be set before sending the request")
			return 0
		}

		var callbackKey string
		if l.IsFunction(2) {
			callbackKey = fmt.Sprintf("marasi_cb_%d", atomic.AddUint64(&globalCallbackCounter, 1))
			l.PushValue(2)
			l.SetField(lua.RegistryIndex, callbackKey)
		}

		reqMethod := builder.method
		reqURLStr := builder.url.String()
		reqBody := builder.body
		reqHeaders := builder.headers.Clone()

		reqCookies := make([]*http.Cookie, len(builder.cookies))
		copy(reqCookies, builder.cookies)

		reqMetadata := make(map[string]any)
		maps.Copy(reqMetadata, builder.metadata)

		extID := extension.Data.ID.String()
		ctx := extension.ExecutionContext
		if ctx == nil {
			ctx = context.Background()
		}
		release := func() {}
		if extension.TrackExecution != nil {
			release = extension.TrackExecution()
		}
		extension.asyncExecutions.Add(1)

		go func() {
			defer extension.asyncExecutions.Done()
			defer release()
			reqBodyBuffer := bytes.NewBuffer([]byte(reqBody))
			var resp *http.Response
			req, err := http.NewRequestWithContext(ctx, reqMethod, reqURLStr, reqBodyBuffer)
			if err == nil {
				req.Header = reqHeaders

				reqMetadata["request_builder"] = true
				reqMetadata["marasi_extension_id"] = extID

				if len(reqMetadata) > 0 {
					if jsonBytes, err := json.Marshal(reqMetadata); err == nil {
						req.Header.Set("x-marasi-metadata", string(jsonBytes))
					}
				}

				for _, c := range reqCookies {
					req.AddCookie(c)
				}

				req.Header.Set("x-extension-id", extID)

				resp, err = builder.client.Do(req)

			}
			if resp != nil {
				defer resp.Body.Close()
			}

			if callbackKey != "" {
				extension.Mu.Lock()
				defer extension.Mu.Unlock()
				if ctx.Err() != nil {
					return
				}
				defer extension.beginExecution(ctx)()

				top := l.Top()
				defer l.SetTop(top)

				l.Field(lua.RegistryIndex, callbackKey)

				if l.IsFunction(-1) {
					if err != nil {
						l.PushNil()
						l.PushString(err.Error())
					} else {
						bodyBytes, readErr := io.ReadAll(resp.Body)
						resp.Body.Close()

						if readErr != nil {
							l.PushNil()
							l.PushString(fmt.Sprintf("reading body: %s", readErr.Error()))
						} else {
							resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
							l.PushUserData(resp)
							lua.SetMetaTableNamed(l, "res")
							l.PushNil()
						}
					}

					if callErr := l.ProtectedCall(2, 0, 0); callErr != nil {
						// TODO: This needs to be properly logged
					}
				}

				l.PushNil()
				l.SetField(lua.RegistryIndex, callbackKey)
			}
		}()
		return 0
	}})

	// Register the RequestBuilder type and its methods in Lua
	RegisterType(extension, "RequestBuilder", "Builds and sends a new HTTP request, from marasi:builder().", funcs, func(l *lua.State) int {
		rb := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		var cookies []string
		for _, c := range rb.cookies {
			cookies = append(cookies, c.String())
		}

		view := struct {
			Method   string
			URL      string
			Headers  http.Header
			Cookies  []string
			Body     string
			Metadata map[string]any
		}{
			Method:   rb.method,
			URL:      rb.url.String(),
			Headers:  rb.headers,
			Cookies:  cookies,
			Body:     rb.body,
			Metadata: rb.metadata,
		}

		l.PushString(fmt.Sprintf("%+v", view))
		return 1

	})
}
