package extensions

import (
	"bytes"
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
	"time"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/core"
)

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
	funcs := map[string]lua.Function{
		// add_rule adds a new rule to the scope.
		//
		// @param rule string The rule to add.
		// @param matchType string The type of match (e.g., "host", "path").
		"add_rule": func(l *lua.State) int {
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
		},
		// remove_rule removes a rule from the scope.
		//
		// @param rule string The rule to remove.
		// @param matchType string The type of match.
		"remove_rule": func(l *lua.State) int {
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
		},
		// matches checks if a request or response matches the scope.
		//
		// @param input Request|Response The request or response to check.
		// @return boolean True if the input matches the scope.
		"matches": func(l *lua.State) int {
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
		},
		// set_default_allow sets the default scope policy.
		//
		// @param allow boolean True to allow by default, false to block.
		"set_default_allow": func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			allow := l.ToBoolean(2)

			scope.DefaultAllow = allow
			return 0
		},
		// matches_string checks if a string matches a specific rule type in the scope.
		//
		// @param input string The string to check.
		// @param matchType string The type of match to perform.
		// @return boolean True if the string matches.
		"matches_string": func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			input := lua.CheckString(l, 2)
			matchType := lua.CheckString(l, 3)
			result := scope.MatchesString(input, matchType)

			l.PushBoolean(result)
			return 1
		},
		// clear_rules removes all rules from the scope.
		"clear_rules": func(l *lua.State) int {
			scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)
			scope.ClearRules()
			return 0
		},
	}

	RegisterType(extension.LuaState, "scope", funcs, func(l *lua.State) int {
		scope := lua.CheckUserData(l, 1, "scope").(*compass.Scope)

		policy := "Block"
		if scope.DefaultAllow {
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
			formatRules(scope.IncludeRules),
			formatRules(scope.ExcludeRules),
		)

		l.PushString(result)
		return 1
	})
}

// RegisterRegexType registers the `regexp.Regexp` type and its methods with the Lua state.
// This allows Lua scripts to perform regular expression matching, searching, and replacement.
func RegisterRegexType(extension *Runtime) {
	funcs := make(map[string]lua.Function)

	// match checks if the regex matches a string.
	//
	// @param input string The string to match against.
	// @return boolean True if the regex matches.
	funcs["match"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		matched := re.MatchString(input)

		l.PushBoolean(matched)
		return 1
	}

	// is_anchored_match checks if the regex matches the entire string.
	//
	// @param input string The string to match against.
	// @return boolean True if the regex matches the entire string.
	funcs["is_anchored_match"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)

		loc := re.FindStringIndex(input)
		isAnchored := loc != nil && loc[0] == 0 && loc[1] == len(input)

		l.PushBoolean(isAnchored)
		return 1
	}

	// find_submatch_indices returns the indices of the first match and its submatches.
	//
	// @param input string The string to search in.
	// @return table A table of indices, or nil if no match.
	funcs["find_submatch_indices"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		indices := re.FindStringSubmatchIndex(input)

		if indices == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, indices)
		return 1
	}

	// find_named_submatch returns a table of named submatches.
	//
	// @param input string The string to search in.
	// @return table A table of named submatches, or nil if no match.
	funcs["find_named_submatch"] = func(l *lua.State) int {
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
	}

	// find_all returns all non-overlapping matches in a string.
	//
	// @param input string The string to search in.
	// @return table A table of all matches, or nil if no match.
	funcs["find_all"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		matches := re.FindAllString(input, -1)

		if matches == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, matches)
		return 1
	}

	// replace replaces all matches in a string with a replacement string.
	//
	// @param input string The string to search in.
	// @param replacement string The replacement string.
	// @return string The new string.
	funcs["replace"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		replacement := lua.CheckString(l, 3)
		result := re.ReplaceAllString(input, replacement)

		l.PushString(result)
		return 1
	}

	// split splits a string by the regex.
	//
	// @param input string The string to split.
	// @return table A table of the split parts.
	funcs["split"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		parts := re.Split(input, -1)
		util.DeepPush(l, parts)
		return 1
	}

	// pattern returns the regex pattern as a string.
	//
	// @return string The regex pattern.
	funcs["pattern"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		l.PushString(re.String())
		return 1
	}

	// find returns the first match in a string.
	//
	// @param input string The string to search in.
	// @return string The first match, or an empty string if no match.
	funcs["find"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		match := re.FindString(input)

		l.PushString(match)
		return 1
	}

	// find_submatch returns the first match and its submatches.
	//
	// @param input string The string to search in.
	// @return table A table of the match and its submatches, or nil if no match.
	funcs["find_submatch"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		submatches := re.FindStringSubmatch(input)

		if submatches == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, submatches)
		return 1
	}

	// find_all_submatches returns all matches and their submatches.
	//
	// @param input string The string to search in.
	// @return table A table of all matches and their submatches, or nil if no match.
	funcs["find_all_submatches"] = func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		input := lua.CheckString(l, 2)
		submatches := re.FindAllStringSubmatch(input, -1)

		if submatches == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, submatches)
		return 1
	}

	RegisterType(extension.LuaState, "regexp", funcs, func(l *lua.State) int {
		re := lua.CheckUserData(l, 1, "regexp").(*regexp.Regexp)
		l.PushString(fmt.Sprintf("Regexp { Pattern: %s, Subexpressions: %d }", re.String(), re.NumSubexp()))
		return 1
	})
}

