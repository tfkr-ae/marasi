package extensions

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/url"
	"strings"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
)

// deepExpand recursively walks a value. If it finds a string that looks like
// a JSON object or array, it attempts to unmarshal it. If unmarshalling succeeds,
// it returns the expanded data; otherwise, it returns the original string.
func deepExpand(v any) any {
	switch val := v.(type) {
	case string:
		trimmed := strings.TrimSpace(val)
		if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
			(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
			var nested any
			err := json.Unmarshal([]byte(val), &nested)
			if err != nil {
				return val
			}
			return deepExpand(nested)
		}
		return val

	case map[string]any:
		for k, v := range val {
			val[k] = deepExpand(v)
		}
		return val

	case []any:
		for i, v := range val {
			val[i] = deepExpand(v)
		}
		return val

	default:
		return val
	}
}

func registerEncodingLibrary(extension *Runtime) {
	l := extension.LuaState
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	l.NewTable()

	register := func(name string, funcs []APIFunction) {
		newLibrary(extension, "marasi.encoding."+name, funcs)
		l.SetField(-2, name)
	}

	register("base64", base64Library())
	register("hex", hexLibrary())
	register("url", urlEncodeLibrary())
	register("html", htmlLibrary())
	register("json", jsonLibrary())

	l.SetField(-2, "encoding")
	l.Pop(1)
}

// base64Library returns a list of Lua functions for base64 encoding and
// decoding. These functions are available under the `marasi.encoding.base64`
// table in Lua scripts.
func base64Library() []APIFunction {
	return []APIFunction{
		{Name: "encode", Signature: "(input: string) -> string", Summary: "Encodes a string using base64.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			l.PushString(base64.StdEncoding.EncodeToString([]byte(inputString)))
			return 1
		}},
		{Name: "decode", Signature: "(input: string) -> string", Summary: "Decodes a base64 encoded string.", Function: func(l *lua.State) int {
			encodedString := lua.CheckString(l, 2)

			decoded, err := base64.StdEncoding.DecodeString(encodedString)
			if err != nil {
				lua.Errorf(l, "decoding base64 %s: %s", encodedString, err.Error())
				return 0
			}
			l.PushString(string(decoded))
			return 1
		}},
	}
}

// hexLibrary returns a list of Lua functions for hexadecimal encoding and
// decoding. These functions are available under the `marasi.encoding.hex`
// table in Lua scripts.
func hexLibrary() []APIFunction {
	return []APIFunction{
		{Name: "encode", Signature: "(input: string) -> string", Summary: "Encodes a string using hexadecimal.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(hex.EncodeToString([]byte(inputString)))
			return 1
		}},
		{Name: "decode", Signature: "(input: string) -> string", Summary: "Decodes a hexadecimal encoded string.", Function: func(l *lua.State) int {
			encodedString := lua.CheckString(l, 2)

			decoded, err := hex.DecodeString(encodedString)
			if err != nil {
				lua.Errorf(l, "decoding hex %s: %s", encodedString, err.Error())
				return 0
			}
			l.PushString(string(decoded))
			return 1
		}},
	}
}

// urlEncodeLibrary returns a list of Lua functions for URL encoding and
// decoding. These functions are available under the `marasi.encoding.url`
// table in Lua scripts.
func urlEncodeLibrary() []APIFunction {
	return []APIFunction{
		{Name: "encode", Signature: "(input: string) -> string", Summary: "Encodes a string for use in a URL query.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(url.QueryEscape(inputString))
			return 1
		}},
		{Name: "decode", Signature: "(input: string) -> string", Summary: "Decodes a URL encoded string.", Function: func(l *lua.State) int {
			encodedString := lua.CheckString(l, 2)

			decoded, err := url.QueryUnescape(encodedString)
			if err != nil {
				lua.Errorf(l, "decoding url %s: %s", encodedString, err.Error())
				return 0
			}
			l.PushString(decoded)
			return 1
		}},
	}
}

// htmlLibrary returns a list of Lua functions for HTML escaping and
// unescaping. These functions are available under the `marasi.encoding.html`
// table in Lua scripts.
func htmlLibrary() []APIFunction {
	return []APIFunction{
		{Name: "escape", Signature: "(input: string) -> string", Summary: "Escapes a string for use in HTML.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			l.PushString(html.EscapeString(inputString))
			return 1
		}},
		{Name: "unescape", Signature: "(input: string) -> string", Summary: "Unescapes an HTML escaped string.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			l.PushString(html.UnescapeString(inputString))
			return 1
		}},
	}
}

// jsonLibrary returns a list of Lua functions for JSON encoding and
// decoding. These functions are available under the `marasi.encoding.json`
// table in Lua scripts.
func jsonLibrary() []APIFunction {
	return []APIFunction{
		{Name: "encode", Signature: "(value: any, indent?: number) -> string", Summary: "Encodes a Lua value to a JSON string.", Function: func(l *lua.State) int {
			val := GoValue(l, 2)
			indent := lua.OptInteger(l, 3, 0)

			var jsonBytes []byte
			var err error

			if indent > 0 {
				jsonBytes, err = json.MarshalIndent(val, "", strings.Repeat(" ", indent))
			} else {
				jsonBytes, err = json.Marshal(val)
			}

			if err != nil {
				lua.Errorf(l, "marshalling json: %s", err.Error())
				return 0
			}

			l.PushString(string(jsonBytes))
			return 1
		}},
		{Name: "decode", Signature: "(input: string) -> any", Summary: "Decodes a JSON string to a Lua value. It also recursively decodes any nested JSON objects or arrays found within the string values of the initial JSON structure.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			var decoded any

			err := json.Unmarshal([]byte(inputString), &decoded)
			if err != nil {
				lua.Errorf(l, "unmarshalling json: %s", err.Error())
				return 0
			}

			decoded = deepExpand(decoded)

			util.DeepPush(l, decoded)
			return 1
		}},
	}
}
