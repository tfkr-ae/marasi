package extensions

import (
	"crypto/rand"
	"math/big"

	"github.com/Shopify/go-lua"
)

func registerRandomLibrary(l *lua.State) {
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	lua.NewLibrary(l, randomLibrary())

	l.SetField(-2, "random")

	l.Pop(1)
}

// randomLibrary returns a list of Lua functions that provide random data
// generation functionalities. These functions are available under the `marasi.random`
// table in Lua scripts.
func randomLibrary() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		// int returns a random integer in a given range.
		//
		// @param min int The minimum value of the range.
		// @param max int The maximum value of the range.
		// @return int A random integer between min and max (inclusive).
		{Name: "int", Function: func(l *lua.State) int {
			min := lua.CheckInteger(l, 2)
			max := lua.CheckInteger(l, 3)

			if min > max {
				lua.ArgumentError(l, 2, "minimum value cannot be greater than max")
				return 0
			}
			minBig := big.NewInt(int64(min))
			maxBig := big.NewInt(int64(max))

			diff := new(big.Int).Sub(maxBig, minBig)
			diff.Add(diff, big.NewInt(1))

			n, err := rand.Int(rand.Reader, diff)
			if err != nil {
				lua.Errorf(l, "generating random int: %s", err.Error())
				return 0
			}

			resultBig := new(big.Int).Add(n, minBig)

			l.PushInteger(int(resultBig.Int64()))
			return 1
		}},
		// string returns a random string of a given length, using an optional charset.
		//
		// @param length int The length of the random string.
		// @param charset string (optional) The set of characters to use for the random
		// string. Defaults to alphanumeric characters.
		// @return string The generated random string.
		{Name: "string", Function: func(l *lua.State) int {
			length := lua.CheckInteger(l, 2)
			charset := lua.OptString(l, 3, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

			if length <= 0 {
				l.PushString("")
				return 1
			}

			if len(charset) == 0 {
				lua.ArgumentError(l, 3, "charset cannot be empty")
				return 0
			}

			result := make([]byte, length)
			charsetLen := big.NewInt(int64(len(charset)))

			for i := range length {
				num, err := rand.Int(rand.Reader, charsetLen)
				if err != nil {
					lua.Errorf(l, "generating random int: %s", err.Error())
					return 0
				}
				result[i] = charset[num.Int64()]
			}

			l.PushString(string(result))
			return 1
		}},
	}
}