// RegisterCookieType registers the `http.Cookie` type and its methods with the Lua state.
// This allows Lua scripts to read and modify HTTP cookies.
func RegisterCookieType(extension *Runtime) {
	funcs := make(map[string]lua.Function)

	// name returns the cookie's name.
	//
	// @return string The cookie's name.
	funcs["name"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Name)
		return 1
	}

	// set_name sets the cookie's name.
	//
	// @param name string The new name.
	funcs["set_name"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Name = lua.CheckString(l, 2)
		return 0
	}

	// value returns the cookie's value.
	//
	// @return string The cookie's value.
	funcs["value"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Value)
		return 1
	}

	// set_value sets the cookie's value.
	//
	// @param value string The new value.
	funcs["set_value"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Value = lua.CheckString(l, 2)
		return 0
	}

	// domain returns the cookie's domain.
	//
	// @return string The cookie's domain.
	funcs["domain"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Domain)
		return 1
	}

	// set_domain sets the cookie's domain.
	//
	// @param domain string The new domain.
	funcs["set_domain"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Domain = lua.CheckString(l, 2)
		return 0
	}

	// path returns the cookie's path.
	//
	// @return string The cookie's path.
	funcs["path"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.Path)
		return 1
	}

	// set_path sets the cookie's path.
	//
	// @param path string The new path.
	funcs["set_path"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Path = lua.CheckString(l, 2)
		return 0
	}

	// expires returns the cookie's expiration time as a Unix timestamp.
	//
	// @return number The expiration timestamp, or nil if not set.
	funcs["expires"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		if cookie.Expires.IsZero() {
			l.PushNil()
			return 1
		}
		l.PushNumber(float64(cookie.Expires.Unix()))
		return 1
	}

	// set_expires sets the cookie's expiration time from a Unix timestamp.
	//
	// @param timestamp number The new expiration timestamp.
	funcs["set_expires"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		timestamp := lua.CheckNumber(l, 2)

		cookie.Expires = time.Unix(int64(timestamp), 0)
		return 0
	}

	// secure returns the cookie's Secure flag.
	//
	// @return boolean The Secure flag.
	funcs["secure"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushBoolean(cookie.Secure)
		return 1
	}

	// set_secure sets the cookie's Secure flag.
	//
	// @param secure boolean The new Secure flag.
	funcs["set_secure"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.Secure = l.ToBoolean(2)
		return 0
	}

	// http_only returns the cookie's HttpOnly flag.
	//
	// @return boolean The HttpOnly flag.
	funcs["http_only"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushBoolean(cookie.HttpOnly)
		return 1
	}

	// set_http_only sets the cookie's HttpOnly flag.
	//
	// @param httpOnly boolean The new HttpOnly flag.
	funcs["set_http_only"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		cookie.HttpOnly = l.ToBoolean(2)
		return 0
	}

	// max_age returns the cookie's Max-Age value.
	//
	// @return number The Max-Age value.
	funcs["max_age"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushNumber(float64(cookie.MaxAge))
		return 1
	}

	// set_max_age sets the cookie's Max-Age value.
	//
	// @param maxAge number The new Max-Age value.
	funcs["set_max_age"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		maxAge := lua.CheckNumber(l, 2)

		cookie.MaxAge = int(maxAge)
		return 0
	}

	// same_site returns the cookie's SameSite attribute.
	//
	// @return string The SameSite attribute ("lax", "strict", "none", or "default").
	funcs["same_site"] = func(l *lua.State) int {
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
	}

	// set_same_site sets the cookie's SameSite attribute.
	//
	// @param sameSite string The new SameSite attribute ("lax", "strict", "none", or "default").
	funcs["set_same_site"] = func(l *lua.State) int {
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
	}

	// serialize returns the cookie as a string.
	//
	// @return string The serialized cookie.
	funcs["serialize"] = func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.String())
		return 1
	}

	RegisterType(extension.LuaState, "cookie", funcs, func(l *lua.State) int {
		cookie := lua.CheckUserData(l, 1, "cookie").(*http.Cookie)
		l.PushString(cookie.String())
		return 1
	})
}

