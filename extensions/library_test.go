package extensions

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Shopify/go-lua"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/domain"
)

func TestMarasiLog(t *testing.T) {
	t.Run("marasi:log should write to proxy log with correct extension ID", func(t *testing.T) {
		ext, mockProxy := setupTestExtension(t, "")

		var capturedLog *domain.Log

		mockProxy.WriteLogFunc = func(level, msg string, opts ...func(*domain.Log) error) error {
			log := &domain.Log{
				Message: msg,
				Level:   level,
			}
			for _, option := range opts {
				if err := option(log); err != nil {
					return err
				}
			}
			capturedLog = log
			return nil
		}

		luaCode := `marasi:log("hello from lua", "WARN")`
		err := ext.ExecuteLua(luaCode)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		if capturedLog == nil {
			t.Errorf("wanted:\nlog called\ngot:\nnil")
			return
		}

		if capturedLog.Message != "hello from lua" {
			t.Errorf("wanted:\n%q\ngot:\n%q", "hello from lua", capturedLog.Message)
		}

		if capturedLog.Level != "WARN" {
			t.Errorf("wanted:\n%q\ngot:\n%q", "WARN", capturedLog.Level)
		}

		if capturedLog.ExtensionID == nil {
			t.Errorf("wanted:\nextension ID set\ngot:\nnil")
			return
		}

		if *capturedLog.ExtensionID != ext.Data.ID {
			t.Errorf("wanted:\n%v\ngot:\n%v", ext.Data.ID, *capturedLog.ExtensionID)
		}
	})

	t.Run("marasi:log should default to INFO level if not provided", func(t *testing.T) {
		ext, mockProxy := setupTestExtension(t, "")
		var capturedLog *domain.Log

		mockProxy.WriteLogFunc = func(level, msg string, opts ...func(*domain.Log) error) error {
			capturedLog = &domain.Log{Level: level, Message: msg}
			return nil
		}

		err := ext.ExecuteLua(`marasi:log("default level check")`)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		if capturedLog.Level != "INFO" {
			t.Errorf("wanted:\nINFO\ngot:\n%q", capturedLog.Level)
		}
	})

	t.Run("marasi:log should return error string to lua if WriteLog fails", func(t *testing.T) {
		ext, mockProxy := setupTestExtension(t, "")

		mockProxy.WriteLogFunc = func(level, msg string, opts ...func(*domain.Log) error) error {
			return errors.New("log write failed")
		}

		luaCode := `
			local ok, res = pcall(marasi.log, marasi, "fail", "INFO")
			if ok then
				return "expected error"
			end
			return res
		`
		err := ext.ExecuteLua(luaCode)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		result := goValue(ext.LuaState, -1)
		errStr, ok := result.(string)
		if !ok {
			t.Fatalf("wanted:\nstring error\ngot:\n%T", result)
		}

		if !strings.Contains(errStr, "writing log : log write failed") {
			t.Errorf("wanted:\nerror containing 'writing log : log write failed'\ngot:\n%v", errStr)
		}
	})
}

func TestMarasiConfig(t *testing.T) {
	t.Run("marasi:config should return config directory path", func(t *testing.T) {
		ext, mockProxy := setupTestExtension(t, "")

		want := "/custom/config/marasi"
		mockProxy.GetConfigDirFunc = func() (string, error) {
			return want, nil
		}

		err := ext.ExecuteLua(`return marasi:config()`)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		got := goValue(ext.LuaState, -1)
		if got != want {
			t.Errorf("wanted:\n%q\ngot:\n%v", want, got)
		}
	})

	t.Run("marasi:config should return empty string on error", func(t *testing.T) {
		ext, mockProxy := setupTestExtension(t, "")

		mockProxy.GetConfigDirFunc = func() (string, error) {
			return "", errors.New("config error")
		}

		err := ext.ExecuteLua(`return marasi:config()`)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		got := goValue(ext.LuaState, -1)
		if got != "" {
			t.Errorf("wanted:\nempty string\ngot:\n%v", got)
		}
	})
}

