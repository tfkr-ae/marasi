package extensions

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Shopify/go-lua"
)

func registerCryptoLibrary(l *lua.State) {
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	lua.NewLibrary(l, cryptoLibrary())

	l.SetField(-2, "crypto")

	l.Pop(1)
}

// cryptoLibrary returns a list of Lua functions that provide cryptographic
// hashing functionalities. These functions are available under the `marasi.crypto`
// table in Lua scripts.
func cryptoLibrary() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		// md5 calculates the MD5 hash of a given string.
		//
		// @param input string The string to hash.
		// @return string The MD5 hash encoded as a hexadecimal string.
		{Name: "md5", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			hash := md5.Sum([]byte(inputString))
			l.PushString(hex.EncodeToString(hash[:]))
			return 1
		}},
		// sha1 calculates the SHA1 hash of a given string.
		//
		// @param input string The string to hash.
		// @return string The SHA1 hash encoded as a hexadecimal string.
		{Name: "sha1", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			hash := sha1.Sum([]byte(inputString))
			l.PushString(hex.EncodeToString(hash[:]))
			return 1
		}},
		// sha256 calculates the SHA256 hash of a given string.
		//
		// @param input string The string to hash.
		// @return string The SHA256 hash encoded as a hexadecimal string.
		{Name: "sha256", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)

			hash := sha256.Sum256([]byte(inputString))
			l.PushString(hex.EncodeToString(hash[:]))
			return 1
		}},
		// hmac_sha256 calculates the HMAC-SHA256 of a message with a given secret.
		//
		// @param secret string The secret key.
		// @param message string The message to authenticate.
		// @return string The HMAC-SHA256 encoded as a hexadecimal string.
		{Name: "hmac_sha256", Function: func(l *lua.State) int {
			secret := lua.CheckString(l, 2)
			message := lua.CheckString(l, 3)

			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(message))

			l.PushString(hex.EncodeToString(mac.Sum(nil)))
			return 1
		}},
	}
}
