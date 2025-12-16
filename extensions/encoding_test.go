package extensions

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestJSONLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "encoding.json:encode should encode tables to json string",
			luaCode: `return marasi.encoding.json:encode({key = "Marasi", ver = 123, flags = {1,2,3}})`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				var data map[string]any
				if err := json.Unmarshal([]byte(str), &data); err != nil {
					t.Fatalf("\nwanted:\nvalid json\ngot error:\n%v", err)
				}

				want := make(map[string]any)
				want["key"] = "Marasi"
				want["ver"] = 123.0
				want["flags"] = []any{1.0, 2.0, 3.0}
				if !reflect.DeepEqual(want, data) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, str)
				}
			},
		},
		{
			name:    "encoding.json:encode should support indentation",
			luaCode: `return marasi.encoding.json:encode({key="marasi"}, 2)`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				want := "{\n  \"key\": \"marasi\"\n}"
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name:    "encoding.json:decode should decode JSON string to table",
			luaCode: `return marasi.encoding.json:decode('{"key": "marasi", "ver": 123.0, "flags": [1,2,3]}')`,
			validatorFunc: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				want := make(map[string]any)
				want["key"] = "marasi"
				want["ver"] = 123.0
				want["flags"] = []any{1.0, 2.0, 3.0}
				if !reflect.DeepEqual(m, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, m)
				}
			},
		},
		{
			name:    "encoding.json:decode should decode JSON array to slice",
			luaCode: `return marasi.encoding.json:decode('["marasi", 123.0, {"key": "marasi-app"}]')`,
			validatorFunc: func(t *testing.T, got any) {
				m, ok := got.([]any)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				want := []any{"marasi", 123.0, map[string]any{"key": "marasi-app"}}
				if !reflect.DeepEqual(m, want) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, m)
				}
			},
		},
		{
			name:    "encoding.json:decode should recursively expand nested json strings",
			luaCode: `return marasi.encoding.json:decode('{"meta": "{\\"nested\\": true, \\"array\\": \\"[1, 2]\\"}"}')`,
			validatorFunc: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
				meta, ok := m["meta"].(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmap (for meta)\ngot:\n%T", m["meta"])
				}
				if meta["nested"] != true {
					t.Errorf("\nwanted:\ntrue\ngot:\n%v", meta["nested"])
				}
				arr, ok := meta["array"].([]any)
				if !ok {
					t.Fatalf("\nwanted:\nslice (for array)\ngot:\n%T", meta["array"])
				}
				if len(arr) != 2 {
					t.Errorf("\nwanted:\nlength 2\ngot:\n%d", len(arr))
				}
			},
		},
		{
			name:    "encoding.json:decode should strictly preserve non-JSON strings",
			luaCode: `return marasi.encoding.json:decode('{"id": "12345", "malformed": "{abc", "not_obj": "true"}')`,
			validatorFunc: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
				id, ok := m["id"].(string)
				if !ok {
					t.Errorf("\nwanted:\nstring (for id)\ngot:\n%T", m["id"])
				}
				if id != "12345" {
					t.Errorf("\nwanted:\n'12345'\ngot:\n%v", id)
				}
				mal, ok := m["malformed"].(string)
				if !ok {
					t.Errorf("\nwanted:\nstring (for malformed)\ngot:\n%T", m["malformed"])
				}
				if mal != "{abc" {
					t.Errorf("\nwanted:\n'{abc'\ngot:\n%v", mal)
				}
			},
		},
		{
			name: "encoding.json:decode should return error on invalid json",
			luaCode: `
				local ok, res = pcall(marasi.encoding.json.decode, marasi.encoding.json, '{"bad": json}')
				if ok then
					return "expected nil result"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "invalid character") {
					t.Errorf("\nwanted:\nerror containing 'invalid character'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := goValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}

func TestHTMLEscapeLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "encoding.html:escape should escape input correctly",
			luaCode: "return marasi.encoding.html:escape('<p>Marasi Escaped</p>')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := html.EscapeString("<p>Marasi Escaped</p>")
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name:    "encoding.html:unescape should unescape input correctly",
			luaCode: "return marasi.encoding.html:unescape('&lt;p&gt;Marasi Escaped&lt;/p&gt;')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := "<p>Marasi Escaped</p>"
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := goValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}

func TestURLEncodeLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "encoding.url:encode should encode input correctly",
			luaCode: "return marasi.encoding.url:encode('marasi escaped?')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := url.QueryEscape("marasi escaped?")
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name:    "encoding.url:decode should decode input correctly",
			luaCode: "return marasi.encoding.url:decode('marasi+escaped%3F')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := "marasi escaped?"
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name: "encoding.url:decode should return error on invalid input",
			luaCode: `
				local ok, res = pcall(marasi.encoding.url.decode, marasi.encoding.url, "%")
				if ok then
					return "expected nil result"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "invalid URL escape") {
					t.Errorf("\nwanted:\nerror containing 'invalid URL escape'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := goValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}

func TestHexlibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "encoding.hex:encode should encode input correctly",
			luaCode: "return marasi.encoding.hex:encode('marasi')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := hex.EncodeToString([]byte("marasi"))
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name:    "encoding.hex:decode should decode input correctly",
			luaCode: "return marasi.encoding.hex:decode('6d61726173695f6465636f646564')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := "marasi_decoded"
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name: "encoding.hex:decode should return error on invalid input",
			luaCode: `
				local ok, res = pcall(marasi.encoding.hex.decode, marasi.encoding.hex, "invalid-hex")
				if ok then
					return "expected nil result"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "invalid byte") {
					t.Errorf("\nwanted:\nerror containing 'invalid byte'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := goValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}
func TestBase64Library(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "encoding.base64:encode should encode input correctly",
			luaCode: "return marasi.encoding.base64:encode('marasi')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := base64.StdEncoding.EncodeToString([]byte("marasi"))
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name:    "encoding.base64:decode should decode input correctly",
			luaCode: "return marasi.encoding.base64:decode('bWFyYXNpX2RlY29kZWQ=')",
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := "marasi_decoded"
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name: "encoding.base64:decode should return error on invalid input",
			luaCode: `
				local ok, res = pcall(marasi.encoding.base64.decode, marasi.encoding.base64, "invalid-base64")
				if ok then
					return "expected nil result"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)

				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}

				if !strings.Contains(errStr, "illegal base64 data") {
					t.Errorf("\nwanted:\nerror containing 'illegal base64 data'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := goValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}
