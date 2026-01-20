package extensions

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Shopify/go-lua"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/core"
)

func TestScopeType(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		setupScope    func() *compass.Scope
		validatorFunc func(t *testing.T, scope *compass.Scope, ext *Runtime, got any)
	}{
		{
			name: "scope:add_rule should add inclusion rule if '-' prefix is not found",
			luaCode: `
				local s = marasi:scope()
				s:add_rule("marasi\\.app", "host")
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				pattern, err := regexp.Compile("marasi\\.app")
				if err != nil {
					t.Fatalf("compiling regex")
				}
				want := map[string]compass.Rule{
					"marasi\\.app|host": {
						Pattern:   pattern,
						MatchType: "host",
					},
				}

				if len(scope.ExcludeRules) != 0 {
					t.Errorf("\nwanted:\n0 exclude rules\ngot:\n%d", len(scope.ExcludeRules))
				}

				if len(scope.IncludeRules) != 1 {
					t.Errorf("\nwanted:\n1 include rule\ngot:\n%d", len(scope.IncludeRules))
				}
				if !reflect.DeepEqual(want, scope.IncludeRules) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, scope.IncludeRules)
				}
			},
		},
		{
			name: "scope:add_rule should add exclusion rule if '-' prefix is found",
			luaCode: `
				local s = marasi:scope()
				s:add_rule("-marasi\\.app", "host")
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				pattern, err := regexp.Compile("marasi\\.app")
				if err != nil {
					t.Fatalf("compiling regex")
				}
				want := map[string]compass.Rule{
					"marasi\\.app|host": {
						Pattern:   pattern,
						MatchType: "host",
					},
				}

				if len(scope.IncludeRules) != 0 {
					t.Errorf("\nwanted:\n0 include rules\ngot:\n%d", len(scope.IncludeRules))
				}

				if len(scope.ExcludeRules) != 1 {
					t.Errorf("\nwanted:\n1 exclude rule\ngot:\n%d", len(scope.ExcludeRules))
				}
				if !reflect.DeepEqual(want, scope.ExcludeRules) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, scope.ExcludeRules)
				}
			},
		},
		{
			name: "scope:add_rule should raise an error if scope.AddRule errors",
			luaCode: `
				local s = marasi:scope()
				local ok, res = pcall(s.add_rule, s, "-marasi\\.app", "non-existent")
				if ok then
					return "expected error but got success"
				end
				return res
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				errString, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errString, "adding rule") {
					t.Errorf("\nwanted:\nerror message: %s\ngot:\n%s", "adding rule", errString)
				}
			},
		},
		{
			name: "scope:remove_rule should remove rule",
			luaCode: `
				local s = marasi:scope()
				s:add_rule("marasi\\.app", "host")
				s:add_rule("marasi\\.com", "host")
				s:remove_rule("marasi\\.app", "host")
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				pattern, err := regexp.Compile("marasi\\.com")
				if err != nil {
					t.Fatalf("compiling regex")
				}
				want := map[string]compass.Rule{
					"marasi\\.com|host": {
						Pattern:   pattern,
						MatchType: "host",
					},
				}

				if len(scope.ExcludeRules) != 0 {
					t.Errorf("\nwanted:\n0 exclude rules\ngot:\n%d", len(scope.ExcludeRules))
				}

				if len(scope.IncludeRules) != 1 {
					t.Errorf("\nwanted:\n1 include rule\ngot:\n%d", len(scope.IncludeRules))
				}
				if !reflect.DeepEqual(want, scope.IncludeRules) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, scope.IncludeRules)
				}
			},
		},
		{
			name: "scope:remove_rule should raise an error if scope.RemoveRule errors",
			luaCode: `
				local s = marasi:scope()
				local ok, res = pcall(s.remove_rule, s, "-marasi\\.app", "host")
				if ok then
					return "expected error but got success"
				end
				return res
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				errString, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errString, "removing rule") {
					t.Errorf("\nwanted:\nerror message: %s\ngot:\n%s", "removing rule", errString)
				}
			},
		},
		{
			name: "scope:matches should return true on successful match (host - request)",
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := httptest.NewRequest("GET", "https://marasi.app/path", nil)
					r.LuaState.PushUserData(req)
					lua.SetMetaTableNamed(r.LuaState, "req")
					r.LuaState.SetGlobal("test_req")
					return nil
				},
			},
			luaCode: `
				local s = marasi:scope()
				s:add_rule("marasi\\.app", "host")
				return s:matches(test_req)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}

				if !matched {
					t.Fatalf("\nwanted:\ntrue\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:matches should return true on successful match (url - request)",
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := httptest.NewRequest("GET", "https://marasi.app/admin/dashboard", nil)
					r.LuaState.PushUserData(req)
					lua.SetMetaTableNamed(r.LuaState, "req")
					r.LuaState.SetGlobal("test_req")
					return nil
				},
			},
			luaCode: `
				local s = marasi:scope()
				s:add_rule("admin", "url")
				return s:matches(test_req)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if !matched {
					t.Fatalf("\nwanted:\ntrue\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:matches should return false on mismatch (url - request) with default allow policy=false",
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := httptest.NewRequest("GET", "https://marasi.app/public/home", nil)
					r.LuaState.PushUserData(req)
					lua.SetMetaTableNamed(r.LuaState, "req")
					r.LuaState.SetGlobal("test_req")
					return nil
				},
			},
			luaCode: `
				local s = marasi:scope()
				s:add_rule("admin", "url")
				return s:matches(test_req)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if matched {
					t.Fatalf("\nwanted:\nfalse\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:matches should return true on successful match (host - response)",
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := httptest.NewRequest("GET", "https://marasi.app/path", nil)
					res := &http.Response{Request: req}
					r.LuaState.PushUserData(res)
					lua.SetMetaTableNamed(r.LuaState, "res")
					r.LuaState.SetGlobal("test_res")
					return nil
				},
			},
			luaCode: `
				local s = marasi:scope()
				s:add_rule("marasi\\.app", "host")
				return s:matches(test_res)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if !matched {
					t.Fatalf("\nwanted:\ntrue\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:matches should return true on successful match (url - response)",
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := httptest.NewRequest("GET", "https://marasi.app/admin/dashboard", nil)
					res := &http.Response{Request: req}
					r.LuaState.PushUserData(res)
					lua.SetMetaTableNamed(r.LuaState, "res")
					r.LuaState.SetGlobal("test_res")
					return nil
				},
			},
			luaCode: `
				local s = marasi:scope()
				s:add_rule("admin", "url")
				return s:matches(test_res)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if !matched {
					t.Fatalf("\nwanted:\ntrue\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:matches should return false on mismatch (url - response) with default allow policy=false",
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := httptest.NewRequest("GET", "https://marasi.app/public/home", nil)
					res := &http.Response{Request: req}
					r.LuaState.PushUserData(res)
					lua.SetMetaTableNamed(r.LuaState, "res")
					r.LuaState.SetGlobal("test_res")
					return nil
				},
			},
			luaCode: `
				local s = marasi:scope()
				s:add_rule("admin", "url")
				return s:matches(test_res)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if matched {
					t.Fatalf("\nwanted:\nfalse\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:matches should raise error on invalid input type",
			luaCode: `
				local s = marasi:scope()
				local ok, res = pcall(s.matches, s, "invalid-type")
				if ok then
					return "expected error but got success"
				end
				return res
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				errString, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}

				if !strings.Contains(errString, "expected request / response object") {
					t.Errorf("\nwanted error containing 'expected request / response object', got:\n%s", errString)
				}
			},
		},
		{
			name: "scope:matches_string should return true for matching host string",
			luaCode: `
				local s = marasi:scope()
				s:add_rule("marasi\\.app", "host")
				return s:matches_string("marasi.app", "host")
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if !matched {
					t.Fatalf("\nwanted:\ntrue\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:matches_string should return true for matching url string",
			luaCode: `
				local s = marasi:scope()
				s:add_rule("v1/api", "url")
				return s:matches_string("https://marasi.app/v1/api/users", "url")
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if !matched {
					t.Fatalf("\nwanted:\ntrue\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:set_default_allow should change default behavior to block",
			luaCode: `
				local s = marasi:scope()
				s:set_default_allow(false)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(true) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				if scope.DefaultAllow {
					t.Errorf("\nwanted:\nDefaultAllow false\ngot:\ntrue")
				}
			},
		},
		{
			name: "scope:set_default_allow should change default behavior to allow",
			luaCode: `
				local s = marasi:scope()
				s:set_default_allow(true)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				if !scope.DefaultAllow {
					t.Errorf("\nwanted:\nDefaultAllow true\ngot:\nfalse")
				}
			},
		},
		{
			name: "scope:set_default_allow should change default behavior to block",
			luaCode: `
				local s = marasi:scope()
				s:set_default_allow(false)
				-- No rules added, should return false (block)
				return s:matches_string("marasi.app", "host")
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(true) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				if scope.DefaultAllow {
					t.Errorf("\nwanted:\nDefaultAllow false\ngot:\ntrue")
				}

				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if matched {
					t.Fatalf("\nwanted:\nfalse (blocked)\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:set_default_allow should change default behavior to allow",
			luaCode: `
				local s = marasi:scope()
				s:set_default_allow(true)
				-- No rules added, should return true (allow)
				return s:matches_string("marasi.app", "host")
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				if !scope.DefaultAllow {
					t.Errorf("\nwanted:\nDefaultAllow true\ngot:\nfalse")
				}

				matched, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nboolean\ngot:\n%T", got)
				}
				if !matched {
					t.Fatalf("\nwanted:\ntrue (allowed)\ngot:\n%t", matched)
				}
			},
		},
		{
			name: "scope:clear_rules should remove all rules",
			luaCode: `
				local s = marasi:scope()
				s:add_rule("marasi\\.app", "host")
				s:add_rule("-marasi\\.com", "host")
				s:clear_rules()
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				if len(scope.IncludeRules) != 0 {
					t.Errorf("\nwanted:\n0 include rules\ngot:\n%d", len(scope.IncludeRules))
				}
				if len(scope.ExcludeRules) != 0 {
					t.Errorf("\nwanted:\n0 exclude rules\ngot:\n%d", len(scope.ExcludeRules))
				}
			},
		},
		{
			name: "scope:tostring should return formatted string representation",
			luaCode: `
				local s = marasi:scope()
				s:add_rule("marasi\\.app", "host")
				s:add_rule("-marasi\\.com", "url")
				return tostring(s)
			`,
			setupScope: func() *compass.Scope { return compass.NewScope(false) },
			validatorFunc: func(t *testing.T, scope *compass.Scope, ext *Runtime, got any) {
				want := "Scope (Default: Block)\n  Include Rules:\n    - marasi\\.app (host)\n  Exclude Rules:\n    - marasi\\.com (url)"
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				if want != str {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, mockProxy := setupTestExtension(t, "", tt.options...)
			scope := tt.setupScope()
			mockProxy.GetScopeFunc = func() (*compass.Scope, error) {
				return scope, nil
			}

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, scope, extension, got)
			}
		})
	}
}

func TestRegexType(t *testing.T) {
	withRegex := func(pattern string) func(*Runtime) error {
		return func(r *Runtime) error {
			re := regexp.MustCompile(pattern)
			r.LuaState.PushUserData(re)
			lua.SetMetaTableNamed(r.LuaState, "regexp")
			r.LuaState.SetGlobal("re")
			return nil
		}
	}

	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		validatorFunc func(t *testing.T, ext *Runtime, got any)
	}{
		{
			name:    "regexp:match should return true for match",
			luaCode: `return re:match("marasi")`,
			options: []func(*Runtime) error{
				withRegex(`marasi`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != true {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "regexp:match should return false for no match",
			luaCode: `return re:match("nomatch")`,
			options: []func(*Runtime) error{
				withRegex(`marasi`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != false {
					t.Errorf("\nwanted:\nfalse\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "regexp:is_anchored_match should match full string only",
			luaCode: `return re:is_anchored_match("marasi"), re:is_anchored_match("marasi-proxy")`,
			options: []func(*Runtime) error{
				withRegex(`marasi`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				matchPartial := got.(bool)
				matchExact := GoValue(ext.LuaState, -2).(bool)

				if matchExact != true {
					t.Errorf("\nwanted:\ntrue (full match)\ngot:\nfalse")
				}
				if matchPartial != false {
					t.Errorf("\nwanted:\nfalse (partial match)\ngot:\ntrue")
				}
			},
		},
		{
			name:    "regexp:find should return the leftmost match text",
			luaCode: `return re:find("hello marasi world")`,
			options: []func(*Runtime) error{
				withRegex(`m[a-z]+i`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if str != "marasi" {
					t.Errorf("\nwanted:\nmarasi\ngot:\n%q", str)
				}
			},
		},
		{
			name:    "regexp:find should return empty string for no match",
			luaCode: `return re:find("nothing here")`,
			options: []func(*Runtime) error{
				withRegex(`marasi`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if str != "" {
					t.Errorf("\nwanted:\nempty string\ngot:\n%q", str)
				}
			},
		},
		{
			name:    "regexp:find_all should return slice of all matches",
			luaCode: `return re:find_all("cat bat rat")`,
			options: []func(*Runtime) error{
				withRegex(`[a-z]at`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				arr, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice\ngot:\n%T", got)
				}
				want := []any{"cat", "bat", "rat"}
				if !reflect.DeepEqual(arr, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, arr)
				}
			},
		},
		{
			name:    "regexp:find_all should return nil/empty for no match",
			luaCode: `return re:find_all("nothing here")`,
			options: []func(*Runtime) error{
				withRegex(`[0-9]+`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Errorf("\nwanted:\nnil\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "regexp:split should split string by pattern",
			luaCode: `return re:split("a,b,c")`,
			options: []func(*Runtime) error{
				withRegex(`,`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				arr, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice\ngot:\n%T", got)
				}
				want := []any{"a", "b", "c"}
				if !reflect.DeepEqual(arr, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, arr)
				}
			},
		},
		{
			name:    "regexp:replace should replace text",
			luaCode: `return re:replace("marasi 1.0", "2.0")`,
			options: []func(*Runtime) error{
				withRegex(`1\.0`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "marasi 2.0" {
					t.Errorf("\nwanted:\nmarasi 2.0\ngot:\n%q", got)
				}
			},
		},
		{
			name:    "regexp:pattern should return the regex string",
			luaCode: `return re:pattern()`,
			options: []func(*Runtime) error{
				withRegex(`^test$`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "^test$" {
					t.Errorf("\nwanted:\n^test$\ngot:\n%q", got)
				}
			},
		},
		{
			name:    "regexp:find_submatch should return match and capture groups",
			luaCode: `return re:find_submatch("key=value")`,
			options: []func(*Runtime) error{
				withRegex(`(\w+)=(\w+)`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				arr, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice\ngot:\n%T", got)
				}
				want := []any{"key=value", "key", "value"}
				if !reflect.DeepEqual(arr, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, arr)
				}
			},
		},
		{
			name:    "regexp:find_submatch should return nil/empty for no match",
			luaCode: `return re:find_submatch("nothing here")`,
			options: []func(*Runtime) error{
				withRegex(`(\d+)`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Errorf("\nwanted:\nnil\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "regexp:find_submatch_indices should return capture group indices",
			luaCode: `return re:find_submatch_indices("marasi")`,
			options: []func(*Runtime) error{
				withRegex(`ma(ra)si`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				arr, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice\ngot:\n%T", got)
				}
				want := []any{0.0, 6.0, 2.0, 4.0}
				if !reflect.DeepEqual(arr, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, arr)
				}
			},
		},
		{
			name:    "regexp:find_named_submatch should return map of named groups",
			luaCode: `return re:find_named_submatch("2025-10-20")`,
			options: []func(*Runtime) error{
				withRegex(`(?P<year>\d{4})-(?P<month>\d{2})-(?P<day>\d{2})`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				m := asMap(got)
				if m == nil {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
				if m["year"] != "2025" || m["month"] != "10" || m["day"] != "20" {
					t.Errorf("\nwanted:\nyear=2025, month=10, day=20\ngot:\n%v", m)
				}
			},
		},
		{
			name:    "regexp:find_all_submatches should return nested slices of all matches",
			luaCode: `return re:find_all_submatches("a=1 b=2")`,
			options: []func(*Runtime) error{
				withRegex(`(\w+)=(\d)`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				outer, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice of slices\ngot:\n%T", got)
				}

				if len(outer) != 2 {
					t.Fatalf("\nwanted:\n2 matches\ngot:\n%d", len(outer))
				}

				match1, ok1 := outer[0].([]any)
				match2, ok2 := outer[1].([]any)

				if !ok1 || !ok2 {
					t.Fatalf("\nwanted:\ninner items to be slices\ngot:\nsomething else")
				}

				want1 := []any{"a=1", "a", "1"}
				want2 := []any{"b=2", "b", "2"}

				if !reflect.DeepEqual(match1, want1) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want1, match1)
				}
				if !reflect.DeepEqual(match2, want2) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want2, match2)
				}
			},
		},
		{
			name:    "regexp:tostring should return string representation",
			luaCode: `return tostring(re)`,
			options: []func(*Runtime) error{
				withRegex(`^test$`),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				want := "Regexp { Pattern: ^test$, Subexpressions: 0 }"
				if got != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "", tt.options...)

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension, got)
			}
		})
	}
}

func TestCookieType(t *testing.T) {
	withCookie := func(cookie *http.Cookie) func(*Runtime) error {
		return func(r *Runtime) error {
			r.LuaState.PushUserData(cookie)
			lua.SetMetaTableNamed(r.LuaState, "cookie")
			r.LuaState.SetGlobal("c")
			return nil
		}
	}

	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		validatorFunc func(t *testing.T, ext *Runtime, got any)
	}{
		{
			name:    "cookie:name should return the cookie name",
			luaCode: `return c:name()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Name: "session_id"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "session_id" {
					t.Errorf("\nwanted:\nsession_id\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_name should update the cookie name",
			luaCode: `c:set_name("new_session"); return c:name()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Name: "old_session"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "new_session" {
					t.Errorf("\nwanted:\nnew_session\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:value should return the cookie value",
			luaCode: `return c:value()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Value: "xyz123"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "xyz123" {
					t.Errorf("\nwanted:\nxyz123\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_value should update the cookie value",
			luaCode: `c:set_value("abc987"); return c:value()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Value: "xyz123"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "abc987" {
					t.Errorf("\nwanted:\nabc987\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:domain should return the domain",
			luaCode: `return c:domain()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Domain: "marasi.app"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "marasi.app" {
					t.Errorf("\nwanted:\nmarasi.app\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_domain should update the domain",
			luaCode: `c:set_domain("api.marasi.app"); return c:domain()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Domain: "marasi.app"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "api.marasi.app" {
					t.Errorf("\nwanted:\napi.marasi.app\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:path should return the path",
			luaCode: `return c:path()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Path: "/auth"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "/auth" {
					t.Errorf("\nwanted:\n/auth\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_path should update the path",
			luaCode: `c:set_path("/admin"); return c:path()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Path: "/"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "/admin" {
					t.Errorf("\nwanted:\n/admin\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:secure should return true for secure cookies",
			luaCode: `return c:secure()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Secure: true}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != true {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_secure should update the secure flag",
			luaCode: `c:set_secure(true); return c:secure()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Secure: false}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != true {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:http_only should return true for HttpOnly cookies",
			luaCode: `return c:http_only()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{HttpOnly: true}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != true {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_http_only should update the HttpOnly flag",
			luaCode: `c:set_http_only(false); return c:http_only()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{HttpOnly: true}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != false {
					t.Errorf("\nwanted:\nfalse\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:max_age should return the MaxAge",
			luaCode: `return c:max_age()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{MaxAge: 3600}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != 3600.0 {
					t.Errorf("\nwanted:\n3600\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_max_age should update the MaxAge",
			luaCode: `c:set_max_age(7200); return c:max_age()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{MaxAge: 3600}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != 7200.0 {
					t.Errorf("\nwanted:\n7200\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:same_site should return 'lax' for LaxMode",
			luaCode: `return c:same_site()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{SameSite: http.SameSiteLaxMode}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "lax" {
					t.Errorf("\nwanted:\nlax\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:same_site should return 'strict' for StrictMode",
			luaCode: `return c:same_site()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{SameSite: http.SameSiteStrictMode}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "strict" {
					t.Errorf("\nwanted:\nstrict\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:same_site should return 'none' for NoneMode",
			luaCode: `return c:same_site()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{SameSite: http.SameSiteNoneMode}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "none" {
					t.Errorf("\nwanted:\nnone\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_same_site should update SameSite mode",
			luaCode: `c:set_same_site("strict"); return c:same_site()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{SameSite: http.SameSiteLaxMode}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "strict" {
					t.Errorf("\nwanted:\nstrict\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_same_site should default to DefaultMode for unknown strings",
			luaCode: `c:set_same_site("invalid_mode"); return c:same_site()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{SameSite: http.SameSiteLaxMode}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "default" {
					t.Errorf("\nwanted:\ndefault\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:expires should return nil if zero time",
			luaCode: `return c:expires()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Errorf("\nwanted:\nnil\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:expires should return timestamp",
			luaCode: `return c:expires()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Expires: time.Unix(1700000000, 0)}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != 1700000000.0 {
					t.Errorf("\nwanted:\n1700000000\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:set_expires should update expiration time",
			luaCode: `c:set_expires(1750000000); return c:expires()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != 1750000000.0 {
					t.Errorf("\nwanted:\n1750000000\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:serialize should return the cookie string",
			luaCode: `return c:serialize()`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Name: "foo", Value: "bar"}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "foo=bar" {
					t.Errorf("\nwanted:\nfoo=bar\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "cookie:tostring should return serialized string",
			luaCode: `return tostring(c)`,
			options: []func(*Runtime) error{
				withCookie(&http.Cookie{Name: "session", Value: "123", Path: "/admin", SameSite: http.SameSiteLaxMode, Secure: true, HttpOnly: true}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				want := "session=123; Path=/admin; HttpOnly; Secure; SameSite=Lax"
				if got != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "", tt.options...)

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension, got)
			}
		})
	}
}

func TestHeaderType(t *testing.T) {
	withHeader := func(h http.Header) func(*Runtime) error {
		return func(r *Runtime) error {
			headerCopy := h.Clone()
			r.LuaState.PushUserData(&headerCopy)
			lua.SetMetaTableNamed(r.LuaState, "header")
			r.LuaState.SetGlobal("h")
			return nil
		}
	}

	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		validatorFunc func(t *testing.T, ext *Runtime, got any)
	}{
		{
			name:    "header:get should return value",
			luaCode: `return h:get("Content-Type")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"Content-Type": {"application/json"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "application/json" {
					t.Errorf("\nwanted:\napplication/json\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "header:get should return nil if key missing",
			luaCode: `return h:get("X-Missing")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Errorf("\nwanted:\nnil\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "header:set should overwrite value",
			luaCode: `h:set("X-Custom", "new-value"); return h:get("X-Custom")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"X-Custom": {"old-value"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "new-value" {
					t.Errorf("\nwanted:\nnew-value\ngot:\n%v", got)
				}
			},
		},
		{
			name: "header:set should error on empty key",
			luaCode: `
				local ok, res = pcall(h.set, h, "", "val")
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withHeader(http.Header{}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "header key cannot be empty") {
					t.Errorf("\nwanted error containing 'header key cannot be empty'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name:    "header:add should append value",
			luaCode: `h:add("X-List", "item2"); return h:values("X-List")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"X-List": {"item1"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				arr, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice\ngot:\n%T", got)
				}
				want := []any{"item1", "item2"}
				if !reflect.DeepEqual(arr, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, arr)
				}
			},
		},
		{
			name: "header:add should error on empty key",
			luaCode: `
				local ok, res = pcall(h.add, h, "", "val")
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withHeader(http.Header{}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "header key cannot be empty") {
					t.Errorf("\nwanted error containing 'header key cannot be empty'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name:    "header:delete should remove key",
			luaCode: `h:delete("X-Remove"); return h:has("X-Remove")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"X-Remove": {"val"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != false {
					t.Errorf("\nwanted:\nfalse\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "header:has should return true if exists",
			luaCode: `return h:has("X-Exists")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"X-Exists": {"true"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != true {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "header:values should return all values",
			luaCode: `return h:values("Accept")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"Accept": {"text/html", "application/json"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				arr, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice\ngot:\n%T", got)
				}
				want := []any{"text/html", "application/json"}
				if !reflect.DeepEqual(arr, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, arr)
				}
			},
		},
		{
			name:    "header:values should return nil or empty list for non-existent key",
			luaCode: `return h:values("Non-Existent-Key")`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"Existing": {"val"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Fatalf("\nwanted:\nnil\ngot:\n%T", got)
				}
			},
		},
		{
			name:    "header:to_table should convert header to table",
			luaCode: `return h:to_table()`,
			options: []func(*Runtime) error{
				withHeader(http.Header{
					"Content-Type": {"text/plain"},
					"X-Flag":       {"1", "2"},
				}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				m := asMap(got)
				if m == nil {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}

				ct, ok := m["Content-Type"].([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice for Content-Type\ngot:\n%T", m["Content-Type"])
				}
				if len(ct) != 1 || ct[0] != "text/plain" {
					t.Errorf("\nwanted:\n[text/plain]\ngot:\n%v", ct)
				}

				xf, ok := m["X-Flag"].([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice for X-Flag\ngot:\n%T", m["X-Flag"])
				}
				wantFlag := []any{"1", "2"}
				if !reflect.DeepEqual(xf, wantFlag) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", wantFlag, xf)
				}
			},
		},
		{
			name:    "header:tostring should return formatted string",
			luaCode: `return tostring(h)`,
			options: []func(*Runtime) error{
				withHeader(http.Header{"User-Agent": {"Marasi/1.0"}}),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				want := "User-Agent: Marasi/1.0"
				if got != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "", tt.options...)

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension, got)
			}
		})
	}
}

func TestURLType(t *testing.T) {
	withURL := func(rawURL string) func(*Runtime) error {
		return func(r *Runtime) error {
			u, err := url.Parse(rawURL)
			if err != nil {
				return err
			}
			r.LuaState.PushUserData(u)
			lua.SetMetaTableNamed(r.LuaState, "url")
			r.LuaState.SetGlobal("u")
			return nil
		}
	}

	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		validatorFunc func(t *testing.T, ext *Runtime, got any)
	}{
		{
			name:    "url:string should return full url",
			luaCode: `return u:string()`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app/path?q=1"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "https://marasi.app/path?q=1" {
					t.Errorf("\nwanted:\nhttps://marasi.app/path?q=1\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:scheme should return scheme",
			luaCode: `return u:scheme()`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "https" {
					t.Errorf("\nwanted:\nhttps\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:set_scheme should update scheme",
			luaCode: `u:set_scheme("http"); return u:scheme()`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "http" {
					t.Errorf("\nwanted:\nhttp\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:host should return host",
			luaCode: `return u:host()`,
			options: []func(*Runtime) error{
				withURL("https://api.marasi.app"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "api.marasi.app" {
					t.Errorf("\nwanted:\napi.marasi.app\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:set_host should update host",
			luaCode: `u:set_host("new.marasi.app"); return u:host()`,
			options: []func(*Runtime) error{
				withURL("https://old.marasi.app"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "new.marasi.app" {
					t.Errorf("\nwanted:\nnew.marasi.app\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:path should return path",
			luaCode: `return u:path()`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app/v1/api"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "/v1/api" {
					t.Errorf("\nwanted:\n/v1/api\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:set_path should update path",
			luaCode: `u:set_path("/v2/api"); return u:path()`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app/v1/api"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "/v2/api" {
					t.Errorf("\nwanted:\n/v2/api\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:query should return raw query",
			luaCode: `return u:query()`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app?search=test&page=1"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "search=test&page=1" {
					t.Errorf("\nwanted:\nsearch=test&page=1\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:set_query should update raw query",
			luaCode: `u:set_query("foo=bar"); return u:query()`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app?old=val"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "foo=bar" {
					t.Errorf("\nwanted:\nfoo=bar\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:get_param should return specific parameter",
			luaCode: `return u:get_param("id")`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app?id=123&type=admin"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "123" {
					t.Errorf("\nwanted:\n123\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:get_param should return nil if parameter missing",
			luaCode: `return u:get_param("missing")`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app?id=123"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Errorf("\nwanted:\nnil\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:set_param should add/update parameter",
			luaCode: `u:set_param("new_key", "new_val"); return u:get_param("new_key")`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "new_val" {
					t.Errorf("\nwanted:\nnew_val\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:del_param should remove parameter",
			luaCode: `u:del_param("remove_me"); return u:get_param("remove_me")`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app?remove_me=true"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Errorf("\nwanted:\nnil\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "url:tostring should return full url string",
			luaCode: `return tostring(u)`,
			options: []func(*Runtime) error{
				withURL("https://marasi.app/test"),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "https://marasi.app/test" {
					t.Errorf("\nwanted:\nhttps://marasi.app/test\ngot:\n%v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "", tt.options...)

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension, got)
			}
		})
	}
}

func TestRequestType(t *testing.T) {
	withRequest := func(req *http.Request) func(*Runtime) error {
		return func(r *Runtime) error {
			id, _ := uuid.NewV7()
			req = core.ContextWithRequestID(req, id)
			req = core.ContextWithMetadata(req, make(map[string]any))

			r.LuaState.PushUserData(req)
			lua.SetMetaTableNamed(r.LuaState, "req")
			r.LuaState.SetGlobal("r")
			return nil
		}
	}

	basicReq := func() *http.Request {
		req := httptest.NewRequest("GET", "https://marasi.app/path?q=1", strings.NewReader("body content"))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("User-Agent", "Go-Test")
		return req
	}

	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		validatorFunc func(t *testing.T, ext *Runtime, got any)
	}{
		{
			name:    "req:method should return method",
			luaCode: `return r:method()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "GET" {
					t.Errorf("\nwanted:\nGET\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "req:url should return url userdata",
			luaCode: `return r:url():string()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				want := "https://marasi.app/path?q=1"
				if got != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%v", want, got)
				}
			},
		},
		{
			name:    "req:path should return path",
			luaCode: `return r:path()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "/path" {
					t.Errorf("\nwanted:\n/path\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "req:host should return host",
			luaCode: `return r:host()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "marasi.app" {
					t.Errorf("\nwanted:\nmarasi.app\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "req:scheme should return correct scheme",
			luaCode: `return r:scheme()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if str != "https" {
					t.Errorf("\nwanted:\nhttps\ngot:\n%v", str)
				}
			},
		},
		{
			name:    "req:proto should return correct protocol",
			luaCode: `return r:proto()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if str != "HTTP/1.1" {
					t.Errorf("\nwanted:\nHTTP/1.1\ngot:\n%v", str)
				}
			},
		},
		{
			name:    "req:remote_addr should return correct remote address",
			luaCode: `return r:remote_addr()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				wantPrefix := "192.0.2.1"
				if !strings.HasPrefix(str, wantPrefix) {
					t.Errorf("\nwanted prefix:\n%s\ngot:\n%v", wantPrefix, str)
				}
			},
		},
		{
			name:    "req:set_host should update host and metadata",
			luaCode: `r:set_host("new.marasi.app"); return r:host()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "new.marasi.app" {
					t.Errorf("\nwanted:\nnew.marasi.app\ngot:\n%v", got)
				}

				ext.LuaState.Global("r")
				req := ext.LuaState.ToUserData(-1).(*http.Request)
				ext.LuaState.Pop(1)

				meta, _ := core.MetadataFromContext(req.Context())
				if meta["original_host_header"] != "marasi.app" {
					t.Errorf("\nwanted:\noriginal_host_header: marasi.app\ngot:\n%v", meta["original_host_header"])
				}
				if meta["override_host_header"] != "new.marasi.app" {
					t.Errorf("\nwanted:\noverride_host_header: new.marasi.app\ngot:\n%v", meta["override_host_header"])
				}
			},
		},
		{
			name:    "req:body should return body content",
			luaCode: `return r:body()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "body content" {
					t.Errorf("\nwanted:\nbody content\ngot:\n%v", got)
				}
			},
		},
		{
			name: "req:body should error if reading fails",
			luaCode: `
				local ok, res = pcall(r.body, r)
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := basicReq()
					req.Body = io.NopCloser(&erroringReader{})
					return withRequest(req)(r)
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "reading body : forced error") {
					t.Errorf("\nwanted:\nerror containing 'reading body : forced error'\ngot:\n%q", errStr)
				}
			},
		},
		{
			name:    "req:set_body should update body content",
			luaCode: `r:set_body("new body"); return r:body()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "new body" {
					t.Errorf("\nwanted:\nnew body\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "req:headers should return headers object",
			luaCode: `return r:headers():get("User-Agent")`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "Go-Test" {
					t.Errorf("\nwanted:\nGo-Test\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "req:content_type should return content type",
			luaCode: `return r:content_type()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "text/plain" {
					t.Errorf("\nwanted:\ntext/plain\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "req:cookies should return table of cookies",
			luaCode: `return r:cookies()`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := basicReq()
					req.AddCookie(&http.Cookie{Name: "c1", Value: "v1"})
					return withRequest(req)(r)
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				// got is []any of *http.Cookie userdata
				cookies, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\n[]any\ngot:\n%T", got)
				}
				if len(cookies) != 1 {
					t.Errorf("\nwanted:\n1 cookie\ngot:\n%d", len(cookies))
				}
			},
		},
		{
			name:    "req:cookie should return specific cookie",
			luaCode: `return r:cookie("c1"):value()`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := basicReq()
					req.AddCookie(&http.Cookie{Name: "c1", Value: "v1"})
					return withRequest(req)(r)
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "v1" {
					t.Errorf("\nwanted:\nv1\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "req:set_cookie should add cookie",
			luaCode: `r:set_cookie(marasi.utils:cookie("new", "val")); return r:cookie("new"):value()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "val" {
					t.Errorf("\nwanted:\nval\ngot:\n%v", got)
				}
			},
		},
		{
			name: "req:set_cookies should set cookies from table",
			luaCode: `
				local c1 = marasi.utils:cookie("c1", "v1")
				local c2 = marasi.utils:cookie("c2", "v2")
				r:set_cookies({c1, c2})
				return r:cookie("c1"):value(), r:cookie("c2"):value()
			`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				val2 := got.(string)
				ext.LuaState.Pop(1)
				val1 := GoValue(ext.LuaState, -1).(string)

				if val1 != "v1" || val2 != "v2" {
					t.Errorf("\nwanted:\nv1, v2\ngot:\n%s, %s", val1, val2)
				}
			},
		},
		{
			name: "req:set_cookies should error if argument is not a table",
			luaCode: `
				local ok, res = pcall(r.set_cookies, r, "not-a-table")
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "expected table") {
					t.Errorf("\nwanted:\nerror containing 'expected table'\ngot:\n%q", errStr)
				}
			},
		},
		{
			name:    "req:metadata should return metadata map",
			luaCode: `return r:metadata()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				m := asMap(got)
				if m == nil {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
			},
		},
		{
			name:    "req:set_metadata should update metadata for extension",
			luaCode: `r:set_metadata({flag = true})`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				ext.LuaState.Global("r")
				req := ext.LuaState.ToUserData(-1).(*http.Request)
				ext.LuaState.Pop(1)

				meta, _ := core.MetadataFromContext(req.Context())
				extMeta, ok := meta[ext.Data.Name].(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmetadata for extension %s\ngot:\nmissing", ext.Data.Name)
				}
				if val, ok := extMeta["flag"].(bool); !ok || !val {
					t.Errorf("\nwanted:\nflag=true in metadata\ngot:\n%v", val)
				}
			},
		},
		{
			name: "req:set_metadata should error if table values are mixed/array",
			luaCode: `
				local ok, res = pcall(r.set_metadata, r, {1, 2})
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "metadata must be a key-value table") {
					t.Errorf("\nwanted:\nerror containing 'metadata must be a key-value table'\ngot:\n%q", errStr)
				}
			},
		},
		{
			name:    "req:drop should set drop flag",
			luaCode: `r:drop()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				ext.LuaState.Global("r")
				req := ext.LuaState.ToUserData(-1).(*http.Request)
				ext.LuaState.Pop(1)

				if dropped, _ := core.DroppedFlagFromContext(req.Context()); !dropped {
					t.Errorf("\nwanted:\ndropped=true\ngot:\n%v", dropped)
				}
			},
		},
		{
			name:    "req:skip should set skip flag",
			luaCode: `r:skip()`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				ext.LuaState.Global("r")
				req := ext.LuaState.ToUserData(-1).(*http.Request)
				ext.LuaState.Pop(1)

				if skipped, _ := core.SkipFlagFromContext(req.Context()); !skipped {
					t.Errorf("\nwanted:\nskipped=true\ngot:\n%v", skipped)
				}
			},
		},
		{
			name:    "req:tostring should return formatted string",
			luaCode: `return tostring(r)`,
			options: []func(*Runtime) error{
				withRequest(basicReq()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				if !strings.HasPrefix(str, "Request { ID:") {
					t.Errorf("\nwanted prefix:\nRequest { ID:\ngot:\n%s", str)
				}
				if !strings.Contains(str, "Method: GET") {
					t.Errorf("\nwanted:\nMethod: GET\ngot:\n%s", str)
				}
				if !strings.Contains(str, "URL: https://marasi.app/path?q=1") {
					t.Errorf("\nwanted:\nURL: https://marasi.app/path?q=1\ngot:\n%s", str)
				}
				if !strings.Contains(str, "Proto: HTTP/1.1") {
					t.Errorf("\nwanted:\nProto: HTTP/1.1\ngot:\n%s", str)
				}
				if !strings.Contains(str, "Remote: 192.0.2.1:1234") {
					t.Errorf("\nwanted:\nRemote: 192.0.2.1:1234\ngot:\n%s", str)
				}
				if !strings.Contains(str, "Length: 12") {
					t.Errorf("\nwanted:\nLength: 12\ngot:\n%s", str)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "", tt.options...)

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension, got)
			}
		})
	}
}
func TestResponseType(t *testing.T) {
	withResponse := func(res *http.Response) func(*Runtime) error {
		return func(r *Runtime) error {
			id, _ := uuid.NewV7()
			req := res.Request
			if req == nil {
				req = httptest.NewRequest("GET", "https://marasi.app", nil)
				res.Request = req
			}

			req = core.ContextWithRequestID(req, id)
			req = core.ContextWithMetadata(req, make(map[string]any))
			res.Request = req

			r.LuaState.PushUserData(res)
			lua.SetMetaTableNamed(r.LuaState, "res")
			r.LuaState.SetGlobal("r")
			return nil
		}
	}

	basicRes := func() *http.Response {
		req := httptest.NewRequest("GET", "https://marasi.app/path?q=1", nil)
		res := &http.Response{
			StatusCode:    200,
			Status:        "200 OK",
			Proto:         "HTTP/1.1",
			ContentLength: 12,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("body content")),
			Request:       req,
		}
		res.Header.Set("Content-Type", "text/plain")
		res.Header.Set("Server", "Marasi-Test")
		return res
	}

	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		validatorFunc func(t *testing.T, ext *Runtime, got any)
	}{
		{
			name:    "res:id should return request id",
			luaCode: `return r:id()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				idStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if _, err := uuid.Parse(idStr); err != nil {
					t.Errorf("\nwanted:\nvalid uuid\ngot:\n%s", idStr)
				}
			},
		},
		{
			name:    "res:method should return request method",
			luaCode: `return r:method()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "GET" {
					t.Errorf("\nwanted:\nGET\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:url should return url userdata",
			luaCode: `return r:url():string()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				want := "https://marasi.app/path?q=1"
				if got != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%v", want, got)
				}
			},
		},
		{
			name:    "res:status should return status string",
			luaCode: `return r:status()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "200 OK" {
					t.Errorf("\nwanted:\n200 OK\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:status_code should return status code",
			luaCode: `return r:status_code()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != 200.0 {
					t.Errorf("\nwanted:\n200\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:set_status_code should update status code and string",
			luaCode: `r:set_status_code(404); return r:status(), r:status_code()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				code := got.(float64)
				ext.LuaState.Pop(1)
				status := GoValue(ext.LuaState, -1).(string)

				if code != 404.0 {
					t.Errorf("\nwanted:\n404\ngot:\n%v", code)
				}
				if status != "404 Not Found" {
					t.Errorf("\nwanted:\n404 Not Found\ngot:\n%v", status)
				}
			},
		},
		{
			name:    "res:length should return content length",
			luaCode: `return r:length()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != 12.0 {
					t.Errorf("\nwanted:\n12\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:body should return body content",
			luaCode: `return r:body()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "body content" {
					t.Errorf("\nwanted:\nbody content\ngot:\n%v", got)
				}
			},
		},
		{
			name: "res:body should error if reading fails",
			luaCode: `
				local ok, res = pcall(r.body, r)
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					res := basicRes()
					res.Body = io.NopCloser(&erroringReader{})
					return withResponse(res)(r)
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "reading body : forced error") {
					t.Errorf("\nwanted:\nerror containing 'reading body : forced error'\ngot:\n%q", errStr)
				}
			},
		},
		{
			name:    "res:set_body should update body content",
			luaCode: `r:set_body("new body"); return r:body()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "new body" {
					t.Errorf("\nwanted:\nnew body\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:headers should return headers object",
			luaCode: `return r:headers():get("Server")`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "Marasi-Test" {
					t.Errorf("\nwanted:\nMarasi-Test\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:content_type should return content type",
			luaCode: `return r:content_type()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "text/plain" {
					t.Errorf("\nwanted:\ntext/plain\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:cookies should return table of cookies",
			luaCode: `return r:cookies()`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					res := basicRes()
					res.Header.Add("Set-Cookie", (&http.Cookie{Name: "c1", Value: "v1"}).String())
					return withResponse(res)(r)
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				cookies, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\n[]any\ngot:\n%T", got)
				}
				if len(cookies) != 1 {
					t.Errorf("\nwanted:\n1 cookie\ngot:\n%d", len(cookies))
				}
			},
		},
		{
			name:    "res:cookie should return specific cookie",
			luaCode: `return r:cookie("c1"):value()`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					res := basicRes()
					res.Header.Add("Set-Cookie", (&http.Cookie{Name: "c1", Value: "v1"}).String())
					return withResponse(res)(r)
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "v1" {
					t.Errorf("\nwanted:\nv1\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "res:set_cookie should add Set-Cookie header",
			luaCode: `r:set_cookie(marasi.utils:cookie("new", "val")); return r:cookie("new"):value()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "val" {
					t.Errorf("\nwanted:\nval\ngot:\n%v", got)
				}
			},
		},
		{
			name: "res:set_cookies should set cookies from table",
			luaCode: `
				local c1 = marasi.utils:cookie("c1", "v1")
				local c2 = marasi.utils:cookie("c2", "v2")
				r:set_cookies({c1, c2})
				return r:cookie("c1"):value(), r:cookie("c2"):value()
			`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				val2 := got.(string)
				ext.LuaState.Pop(1)
				val1 := GoValue(ext.LuaState, -1).(string)

				if val1 != "v1" || val2 != "v2" {
					t.Errorf("\nwanted:\nv1, v2\ngot:\n%s, %s", val1, val2)
				}
			},
		},
		{
			name: "res:set_cookies should error if argument is not a table",
			luaCode: `
				local ok, res = pcall(r.set_cookies, r, "not-a-table")
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "expected table") {
					t.Errorf("\nwanted:\nerror containing 'expected table'\ngot:\n%q", errStr)
				}
			},
		},
		{
			name:    "res:metadata should return metadata map",
			luaCode: `return r:metadata()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				m := asMap(got)
				if m == nil {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
			},
		},
		{
			name:    "res:set_metadata should update metadata for extension",
			luaCode: `r:set_metadata({flag = true})`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				ext.LuaState.Global("r")
				res := ext.LuaState.ToUserData(-1).(*http.Response)
				ext.LuaState.Pop(1)

				meta, _ := core.MetadataFromContext(res.Request.Context())
				extMeta, ok := meta[ext.Data.Name].(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmetadata for extension %s\ngot:\nmissing", ext.Data.Name)
				}
				if val, ok := extMeta["flag"].(bool); !ok || !val {
					t.Errorf("\nwanted:\nflag=true in metadata\ngot:\n%v", val)
				}
			},
		},
		{
			name: "res:set_metadata should error if table values are mixed/array",
			luaCode: `
				local ok, res = pcall(r.set_metadata, r, {1, 2})
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "metadata must be a key-value table") {
					t.Errorf("\nwanted:\nerror containing 'metadata must be a key-value table'\ngot:\n%q", errStr)
				}
			},
		},
		{
			name:    "res:drop should set drop flag on request context",
			luaCode: `r:drop()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				ext.LuaState.Global("r")
				res := ext.LuaState.ToUserData(-1).(*http.Response)
				ext.LuaState.Pop(1)

				if dropped, _ := core.DroppedFlagFromContext(res.Request.Context()); !dropped {
					t.Errorf("\nwanted:\ndropped=true\ngot:\n%v", dropped)
				}
			},
		},
		{
			name:    "res:skip should set skip flag on request context",
			luaCode: `r:skip()`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				ext.LuaState.Global("r")
				res := ext.LuaState.ToUserData(-1).(*http.Response)
				ext.LuaState.Pop(1)

				if skipped, _ := core.SkipFlagFromContext(res.Request.Context()); !skipped {
					t.Errorf("\nwanted:\nskipped=true\ngot:\n%v", skipped)
				}
			},
		},
		{
			name:    "res:tostring should return formatted string",
			luaCode: `return tostring(r)`,
			options: []func(*Runtime) error{
				withResponse(basicRes()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				if !strings.HasPrefix(str, "Response { ID:") {
					t.Errorf("\nwanted prefix:\nResponse { ID:\ngot:\n%s", str)
				}
				if !strings.Contains(str, "Status: 200 OK (200)") {
					t.Errorf("\nwanted:\nStatus: 200 OK (200)\ngot:\n%s", str)
				}
				if !strings.Contains(str, "Proto: HTTP/1.1") {
					t.Errorf("\nwanted:\nProto: HTTP/1.1\ngot:\n%s", str)
				}
				if !strings.Contains(str, "Length: 12") {
					t.Errorf("\nwanted:\nLength: 12\ngot:\n%s", str)
				}
			},
		},
		{
			name: "res:request should return the associated request object",
			luaCode: `
				local req = r:request()
				if req == nil then return "got nil" end
				return req:method(), req:url():string(), req:body()
			`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					req := httptest.NewRequest("POST", "https://marasi.app/submit", strings.NewReader("request payload"))
					res := &http.Response{
						Request: req,
						Header:  make(http.Header),
						Body:    io.NopCloser(strings.NewReader("response")),
					}
					return withResponse(res)(r)
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				body := got.(string)
				ext.LuaState.Pop(1)
				url := GoValue(ext.LuaState, -1).(string)
				ext.LuaState.Pop(1)
				method := GoValue(ext.LuaState, -1).(string)

				if method != "POST" {
					t.Errorf("\nwanted:\nPOST\ngot:\n%v", method)
				}
				if url != "https://marasi.app/submit" {
					t.Errorf("\nwanted:\nhttps://marasi.app/submit\ngot:\n%v", url)
				}
				if body != "request payload" {
					t.Errorf("\nwanted:\nrequest payload\ngot:\n%v", body)
				}
			},
		},
		{
			name:    "res:request should return nil if no request is associated",
			luaCode: `return r:request()`,
			options: []func(*Runtime) error{
				func(r *Runtime) error {
					res := &http.Response{
						Header: make(http.Header),
						Body:   io.NopCloser(strings.NewReader("")),
					}

					r.LuaState.PushUserData(res)
					lua.SetMetaTableNamed(r.LuaState, "res")
					r.LuaState.SetGlobal("r")
					return nil
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != nil {
					t.Errorf("\nwanted:\nnil\ngot:\n%v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "", tt.options...)

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension, got)
			}
		})
	}
}

func TestRequestBuilderType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Body", string(body))
		w.Header().Set("X-Echo-Method", r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("server response"))
	}))
	defer server.Close()

	withBuilder := func(client *http.Client) func(*Runtime) error {
		return func(r *Runtime) error {
			builder := NewRequestBuilder(client)
			r.LuaState.PushUserData(builder)
			lua.SetMetaTableNamed(r.LuaState, "RequestBuilder")
			r.LuaState.SetGlobal("b")
			return nil
		}
	}

	asyncResultCh := make(chan string, 1)
	tests := []struct {
		name          string
		luaCode       string
		options       []func(*Runtime) error
		validatorFunc func(t *testing.T, ext *Runtime, got any)
	}{
		{
			name:    "b:method should return empty string by default",
			luaCode: `return b:method()`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "" {
					t.Errorf("\nwanted:\nempty string\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "b:set_method should update method and support chaining",
			luaCode: `return b:set_method("POST"):method()`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "POST" {
					t.Errorf("\nwanted:\nPOST\ngot:\n%v", got)
				}
			},
		},
		{
			name: "b:set_method should error on empty method",
			luaCode: `
				local ok, res = pcall(b.set_method, b, "")
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "HTTP method cannot be empty") {
					t.Errorf("\nwanted:\nerror containing 'HTTP method cannot be empty'\ngot:\n%q", errStr)
				}
			},
		},
		{
			name:    "b:url should return url userdata",
			luaCode: `b:set_url("https://marasi.app"); return b:url():string()`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "https://marasi.app" {
					t.Errorf("\nwanted:\nhttps://marasi.app\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "b:set_url should accept string",
			luaCode: `b:set_url("https://marasi.app/api"); return b:url():path()`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "/api" {
					t.Errorf("\nwanted:\n/api\ngot:\n%v", got)
				}
			},
		},
		{
			name: "b:set_url should accept url userdata",
			luaCode: `
				local u = marasi.utils:url("https://marasi.app/test")
				b:set_url(u)
				return b:url():string()
			`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "https://marasi.app/test" {
					t.Errorf("\nwanted:\nhttps://marasi.app/test\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "b:set_body should update body content",
			luaCode: `b:set_body("custom payload"); return b:body()`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "custom payload" {
					t.Errorf("\nwanted:\ncustom payload\ngot:\n%v", got)
				}
			},
		},
		{
			name:    "b:add_header should add header value",
			luaCode: `b:add_header("X-Custom", "value1"); return b:headers():get("X-Custom")`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "value1" {
					t.Errorf("\nwanted:\nvalue1\ngot:\n%v", got)
				}
			},
		},
		{
			name: "b:set_headers should replace headers from a header object",
			luaCode: `
				-- Create a temporary builder to get a header object
				local h = marasi.builder():headers()
				h:set("X-Bulk-Set", "true")
				
				-- Apply it to our main builder 'b'
				b:set_headers(h)
				return b:headers():get("X-Bulk-Set")
			`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "true" {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", got)
				}
			},
		},
		{
			name: "b:set_cookie should add cookie",
			luaCode: `
				local c = marasi.utils:cookie("session", "123")
				b:set_cookie(c)
				return b:cookies()[1]:value()
			`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "123" {
					t.Errorf("\nwanted:\n123\ngot:\n%v", got)
				}
			},
		},
		{
			name: "b:set_cookies should replace cookies with new table",
			luaCode: `
				local c1 = marasi.utils:cookie("session", "abc")
				local c2 = marasi.utils:cookie("theme", "dark")
				b:set_cookies({c1, c2})
				
				local list = b:cookies()
				return #list, list[1]:value(), list[2]:value()
			`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				// Stack return order: count, val1, val2 (top)
				val2, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring for cookie 2\ngot:\n%T", got)
				}

				ext.LuaState.Pop(1)
				val1 := GoValue(ext.LuaState, -1).(string)

				ext.LuaState.Pop(1)
				count := GoValue(ext.LuaState, -1).(float64)

				if count != 2 {
					t.Errorf("\nwanted:\n2\ngot:\n%v", count)
				}
				if val1 != "abc" {
					t.Errorf("\nwanted:\nabc\ngot:\n%v", val1)
				}
				if val2 != "dark" {
					t.Errorf("\nwanted:\ndark\ngot:\n%v", val2)
				}
			},
		},
		{
			name:    "b:set_metadata should set metadata map",
			luaCode: `b:set_metadata({origin="test"}); return b:metadata()["origin"]`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				if got != "test" {
					t.Errorf("\nwanted:\ntest\ngot:\n%v", got)
				}
			},
		},
		{
			name: "b:send should execute request and return response userdata",
			luaCode: fmt.Sprintf(`
				b:set_method("POST")
				b:set_url("%s")
				b:set_body("test_body")
				b:add_header("X-Test", "true")
				local res, err = b:send()
				if err then error(err) end
				return res:body(), res:headers():get("X-Echo-Method")
			`, server.URL),
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				method := got.(string)
				ext.LuaState.Pop(1)
				body := GoValue(ext.LuaState, -1).(string)

				if body != "server response" {
					t.Errorf("\nwanted:\nserver response\ngot:\n%s", body)
				}
				if method != "POST" {
					t.Errorf("\nwanted:\nPOST\ngot:\n%s", method)
				}
			},
		},
		{
			name: "b:send_async should execute request asynchronously",
			luaCode: fmt.Sprintf(`
                b:set_method("GET")
                b:set_url("%s")
                b:send_async(function(res, err)
                    -- Call the Go function registered in options
                    test_done(res and res:body() or err)
                end)
            `, server.URL),
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
				func(r *Runtime) error {
					r.LuaState.Register("test_done", func(l *lua.State) int {
						res := lua.CheckString(l, 1)
						asyncResultCh <- res
						return 0
					})
					return nil
				},
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				select {
				case res := <-asyncResultCh:
					if res != "server response" {
						t.Errorf("wanted 'server response', got %q", res)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("timed out waiting for async callback")
				}
			},
		},
		{
			name: "b:send should error if method or url are missing",
			luaCode: `
				local ok, res = pcall(b.send, b)
				if ok then return "expected error" end
				return res
			`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "method and url must be set") {
					t.Errorf("\nwanted:\nerror containing 'method and url must be set'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name:    "b:tostring should return formatted structure",
			luaCode: `b:set_method("GET"); b:set_url("https://marasi.app"); return tostring(b)`,
			options: []func(*Runtime) error{
				withBuilder(server.Client()),
			},
			validatorFunc: func(t *testing.T, ext *Runtime, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if !strings.Contains(str, "Method:GET") {
					t.Errorf("\nwanted:\nMethod:GET\ngot:\n%s", str)
				}
				if !strings.Contains(str, "URL:https://marasi.app") {
					t.Errorf("\nwanted:\nURL:https://marasi.app\ngot:\n%s", str)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "", tt.options...)

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, extension, got)
			}
		})
	}
}
