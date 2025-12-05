package extensions

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Shopify/go-lua"
)

func TestRuntime_Sandbox(t *testing.T) {
	restrictedGlobals := []string{
		"os",
		"io",
		"dofile",
		"loadfile",
		"load",
		"loadstring",
		"require",
		"package",
		"debug",
		"collectgarbage",
		"string",
	}

	for _, global := range restrictedGlobals {
		t.Run(fmt.Sprintf("%s should be nil", global), func(t *testing.T) {
			ext, _ := setupTestExtension(t, "")

			luaCode := fmt.Sprintf(`
				if %s == nil then return "nil" end
				return "exists"
			`, global)

			err := ext.ExecuteLua(luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", luaCode, err)
			}

			val := goValue(ext.LuaState, -1)
			if val != "nil" {
				t.Errorf("\nwanted:\nnil\ngot:\n%v", val)
			}
		})
	}
}

func TestRuntime_LuaStandardLibraries(t *testing.T) {
	tests := []struct {
		name    string
		luaCode string
		want    any
	}{
		{
			name:    "math library should be available",
			luaCode: `return math.abs(-10)`,
			want:    10.0,
		},
		{
			name:    "table library should be available",
			luaCode: `local t = {1, 2, 3}; return table.concat(t, "-")`,
			want:    "1-2-3",
		},
		{
			name:    "bit32 library should be available",
			luaCode: `return bit32.band(10, 2)`,
			want:    2.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, _ := setupTestExtension(t, "")

			err := ext.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}

			got := goValue(ext.LuaState, -1)
			if got != tt.want {
				t.Errorf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}

func TestRuntime_ExecuteLua(t *testing.T) {
	t.Run("should execute valid lua code", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")
		err := ext.ExecuteLua(`print("hello")`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should return error on invalid lua code", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")
		err := ext.ExecuteLua(`invalid syntax`)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestRuntime_ShouldInterceptRequest(t *testing.T) {
	t.Run("should return true when interceptRequest returns true", func(t *testing.T) {
		luaCode := `
			function interceptRequest(req)
				return true
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		req, _ := http.NewRequest("GET", "https://marasi.app", nil)

		got, err := ext.ShouldInterceptRequest(req)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if !got {
			t.Errorf("\nwanted:\ntrue\ngot:\nfalse")
		}
	})

	t.Run("should return false when interceptRequest returns false", func(t *testing.T) {
		luaCode := `
			function interceptRequest(req)
				return false
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		req, _ := http.NewRequest("GET", "https://marasi.app", nil)

		got, err := ext.ShouldInterceptRequest(req)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got {
			t.Errorf("\nwanted:\nfalse\ngot:\ntrue")
		}
	})

	t.Run("should return error if interceptRequest fails", func(t *testing.T) {
		luaCode := `
			function interceptRequest(req)
				error("forced error")
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		req, _ := http.NewRequest("GET", "https://marasi.app", nil)

		got, err := ext.ShouldInterceptRequest(req)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
		if got {
			t.Errorf("\nwanted:\nfalse\ngot:\ntrue")
		}
	})
}

func TestRuntime_ShouldInterceptResponse(t *testing.T) {
	t.Run("should return true when interceptResponse returns true", func(t *testing.T) {
		luaCode := `
			function interceptResponse(res)
				return true
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		res := &http.Response{}

		got, err := ext.ShouldInterceptResponse(res)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if !got {
			t.Errorf("\nwanted:\ntrue\ngot:\nfalse")
		}
	})

	t.Run("should return false when interceptResponse returns false", func(t *testing.T) {
		luaCode := `
			function interceptResponse(res)
				return false
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		res := &http.Response{}

		got, err := ext.ShouldInterceptResponse(res)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got {
			t.Errorf("\nwanted:\nfalse\ngot:\ntrue")
		}
	})

	t.Run("should return error if interceptResponse fails", func(t *testing.T) {
		luaCode := `
			function interceptResponse(res)
				error("forced error")
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		res := &http.Response{}

		got, err := ext.ShouldInterceptResponse(res)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
		if got {
			t.Errorf("\nwanted:\nfalse\ngot:\ntrue")
		}
	})
}

func TestRuntime_CallRequestHandler(t *testing.T) {
	t.Run("should execute processRequest successfully", func(t *testing.T) {
		luaCode := `
			function processRequest(req)
				print("processRequest executed")
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		req, _ := http.NewRequest("GET", "https://marasi.app", nil)

		err := ext.CallRequestHandler(req)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(ext.Logs) != 1 {
			t.Fatalf("\nwanted:\n1 log\ngot:\n%d", len(ext.Logs))
		}

		if ext.Logs[0].Text != "processRequest executed" {
			t.Errorf("\nwanted:\nprocessRequest executed\ngot:\n%s", ext.Logs[0].Text)
		}
	})

	t.Run("should return error if processRequest fails", func(t *testing.T) {
		luaCode := `
			function processRequest(req)
				error("forced error")
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		req, _ := http.NewRequest("GET", "https://marasi.app", nil)

		err := ext.CallRequestHandler(req)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestRuntime_CallResponseHandler(t *testing.T) {
	t.Run("should execute processResponse successfully", func(t *testing.T) {
		luaCode := `
			function processResponse(res)
				print("processResponse executed")
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		res := &http.Response{}

		err := ext.CallResponseHandler(res)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(ext.Logs) != 1 {
			t.Fatalf("\nwanted:\n1 log\ngot:\n%d", len(ext.Logs))
		}

		if ext.Logs[0].Text != "processResponse executed" {
			t.Errorf("\nwanted:\nprocessResponse executed\ngot:\n%s", ext.Logs[0].Text)
		}
	})

	t.Run("should return error if processResponse fails", func(t *testing.T) {
		luaCode := `
			function processResponse(res)
				error("forced error")
			end
		`
		ext, _ := setupTestExtension(t, luaCode)
		res := &http.Response{}

		err := ext.CallResponseHandler(res)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestRuntime_GlobalFunctions(t *testing.T) {
	luaCode := `
		my_bool_true = true
		my_bool_false = false
		my_string = "hello world"
		my_number = 123
		function my_func() return true end
	`
	ext, _ := setupTestExtension(t, luaCode)

	t.Run("CheckGlobalFlag should only return true for boolean values", func(t *testing.T) {
		tests := []struct {
			globalName string
			want       bool
		}{
			{"my_bool_true", true},
			{"my_bool_false", false},
			{"my_string", false},
			{"non_existent", false},
			{"my_func", false},
		}

		for _, tt := range tests {
			got := ext.CheckGlobalFlag(tt.globalName)
			if got != tt.want {
				t.Errorf("\nwanted:\n%v = %t\ngot:\n%v", tt.globalName, tt.want, got)
			}
		}
	})

	t.Run("GetGlobalString should only string globals and error for non-strings", func(t *testing.T) {
		tests := []struct {
			globalName string
			want       string
		}{
			{"my_bool_true", ""},
			{"my_bool_false", ""},
			{"my_string", "hello world"},
			{"non_existent", ""},
			{"my_func", "test"},
		}

		for _, tt := range tests {
			got, err := ext.GetGlobalString(tt.globalName)
			if err == nil && got != tt.want {
				t.Errorf("\nwanted:\nerr: %v\ngot:\nnil", err)
				t.Errorf("\nwanted:\n%v = %q\ngot:\n%v", tt.globalName, tt.want, got)
			}
		}
	})

	t.Run("CheckGlobalFunction should only true for functions", func(t *testing.T) {
		tests := []struct {
			globalName string
			want       bool
		}{
			{"my_bool_true", false},
			{"my_bool_false", false},
			{"my_string", false},
			{"non_existent", false},
			{"my_func", true},
		}

		for _, tt := range tests {
			got := ext.CheckGlobalFunction(tt.globalName)
			if got != tt.want {
				t.Errorf("\nwanted:\n%v = %t\ngot:\n%v", tt.globalName, tt.want, got)
			}
		}
	})

}

func TestRuntime_MarasiModules(t *testing.T) {
	modules := []string{
		"marasi.log",
		"marasi.config",
		"marasi.scope",
		"marasi.builder",

		"marasi.strings",
		"marasi.crypto",
		"marasi.utils",
		"marasi.settings",
		"marasi.random",
		"marasi.encoding",

		"marasi.encoding.base64",
		"marasi.encoding.hex",
		"marasi.encoding.json",
		"marasi.encoding.url",
		"marasi.encoding.html",
	}

	for _, module := range modules {
		t.Run(fmt.Sprintf("%s should not be nil", module), func(t *testing.T) {
			ext, _ := setupTestExtension(t, "")

			luaCode := fmt.Sprintf(`
				if %s == nil then return "nil" end
				return "exists"
			`, module)

			err := ext.ExecuteLua(luaCode)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}

			val := goValue(ext.LuaState, -1)
			if val != "exists" {
				t.Errorf("\nwanted:\nexists\ngot:\n%v", val)
			}
		})
	}
}

func TestRuntime_CustomPrint(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got []ExtensionLog)
	}{
		{
			name:    "basic strings and numbers should log with tabs",
			luaCode: `print("hello", "marasi", 1234)`,
			validatorFunc: func(t *testing.T, got []ExtensionLog) {
				want := "hello\tmarasi\t1234"
				if len(got) != 1 {
					t.Fatalf("\nwanted:\n1 log\ngot:\n%d", len(got))
				}
				if want != got[0].Text {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, got[0].Text)
				}
			},
		},
		{
			name:    "printing nil value should print a 'nil' string and boolean should print string value",
			luaCode: `print(nil,true)`,
			validatorFunc: func(t *testing.T, got []ExtensionLog) {
				want := "nil\ttrue"
				if len(got) != 1 {
					t.Fatalf("\nwanted:\n1 log\ngot:\n%d", len(got))
				}
				if want != got[0].Text {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, got[0].Text)
				}
			},
		},
		{
			name: "print should use tostring for UserData",
			luaCode: `
				local c = marasi.utils:cookie("session_id", "marasi-1234")
				print(c)
			`,
			validatorFunc: func(t *testing.T, got []ExtensionLog) {
				want := "session_id=marasi-1234; Path=/"
				if len(got) != 1 {
					t.Fatalf("\nwanted:\n1 log\ngot:\n%d", len(got))
				}
				if want != got[0].Text {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, got[0].Text)
				}
			},
		},
		{
			name: "calling print multiple times should append to the ExtensionLog slice",
			luaCode: `
				print("test-marasi")
				print("test-2-marasi")
			`,
			validatorFunc: func(t *testing.T, got []ExtensionLog) {
				want := []ExtensionLog{
					{Text: "test-marasi"},
					{Text: "test-2-marasi"},
				}
				if len(got) != 2 {
					t.Fatalf("\nwanted:\n2 logs\ngot:\n%d", len(got))
				}

				if want[0].Text != got[0].Text {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want[0].Text, got[0].Text)
				}

				if want[1].Text != got[1].Text {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want[1].Text, got[1].Text)
				}
			},
		},
		{
			name: "print should add the correct timestamp",
			luaCode: `
				print("test-marasi")
			`,
			validatorFunc: func(t *testing.T, got []ExtensionLog) {
				want := ExtensionLog{
					Time: time.Now(),
				}
				if len(got) != 1 {
					t.Fatalf("\nwanted:\n1 log\ngot:\n%d", len(got))
				}

				diff := want.Time.Sub(got[0].Time)

				if diff < 0 || diff > 50*time.Millisecond {
					t.Fatalf("\nwanted:\n%v\ngot:\n%v", want.Time, got[0].Time)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, _ := setupTestExtension(t, "")
			onLogCalled := []ExtensionLog{}

			ext.OnLog = func(el ExtensionLog) error {
				onLogCalled = append(onLogCalled, el)
				return nil
			}
			err := ext.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, ext.Logs)
			}
			if len(onLogCalled) != len(ext.Logs) {
				t.Fatalf("\nwanted:\n%d onLog calls\ngot:\n%d onLog calls", len(onLogCalled), len(ext.Logs))
			}
		})
	}
}

func TestRuntime_HelperFunctions(t *testing.T) {
	t.Run("goValue should convert primitive lua types correctly", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		ext.LuaState.PushString("marasi")
		ext.LuaState.PushNumber(123.45)
		ext.LuaState.PushBoolean(true)
		ext.LuaState.PushNil()
		ext.LuaState.PushGoFunction(func(l *lua.State) int {
			return 0
		})

		if val := goValue(ext.LuaState, -5); val != "marasi" {
			t.Errorf("\nwanted:\nmarasi\ngot:\n%v", val)
		}
		if val := goValue(ext.LuaState, -4); val != 123.45 {
			t.Errorf("\nwanted:\n123.45\ngot:\n%v", val)
		}
		if val := goValue(ext.LuaState, -3); val != true {
			t.Errorf("\nwanted:\ntrue\ngot:\n%v", val)
		}
		if val := goValue(ext.LuaState, -2); val != nil {
			t.Errorf("\nwanted:\nnil\ngot:\n%v", val)
		}
		if val := goValue(ext.LuaState, -1); val != nil {
			t.Errorf("\nwanted:\nnil\ngot:\n%v", val)
		}
	})

	t.Run("goValue should return the same userdata", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		type marasiTestStruct struct {
			Data string
		}
		want := &marasiTestStruct{Data: "test-data"}
		ext.LuaState.PushUserData(want)

		got := goValue(ext.LuaState, -1)
		if want != got {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("parseTable should return a slice for a lua array", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		err := ext.ExecuteLua(`return {10, 20, 30}`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got := goValue(ext.LuaState, -1)
		want := []any{10.0, 20.0, 30.0}

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("parseTable should return a map[string]any for a lua table", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		err := ext.ExecuteLua(`return {key = "marasi", ver = 1}`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got := goValue(ext.LuaState, -1)
		want := map[string]any{
			"key": "marasi",
			"ver": 1.0,
		}

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("parseTable should return a slice for mixed tables", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")

		err := ext.ExecuteLua(`return {10, key="marasi"}`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got := goValue(ext.LuaState, -1)
		want := map[string]any{
			"1":   10.0,
			"key": "marasi",
		}

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("asMap should cast map[string]any to map[string]any", func(t *testing.T) {
		want := map[string]any{"a": 1}
		got := asMap(want)

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", want, got)
		}

	})

	t.Run("asMap should cast []any to map[string]any", func(t *testing.T) {
		want := map[string]any{}
		got := asMap([]any{})

		if got == nil {
			t.Fatalf("\nwanted:\n%#v\ngot:\nnil", want)
		}

		if !reflect.DeepEqual(want, got) {
			t.Errorf("\nwanted:\n%#v\ngot:\n%#v", want, got)
		}

	})

	t.Run("asMap should return nil for non empty slices", func(t *testing.T) {
		got := asMap([]any{1})

		if got != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%#v", got)
		}

	})

	t.Run("asMap should return nil for invalid types", func(t *testing.T) {
		got := asMap("marasi-test")

		if got != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%#v", got)
		}

	})

	t.Run("getExtensionID should return correct UUID", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "")
		want := ext.Data.ID

		got := getExtensionID(ext.LuaState)

		if want != got {
			t.Errorf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
}

func TestExtensionWithLogHandler(t *testing.T) {
	t.Run("should set the log handler", func(t *testing.T) {
		handler := func(log ExtensionLog) error { return nil }
		option := ExtensionWithLogHandler(handler)
		ext := &Runtime{}
		err := option(ext)

		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if ext.OnLog == nil {
			t.Errorf("\nwanted:\nhandler set\ngot:\nnil")
		}
	})

	t.Run("should return error if log handler is already set", func(t *testing.T) {
		handler := func(log ExtensionLog) error { return nil }
		option := ExtensionWithLogHandler(handler)
		ext := &Runtime{
			OnLog: handler,
		}
		err := option(ext)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "already has a log handler") {
			t.Errorf("\nwanted:\nerror containing 'already has a log handler'\ngot:\n%v", err)
		}
	})
}
