package extensions

import (
	"crypto/rand"
	"math/big"

	"github.com/Shopify/go-lua"
)

func registerRandomLibrary(extension *Runtime) {
	l := extension.LuaState
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	newLibrary(extension, "marasi.random", randomLibrary())

	l.SetField(-2, "random")

	l.Pop(1)
}

// randomLibrary returns a list of Lua functions that provide random data
// generation functionalities. These functions are available under the `marasi.random`
// table in Lua scripts.
func randomLibrary() []APIFunction {
	return []APIFunction{
		{Name: "int", Signature: "(min: integer, max: integer) -> integer", Summary: "Returns a random integer in a given range.", Function: func(l *lua.State) int {
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
		{Name: "string", Signature: "(length: integer, charset?: string) -> string", Summary: "Returns a random string of a given length, using an optional charset. charset defaults to letters and digits.", Function: func(l *lua.State) int {
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
