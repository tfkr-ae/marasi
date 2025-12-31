package extensions

import (
	"slices"
	"strings"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
)

func registerStringsLibrary(l *lua.State) {
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	lua.NewLibrary(l, stringsLibrary())

	l.SetField(-2, "strings")

	l.Pop(1)
}

// stringsLibrary returns a list of Lua functions for string manipulation.
// These functions are available under the `marasi.strings` table in Lua scripts.
func stringsLibrary() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		// upper converts a string to uppercase.
		//
		// @param input string The string to convert.
		// @return string The uppercase string.
		{Name: "upper", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(strings.ToUpper(inputString))
			return 1
		}},
		// lower converts a string to lowercase.
		//
		// @param input string The string to convert.
		// @return string The lowercase string.
		{Name: "lower", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(strings.ToLower(inputString))
			return 1
		}},
		// reverse reverses a string.
		//
		// @param input string The string to reverse.
		// @return string The reversed string.
		{Name: "reverse", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			runes := []rune(inputString)
			slices.Reverse(runes)

			l.PushString(string(runes))
			return 1
		}},
		// len returns the length of a string.
		//
		// @param input string The string.
		// @return number The length of the string.
		{Name: "len", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushInteger(len(inputString))
			return 1
		}},
		// replace replaces all occurrences of a substring with another string.
		//
		// @param input string The original string.
		// @param target string The substring to replace.
		// @param replacement string The string to replace with.
		// @param n number (optional) The maximum number of replacements. -1 means all.
		// @return string The new string.
		{Name: "replace", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			target := lua.CheckString(l, 3)
			replacement := lua.OptString(l, 4, "")
			occurences := lua.OptInteger(l, 5, -1)

			l.PushString(strings.Replace(inputString, target, replacement, occurences))
			return 1
		}},
		// contains checks if a string contains a substring.
		//
		// @param input string The string to check.
		// @param subString string The substring to look for.
		// @return boolean True if the string contains the substring.
		{Name: "contains", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			subString := lua.CheckString(l, 3)

			l.PushBoolean(strings.Contains(inputString, subString))
			return 1
		}},
		// has_prefix checks if a string starts with a prefix.
		//
		// @param input string The string to check.
		// @param prefix string The prefix to look for.
		// @return boolean True if the string starts with the prefix.
		{Name: "has_prefix", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			prefix := lua.CheckString(l, 3)

			l.PushBoolean(strings.HasPrefix(inputString, prefix))
			return 1
		}},
		// has_suffix checks if a string ends with a suffix.
		//
		// @param input string The string to check.
		// @param suffix string The suffix to look for.
		// @return boolean True if the string ends with the suffix.
		{Name: "has_suffix", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			suffix := lua.CheckString(l, 3)

			l.PushBoolean(strings.HasSuffix(inputString, suffix))
			return 1
		}},
		// split splits a string by a separator.
		//
		// @param input string The string to split.
		// @param separator string The separator to split by.
		// @return table A table of the split parts.
		{Name: "split", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			separator := lua.CheckString(l, 3)

			parts := strings.Split(inputString, separator)
			util.DeepPush(l, parts)
			return 1
		}},
		// trim removes leading and trailing whitespace from a string.
		//
		// @param input string The string to trim.
		// @return string The trimmed string.
		{Name: "trim", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			l.PushString(strings.TrimSpace(inputString))
			return 1
		}},
		// substring returns a substring of a string.
		//
		// @param input string The original string.
		// @param start number The starting index (0-based).
		// @param end number (optional) The ending index.
		// @return string The substring.
		{Name: "substring", Function: func(l *lua.State) int {
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