// RegisterHeaderType registers the `http.Header` type and its methods with the Lua state.
// This allows Lua scripts to read and modify HTTP headers.
func RegisterHeaderType(extension *Runtime) {
	funcs := make(map[string]lua.Function)

	// get returns the first value associated with the given key.
	//
	// @param key string The header name.
	// @return string The header value, or nil if not found.
	funcs["get"] = func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)

		value := header.Get(key)
		if value == "" {
			l.PushNil()
			return 1
		}
		l.PushString(value)
		return 1
	}

	// values returns all values associated with the given key.
	//
	// @param key string The header name.
	// @return table A table of header values, or nil if not found.
	funcs["values"] = func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)

		values := header.Values(key)

		if values == nil {
			l.PushNil()
			return 1
		}

		util.DeepPush(l, values)
		return 1
	}

	// to_table returns the headers as a Lua table.
	//
	// @return table The headers as a table.
	funcs["to_table"] = func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		util.DeepPush(l, *header)
		return 1
	}

	// set sets the header entries associated with key to the single element value.
	// It replaces any existing values associated with key.
	//
	// @param key string The header name.
	// @param value string The header value.
	funcs["set"] = func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		value := lua.CheckString(l, 3)

		if key == "" {
			lua.ArgumentError(l, 2, "header key cannot be empty")
			return 0
		}

		header.Set(key, value)
		return 0
	}

	// add adds the key, value pair to the header. It appends to any existing
	// values associated with key.
	//
	// @param key string The header name.
	// @param value string The header value.
	funcs["add"] = func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		value := lua.CheckString(l, 3)

		if key == "" {
			lua.ArgumentError(l, 2, "header key cannot be empty")
			return 0
		}

		header.Add(key, value)
		return 0
	}

	// delete deletes the values associated with key.
	//
	// @param key string The header name.
	funcs["delete"] = func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		header.Del(key)
		return 0
	}

	// has checks if a header with the given key exists.
	//
	// @param key string The header name.
	// @return boolean True if the header exists.
	funcs["has"] = func(l *lua.State) int {
		header := lua.CheckUserData(l, 1, "header").(*http.Header)
		key := lua.CheckString(l, 2)
		if value := header.Get(key); value != "" {
			l.PushBoolean(true)
		} else {
			l.PushBoolean(false)
		}
		return 1
	}

	RegisterType(extension.LuaState, "header", funcs, func(l *lua.State) int {
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
	funcs := make(map[string]lua.Function)

	// string returns the URL as a string.
	//
	// @return string The URL string.
	funcs["string"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.String())
		return 1
	}

	// scheme returns the URL's scheme.
	//
	// @return string The scheme.
	funcs["scheme"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.Scheme)
		return 1
	}

	// set_scheme sets the URL's scheme.
	//
	// @param scheme string The new scheme.
	funcs["set_scheme"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		scheme := lua.CheckString(l, 2)
		u.Scheme = scheme
		return 0
	}

	// host returns the URL's host.
	//
	// @return string The host.
	funcs["host"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.Host)
		return 1
	}

	// set_host sets the URL's host.
	//
	// @param host string The new host.
	funcs["set_host"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		host := lua.CheckString(l, 2)
		u.Host = host
		return 0
	}

	// path returns the URL's path.
	//
	// @return string The path.
	funcs["path"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.Path)
		return 1
	}

	// set_path sets the URL's path.
	//
	// @param path string The new path.
	funcs["set_path"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		path := lua.CheckString(l, 2)
		u.Path = path
		return 0
	}

	// query returns the URL's raw query string.
	//
	// @return string The raw query string.
	funcs["query"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.RawQuery)
		return 1
	}

	// set_query sets the URL's raw query string.
	//
	// @param query string The new raw query string.
	funcs["set_query"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		query := lua.CheckString(l, 2)
		u.RawQuery = query
		return 0
	}

	// get_param returns the first value for the given query parameter.
	//
	// @param key string The parameter name.
	// @return string The parameter value, or nil if not found.
	funcs["get_param"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		key := lua.CheckString(l, 2)
		val := u.Query().Get(key)
		if val == "" {
			l.PushNil()
			return 1
		}
		l.PushString(val)
		return 1
	}

	// set_param sets a query parameter to a single value.
	//
	// @param key string The parameter name.
	// @param value string The parameter value.
	funcs["set_param"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		key := lua.CheckString(l, 2)
		value := lua.CheckString(l, 3)

		q := u.Query()
		q.Set(key, value)
		u.RawQuery = q.Encode()
		return 0
	}

	// del_param deletes a query parameter.
	//
	// @param key string The parameter name.
	funcs["del_param"] = func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		key := lua.CheckString(l, 2)

		q := u.Query()
		q.Del(key)
		u.RawQuery = q.Encode()
		return 0
	}

	RegisterType(extension.LuaState, "url", funcs, func(l *lua.State) int {
		u := lua.CheckUserData(l, 1, "url").(*url.URL)
		l.PushString(u.String())
		return 1
	})
}

