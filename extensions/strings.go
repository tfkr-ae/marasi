package extensions

import (
	"slices"
	"strings"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
)

func registerStringsLibrary(extension *Runtime) {
	l := extension.LuaState
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	extension.newLibrary("marasi.strings", stringsLibrary())

	l.SetField(-2, "strings")

	l.Pop(1)
}

// stringsLibrary returns a list of Lua functions for string manipulation.
// These functions are available under the `marasi.strings` table in Lua scripts.
func stringsLibrary() []APIFunction {
	return []APIFunction{
		{Name: "upper", Signature: "(input: string) -> string", Summary: "Converts a string to uppercase.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(strings.ToUpper(inputString))
			return 1
		}},
		{Name: "lower", Signature: "(input: string) -> string", Summary: "Converts a string to lowercase.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(strings.ToLower(inputString))
			return 1
		}},
		{Name: "reverse", Signature: "(input: string) -> string", Summary: "Reverses a string.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			runes := []rune(inputString)
			slices.Reverse(runes)

			l.PushString(string(runes))
			return 1
		}},
		{Name: "len", Signature: "(input: string) -> integer", Summary: "Returns the length of a string in bytes.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushInteger(len(inputString))
			return 1
		}},
		{Name: "replace", Signature: "(input: string, target: string, replacement?: string, n?: integer) -> string", Summary: "Replaces the first n occurrences of target, or every occurrence when n is omitted. replacement defaults to an empty string.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			target := lua.CheckString(l, 3)
			replacement := lua.OptString(l, 4, "")
			occurences := lua.OptInteger(l, 5, -1)

			l.PushString(strings.Replace(inputString, target, replacement, occurences))
			return 1
		}},
		{Name: "contains", Signature: "(input: string, subString: string) -> boolean", Summary: "Checks if a string contains a substring.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			subString := lua.CheckString(l, 3)

			l.PushBoolean(strings.Contains(inputString, subString))
			return 1
		}},
		{Name: "has_prefix", Signature: "(input: string, prefix: string) -> boolean", Summary: "Checks if a string starts with a prefix.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			prefix := lua.CheckString(l, 3)

			l.PushBoolean(strings.HasPrefix(inputString, prefix))
			return 1
		}},
		{Name: "has_suffix", Signature: "(input: string, suffix: string) -> boolean", Summary: "Checks if a string ends with a suffix.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			suffix := lua.CheckString(l, 3)

			l.PushBoolean(strings.HasSuffix(inputString, suffix))
			return 1
		}},
		{Name: "split", Signature: "(input: string, separator: string) -> table", Summary: "Splits a string by a separator.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			separator := lua.CheckString(l, 3)

			parts := strings.Split(inputString, separator)
			util.DeepPush(l, parts)
			return 1
		}},
		{Name: "trim", Signature: "(input: string) -> string", Summary: "Removes leading and trailing whitespace from a string.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(strings.TrimSpace(inputString))
			return 1
		}},
		{Name: "substring", Signature: "(input: string, start: integer, end?: integer) -> string", Summary: "Returns the characters from start up to, but not including, end. Indices count characters from 0, unlike Lua's string.sub. end defaults to the length in characters.", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			runes := []rune(inputString)
			lenRunes := len(runes)

			start := lua.CheckInteger(l, 3)
			end := lua.OptInteger(l, 4, lenRunes)

			if start < 0 {
				start = 0
			} else if start > lenRunes {
				start = lenRunes
			}

			if end < start {
				end = start
			}
			if end > lenRunes {
				end = lenRunes
			}

			l.PushString(string(runes[start:end]))
			return 1
		}},
	}
}