func TestMarasiBuilder(t *testing.T) {
	t.Run("marasi:builder() should return a new empty RequestBuilder", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		err := ext.ExecuteLua(`return marasi:builder()`)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		got := goValue(ext.LuaState, -1)
		builder, ok := got.(*RequestBuilder)
		if !ok {
			t.Fatalf("\nwanted:\n*RequestBuilder\ngot:\n%T", got)
		}

		if builder.method != "" {
			t.Errorf("\nwanted:\nempty method\ngot:\n%q", builder.method)
		}
		if builder.body != "" {
			t.Errorf("\nwanted:\nempty body\ngot:\n%q", builder.body)
		}
		if builder.url.String() != "" {
			t.Errorf("\nwanted:\nempty url\ngot:\n%q", builder.url.String())
		}

		if len(builder.headers) != 0 {
			t.Errorf("\nwanted:\n0 headers\ngot:\n%d", len(builder.headers))
		}
		if len(builder.cookies) != 0 {
			t.Errorf("\nwanted:\n0 cookies\ngot:\n%d", len(builder.cookies))
		}
		if len(builder.metadata) != 0 {
			t.Errorf("\nwanted:\n0 metadata items\ngot:\n%d", len(builder.metadata))
		}
	})

	t.Run("marasi:builder(req) should initialize builder from request", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		bodyContent := `{"marasi":"app"}`
		req, _ := http.NewRequest("POST", "https://marasi.app/test", strings.NewReader(bodyContent))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Add("X-Custom", "Marasi")

		ext.LuaState.PushUserData(req)
		lua.SetMetaTableNamed(ext.LuaState, "req")
		ext.LuaState.SetGlobal("test_req")

		err := ext.ExecuteLua(`return marasi:builder(test_req)`)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		got := goValue(ext.LuaState, -1)
		builder, ok := got.(*RequestBuilder)
		if !ok {
			t.Fatalf("\nwanted:\n*RequestBuilder\ngot:\n%T", got)
		}

		if builder.method != "POST" {
			t.Errorf("\nwanted:\nPOST\ngot:\n%q", builder.method)
		}

		if builder.url.String() != "https://marasi.app/test" {
			t.Errorf("\nwanted:\nhttps://marasi.app/test\ngot:\n%q", builder.url.String())
		}

		if builder.body != bodyContent {
			t.Errorf("\nwanted:\n%q\ngot:\n%q", bodyContent, builder.body)
		}

		if builder.contentType != "application/json" {
			t.Errorf("\nwanted:\napplication/json\ngot:\n%q", builder.contentType)
		}

		if !reflect.DeepEqual(builder.headers, req.Header) {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", req.Header, builder.headers)
		}

		if !reflect.DeepEqual(builder.cookies, req.Cookies()) {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", req.Cookies(), builder.cookies)
		}
	})

	t.Run("marasi:builder() should error with invalid arguments", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		luaCode := `
			local ok, res = pcall(marasi.builder, marasi, "not-a-request-object")
			if ok then
				return "expected error"
			end
			return res
		`
		err := ext.ExecuteLua(luaCode)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		result := goValue(ext.LuaState, -1)
		errStr, ok := result.(string)
		if !ok {
			t.Fatalf("\nwanted:\nstring error\ngot:\n%T", result)
		}

		if !strings.Contains(errStr, "expected request object") {
			t.Errorf("\nwanted:\nerror containing 'expected request object'\ngot:\n%v", errStr)
		}
	})
}

func TestMarasiScope(t *testing.T) {
	t.Run("marasi:scope() should return the scope user data", func(t *testing.T) {
		ext, mockProxy := setupTestExtension(t, "")
		want := compass.NewScope(true)

		mockProxy.GetScopeFunc = func() (*compass.Scope, error) {
			return want, nil
		}

		script := `
			return marasi:scope()
		`
		err := ext.ExecuteLua(script)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		got := goValue(ext.LuaState, -1)
		if got == nil {
			t.Errorf("wanted:\nscope object\ngot:\nnil")
		}

		if got != want {
			t.Errorf("wanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("marasi:scope() should return nil and log error if GetScope fails", func(t *testing.T) {
		ext, mockProxy := setupTestExtension(t, "")

		mockProxy.GetScopeFunc = func() (*compass.Scope, error) {
			return nil, errors.New("scope error")
		}

		script := `
			local ok, res = pcall(marasi.scope, marasi)
			if ok then
				return "expected error"
			end
			return res
		`
		err := ext.ExecuteLua(script)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		result := goValue(ext.LuaState, -1)

		errStr, ok := result.(string)
		if !ok {
			t.Fatalf("wanted:\nstring error\ngot:\n%T", result)
		}

		if !strings.Contains(errStr, "getting scope : scope error") {
			t.Errorf("wanted:\nerror containing 'getting scope : scope error'\ngot:\n%v", errStr)
		}
	})

	t.Run("marasi:scope() interaction should modify core scope", func(t *testing.T) {
		ext, proxy := setupTestExtension(t, "")
		coreScope, _ := proxy.GetScope()

		script := `
			local s = marasi:scope()
			s:add_rule("marasi.app", "host")
		`
		err := ext.ExecuteLua(script)
		if err != nil {
			t.Fatalf("executing lua: %v", err)
		}

		if !coreScope.MatchesString("marasi.app", "host") {
			t.Errorf("wanted:\ntrue\ngot:\nfalse")
		}
	})
}