// RegisterRequestType registers the `http.Request` type and its methods with the Lua state.
// This allows Lua scripts to read and modify incoming HTTP requests.
func RegisterRequestType(extension *Runtime) {
	funcs := make(map[string]lua.Function)

	// id returns the request's unique ID.
	//
	// @return string The request ID.
	funcs["id"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		if requestId, ok := core.RequestIDFromContext(req.Context()); ok {
			l.PushString(requestId.String())
			return 1
		}
		l.PushNil()
		return 0
	}

	// method returns the request's method.
	//
	// @return string The HTTP method.
	funcs["method"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.Method)
		return 1
	}

	// url returns the request's URL object.
	//
	// @return URL The URL object.
	funcs["url"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushUserData(req.URL)
		lua.SetMetaTableNamed(l, "url")
		return 1
	}

	// path returns the request's path.
	//
	// @return string The request path.
	funcs["path"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.URL.Path)
		return 1
	}

	// host returns the request's host.
	//
	// @return string The request host.
	funcs["host"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.Host)
		return 1
	}

	// set_host sets the request's host.
	//
	// @param host string The new host.
	funcs["set_host"] = func(l *lua.State) int {
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

	}

	// scheme returns the request's scheme.
	//
	// @return string The request scheme.
	funcs["scheme"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.URL.Scheme)
		return 1
	}

	// proto returns the request's protocol.
	//
	// @return string The request protocol.
	funcs["proto"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.Proto)
		return 1
	}

	// remote_addr returns the remote address of the client.
	//
	// @return string The remote address.
	funcs["remote_addr"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		l.PushString(req.RemoteAddr)
		return 1
	}

	// body returns the request's body as a string.
	//
	// @return string The request body.
	funcs["body"] = func(l *lua.State) int {
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
	}

	// set_body sets the request's body.
	//
	// @param body string The new request body.
	funcs["set_body"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		body := lua.CheckString(l, 2)

		req.Body = io.NopCloser(bytes.NewBufferString(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		return 0
	}

	// headers returns the request's headers.
	//
	// @return Header The header object.
	funcs["headers"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		l.PushUserData(&req.Header)
		lua.SetMetaTableNamed(l, "header")
		return 1
	}

	// content_type returns the request's Content-Type.
	//
	// @return string The Content-Type.
	funcs["content_type"] = func(l *lua.State) int {
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
	}

	// cookie returns a specific cookie from the request.
	//
	// @param name string The name of the cookie.
	// @return Cookie The cookie object, or nil if not found.
	funcs["cookie"] = func(l *lua.State) int {
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
	}

	// set_cookie adds or replaces a cookie in the request.
	//
	// @param cookie Cookie The cookie to set.
	funcs["set_cookie"] = func(l *lua.State) int {
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
	}

	// cookies returns all cookies from the request.
	//
	// @return table A table of cookie objects.
	funcs["cookies"] = func(l *lua.State) int {
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
	}

	// set_cookies replaces all cookies in the request.
	//
	// @param cookies table A table of cookie objects.
	funcs["set_cookies"] = func(l *lua.State) int {
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
	}

	// metadata returns the request's metadata.
	//
	// @return table The metadata table.
	funcs["metadata"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		if metadata, ok := core.MetadataFromContext(req.Context()); ok {
			util.DeepPush(l, metadata)
			return 1
		}

		l.PushNil()
		return 1
	}

	// set_metadata sets the request's metadata for the current extension.
	//
	// @param metadata table The metadata table to set.
	funcs["set_metadata"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)

		metadata, ok := core.MetadataFromContext(req.Context())
		if !ok {
			lua.Errorf(l, "request context missing metadata")
			return 0
		}

		val := parseTable(l, 2, goValue)

		extensionMetadata, ok := val.(map[string]any)
		if !ok {
			lua.ArgumentError(l, 2, "metadata must be a key-value table, not an array")
			return 0
		}

		metadata[extension.Data.Name] = extensionMetadata
		*req = *core.ContextWithMetadata(req, metadata)
		return 0
	}

	// drop marks the request to be dropped by the proxy.
	funcs["drop"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		*req = *core.ContextWithDropFlag(req, true)
		return 0
	}

	// skip marks the request to be skipped by other extensions.
	funcs["skip"] = func(l *lua.State) int {
		req := lua.CheckUserData(l, 1, "req").(*http.Request)
		*req = *core.ContextWithSkipFlag(req, true)
		return 0
	}

	RegisterType(extension.LuaState, "req", funcs, func(l *lua.State) int {
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

// RegisterResponseType registers the `http.Response` type and its methods with the Lua state.
// This allows Lua scripts to read and modify outgoing HTTP responses.
func RegisterResponseType(extension *Runtime) {
	funcs := make(map[string]lua.Function)

	// id returns the response's associated request ID.
	//
	// @return string The request ID.
	funcs["id"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		if requestId, ok := core.RequestIDFromContext(res.Request.Context()); ok {
			l.PushString(requestId.String())
			return 1
		}
		l.PushNil()
		return 0
	}
	// method returns the method of the original request.
	//
	// @return string The HTTP method.
	funcs["method"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		if res.Request != nil {
			l.PushString(res.Request.Method)
			return 1
		}
		l.PushNil()
		return 0
	}
	// url returns the URL of the original request.
	//
	// @return URL The URL object.
	funcs["url"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushUserData(res.Request.URL)
		lua.SetMetaTableNamed(l, "url")
		return 1
	}
	// status returns the response's status line.
	//
	// @return string The status line (e.g., "200 OK").
	funcs["status"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushString(res.Status)
		return 1
	}
	// status_code returns the response's status code.
	//
	// @return number The status code.
	funcs["status_code"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushInteger(res.StatusCode)
		return 1
	}
	// set_status_code sets the response's status code.
	//
	// @param code number The new status code.
	funcs["set_status_code"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		code := lua.CheckInteger(l, 2)

		res.StatusCode = code
		res.Status = fmt.Sprintf("%d %s", code, http.StatusText(code))
		return 0
	}
	// length returns the response's content length.
	//
	// @return number The content length.
	funcs["length"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		l.PushInteger(int(res.ContentLength))
		return 1
	}
	// body returns the response's body as a string.
	//
	// @return string The response body.
	funcs["body"] = func(l *lua.State) int {
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
	}

	// set_body sets the response's body.
	//
	// @param body string The new response body.
	funcs["set_body"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		body := lua.CheckString(l, 2)

		res.Body = io.NopCloser(bytes.NewBufferString(body))
		res.ContentLength = int64(len(body))
		res.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		return 0
	}

	// headers returns the response's headers.
	//
	// @return Header The header object.
	funcs["headers"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		l.PushUserData(&res.Header)
		lua.SetMetaTableNamed(l, "header")
		return 1
	}

	// content_type returns the response's Content-Type.
	//
	// @return string The Content-Type.
	funcs["content_type"] = func(l *lua.State) int {
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
	}

	// cookie returns a specific cookie from the response.
	//
	// @param name string The name of the cookie.
	// @return Cookie The cookie object, or nil if not found.
	funcs["cookie"] = func(l *lua.State) int {
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
	}

	// set_cookie adds or replaces a cookie in the response.
	//
	// @param cookie Cookie The cookie to set.
	funcs["set_cookie"] = func(l *lua.State) int {
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
	}

	// cookies returns all cookies from the response.
	//
	// @return table A table of cookie objects.
	funcs["cookies"] = func(l *lua.State) int {
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
	}

	// set_cookies replaces all cookies in the response.
	//
	// @param cookies table A table of cookie objects.
	funcs["set_cookies"] = func(l *lua.State) int {
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
	}

	// metadata returns the response's metadata.
	//
	// @return table The metadata table.
	funcs["metadata"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		if metadata, ok := core.MetadataFromContext(res.Request.Context()); ok {
			util.DeepPush(l, metadata)
			return 1
		}

		l.PushNil()
		return 1
	}
	// set_metadata sets the response's metadata for the current extension.
	//
	// @param metadata table The metadata table to set.
	funcs["set_metadata"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)

		metadata, ok := core.MetadataFromContext(res.Request.Context())
		if !ok {
			lua.Errorf(l, "response context missing metadata")
			return 0
		}

		val := parseTable(l, 2, goValue)

		extensionMetadata, ok := val.(map[string]any)
		if !ok {
			lua.ArgumentError(l, 2, "metadata must be a key-value table, not an array")
			return 0
		}

		metadata[extension.Data.Name] = extensionMetadata
		*res.Request = *core.ContextWithMetadata(res.Request, metadata)
		return 0
	}

	// drop marks the response to be dropped by the proxy.
	funcs["drop"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		res.Request = core.ContextWithDropFlag(res.Request, true)
		return 0
	}
	// skip marks the response to be skipped by other extensions.
	funcs["skip"] = func(l *lua.State) int {
		res := lua.CheckUserData(l, 1, "res").(*http.Response)
		res.Request = core.ContextWithSkipFlag(res.Request, true)
		return 0
	}
	RegisterType(extension.LuaState, "res", funcs, func(l *lua.State) int {
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
	var callbackCounter int
	funcs := make(map[string]lua.Function)
	// method returns the request builder's method.
	//
	// @return string The HTTP method.
	funcs["method"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushString(builder.method)
		return 1
	}

	// set_method sets the request builder's method.
	//
	// @param method string The new HTTP method.
	// @return RequestBuilder The request builder.
	funcs["set_method"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		method := lua.CheckString(l, 2)

		if method == "" {
			lua.ArgumentError(l, 2, "HTTP method cannot be empty")
			return 0
		}
		builder.method = strings.ToUpper(method)
		l.PushValue(1)
		return 1
	}

	// url returns the request builder's URL object.
	//
	// @return URL The URL object.
	funcs["url"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushUserData(builder.url)
		lua.SetMetaTableNamed(l, "url")
		return 1
	}

	// set_url sets the request builder's URL.
	//
	// @param url string|URL The new URL.
	// @return RequestBuilder The request builder.
	funcs["set_url"] = func(l *lua.State) int {
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
	}

	// body returns the request builder's body.
	//
	// @return string The request body.
	funcs["body"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushString(builder.body)
		return 1
	}

	// set_body sets the request builder's body.
	//
	// @param body string The new request body.
	// @return RequestBuilder The request builder.
	funcs["set_body"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		builder.body = lua.CheckString(l, 2)
		l.PushValue(1)
		return 1
	}

	// headers returns the request builder's headers.
	//
	// @return Header The header object.
	funcs["headers"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		l.PushUserData(&builder.headers)
		lua.SetMetaTableNamed(l, "header")
		return 1
	}

	// set_headers sets the request builder's headers.
	//
	// @param headers Header The new header object.
	// @return RequestBuilder The request builder.
	funcs["set_headers"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		header := lua.CheckUserData(l, 2, "header").(*http.Header)
		builder.headers = header.Clone()
		l.PushValue(1)
		return 1
	}

	// add_header adds a header to the request builder.
	//
	// @param name string The header name.
	// @param value string The header value.
	// @return RequestBuilder The request builder.
	funcs["add_header"] = func(l *lua.State) int {
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
	}

	// cookies returns the request builder's cookies.
	//
	// @return table A table of cookie objects.
	funcs["cookies"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		l.CreateTable(len(builder.cookies), 0)

		for i, c := range builder.cookies {
			l.PushInteger(i + 1)
			l.PushUserData(c)
			lua.SetMetaTableNamed(l, "cookie")
			l.SetTable(-3)
		}

		return 1
	}

	// set_cookie adds or replaces a cookie in the request builder.
	//
	// @param cookie Cookie The cookie to set.
	// @return RequestBuilder The request builder.
	funcs["set_cookie"] = func(l *lua.State) int {
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
	}
	// set_cookies replaces all cookies in the request builder.
	//
	// @param cookies table A table of cookie objects.
	// @return RequestBuilder The request builder.
	funcs["set_cookies"] = func(l *lua.State) int {
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
	}

	// metadata returns the request builder's metadata.
	//
	// @return table The metadata table.
	funcs["metadata"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)
		util.DeepPush(l, builder.metadata)
		return 1
	}

	// set_metadata sets the request builder's metadata.
	//
	// @param metadata table The metadata table to set.
	// @return RequestBuilder The request builder.
	funcs["set_metadata"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		val := parseTable(l, 2, goValue)

		metadataMap, ok := val.(map[string]any)
		if !ok {
			lua.ArgumentError(l, 2, "Metadata must be a table with string keys")
			return 0
		}

		builder.metadata = metadataMap
		l.PushValue(1)
		return 1
	}

	// send sends the HTTP request.
	//
	// @return Response|nil, string The response object, or nil and an error message.
	funcs["send"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		if builder.method == "" || builder.url.String() == "" || builder.url == nil {
			lua.Errorf(l, "method and url must be set before sending the request")
			return 0
		}

		// Request Body
		reqBody := bytes.NewBuffer([]byte(builder.body))

		req, err := http.NewRequest(builder.method, builder.url.String(), reqBody)
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
	}

	// send_async sends the HTTP request asynchronously.
	//
	// @param callback function (optional) A function to call with the response.
	funcs["send_async"] = func(l *lua.State) int {
		builder := lua.CheckUserData(l, 1, "RequestBuilder").(*RequestBuilder)

		if builder.method == "" || builder.url.String() == "" || builder.url == nil {
			lua.Errorf(l, "method and url must be set before sending the request")
			return 0
		}

		var callbackID int = 0
		if l.IsFunction(-1) {
			callbackCounter++
			callbackID = callbackCounter

			l.PushValue(-1)
			l.RawSetInt(lua.RegistryIndex, callbackID)
		}

		reqMethod := builder.method
		reqUrl := builder.url
		reqBody := builder.body
		reqHeaders := builder.headers.Clone()

		reqCookies := make([]*http.Cookie, len(builder.cookies))
		copy(reqCookies, builder.cookies)

		reqMetadata := make(map[string]any)
		maps.Copy(reqMetadata, builder.metadata)

		go func() {
			reqBodyBuffer := bytes.NewBuffer([]byte(reqBody))
			var resp *http.Response
			req, err := http.NewRequest(reqMethod, reqUrl.String(), reqBodyBuffer)
			if err == nil {
				req.Header = reqHeaders

				reqMetadata["request_builder"] = true
				reqMetadata["marasi_extension_id"] = extension.Data.ID.String()

				if len(reqMetadata) > 0 {
					if jsonBytes, err := json.Marshal(reqMetadata); err == nil {
						req.Header.Set("x-marasi-metadata", string(jsonBytes))
					}
				}

				for _, c := range reqCookies {
					req.AddCookie(c)
				}

				req.Header.Set("x-extension-id", extension.Data.ID.String())

				resp, err = builder.client.Do(req)

			}

			if callbackID != 0 {
				extension.Mu.Lock()
				defer extension.Mu.Unlock()

				l.RawGetInt(lua.RegistryIndex, callbackID)

				if !l.IsFunction(-1) {
					l.Pop(1)
					return
				}

				if err != nil {
					l.PushNil()
					l.PushString(err.Error())
				} else {
					bodyBytes, readErr := io.ReadAll(resp.Body)
					resp.Body.Close()

					resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

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
					l.Pop(1)
				}

				l.PushNil()
				l.RawSetInt(lua.RegistryIndex, callbackID)
			}

		}()
		return 0
	}

	// Register the RequestBuilder type and its methods in Lua
	RegisterType(extension.LuaState, "RequestBuilder", funcs, func(l *lua.State) int {
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
