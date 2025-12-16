package extensions

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/Shopify/go-lua"
)

func registerCryptoLibrary(l *lua.State) {
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	lua.NewLibrary(l, cryptoLibrary())

	lua.NewLibrary(l, aesLibrary())

	lua.NewLibrary(l, aesGCM())
	l.SetField(-2, "gcm")

	lua.NewLibrary(l, aesCBC())
	l.SetField(-2, "cbc")

	l.SetField(-2, "aes")

	lua.NewLibrary(l, rsaLibrary())
	l.SetField(-2, "rsa")

	lua.NewLibrary(l, ed25519Library())
	l.SetField(-2, "ed25519")

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

func aesLibrary() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		{Name: "generate_key", Function: func(l *lua.State) int {
			length := lua.OptInteger(l, 2, 32)

			if length != 16 && length != 24 && length != 32 {
				lua.ArgumentError(l, 2, "key length expected to be 16, 24 or 32")
				return 0
			}

			key := make([]byte, length)
			_, err := io.ReadFull(rand.Reader, key)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("generating key : %s", err.Error()))
				return 0
			}

			l.PushString(hex.EncodeToString(key))
			return 1
		}},
	}
}

func aesGCM() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		{Name: "generate_iv", Function: func(l *lua.State) int {
			iv := make([]byte, 12)
			_, err := io.ReadFull(rand.Reader, iv)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("generating iv : %s", err.Error()))
				return 0
			}

			l.PushString(hex.EncodeToString(iv))
			return 1

		}},

		{Name: "encrypt", Function: func(l *lua.State) int {
			keyHex := lua.CheckString(l, 2)
			plaintext := lua.CheckString(l, 3)
			ivHex := lua.CheckString(l, 4)

			key, err := hex.DecodeString(keyHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected key to be hex encoded")
				return 0
			}

			iv, err := hex.DecodeString(ivHex)
			if err != nil {
				lua.ArgumentError(l, 3, "expected iv to be hex encoded")
				return 0
			}

			block, err := aes.NewCipher(key)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("creating new block cipher with key %s : %s", keyHex, err.Error()))
				return 0
			}

			gcm, err := cipher.NewGCM(block)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("creating new GCM block cipher : %s", err.Error()))
				return 0
			}

			if len(iv) != gcm.NonceSize() {
				lua.ArgumentError(l, 4, fmt.Sprintf("iv length should be %d", gcm.NonceSize()))
				return 0
			}

			ciphertext := gcm.Seal(nil, iv, []byte(plaintext), nil)

			l.PushString(hex.EncodeToString(ciphertext))
			return 1
		}},

		{Name: "decrypt", Function: func(l *lua.State) int {
			keyHex := lua.CheckString(l, 2)
			cipherHex := lua.CheckString(l, 3)
			ivHex := lua.CheckString(l, 4)

			key, err := hex.DecodeString(keyHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected key to be hex encoded")
				return 0
			}

			cipherText, err := hex.DecodeString(cipherHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected ciphertext to be hex encoded")
				return 0
			}

			iv, err := hex.DecodeString(ivHex)
			if err != nil {
				lua.ArgumentError(l, 3, "expected iv to be hex encoded")
				return 0
			}

			block, err := aes.NewCipher(key)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("creating new block cipher with key %s : %s", keyHex, err.Error()))
				return 0
			}

			gcm, err := cipher.NewGCM(block)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("creating new GCM block cipher : %s", err.Error()))
				return 0
			}

			if len(iv) != gcm.NonceSize() {
				lua.ArgumentError(l, 4, fmt.Sprintf("iv length should be %d for AES-GCM", gcm.NonceSize()))
				return 0
			}

			plaintext, err := gcm.Open(nil, iv, cipherText, nil)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("decrypting cipher text: %s", err.Error()))
				return 0
			}

			l.PushString(string(plaintext))
			return 1
		}},
	}
}

func aesCBC() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		{Name: "generate_iv", Function: func(l *lua.State) int {
			iv := make([]byte, aes.BlockSize)
			if _, err := io.ReadFull(rand.Reader, iv); err != nil {
				lua.Errorf(l, fmt.Sprintf("generating iv: %s", err.Error()))
				return 0
			}
			l.PushString(hex.EncodeToString(iv))
			return 1
		}},
		{Name: "encrypt", Function: func(l *lua.State) int {
			keyHex := lua.CheckString(l, 2)
			plaintext := lua.CheckString(l, 3)
			ivHex := lua.CheckString(l, 4)

			key, err := hex.DecodeString(keyHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected key to be hex encoded")
				return 0
			}
			iv, err := hex.DecodeString(ivHex)
			if err != nil {
				lua.ArgumentError(l, 4, "expected iv to be hex encoded")
				return 0
			}

			if len(iv) != aes.BlockSize {
				lua.ArgumentError(l, 4, fmt.Sprintf("iv length should be %d for AES-CBC", aes.BlockSize))
				return 0
			}

			block, err := aes.NewCipher(key)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("creating cipher: %s", err.Error()))
				return 0
			}

			paddingLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
			totalLen := len(plaintext) + paddingLen

			ciphertext := make([]byte, totalLen)

			copy(ciphertext, plaintext)
			padByte := byte(paddingLen)
			for i := len(plaintext); i < totalLen; i++ {
				ciphertext[i] = padByte
			}

			mode := cipher.NewCBCEncrypter(block, iv)
			mode.CryptBlocks(ciphertext, ciphertext)

			l.PushString(hex.EncodeToString(ciphertext))
			return 1
		}},
		{Name: "decrypt", Function: func(l *lua.State) int {
			keyHex := lua.CheckString(l, 2)
			cipherHex := lua.CheckString(l, 3)
			ivHex := lua.CheckString(l, 4)

			key, err := hex.DecodeString(keyHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected key to be hex encoded")
				return 0
			}

			data, err := hex.DecodeString(cipherHex)
			if err != nil {
				lua.ArgumentError(l, 3, "expected ciphertext to be hex encoded")
				return 0
			}

			iv, err := hex.DecodeString(ivHex)
			if err != nil {
				lua.ArgumentError(l, 4, "expected iv to be hex encoded")
				return 0
			}

			if len(iv) != aes.BlockSize {
				lua.ArgumentError(l, 4, fmt.Sprintf("iv length should be %d for AES-CBC", aes.BlockSize))
				return 0
			}

			if len(data)%aes.BlockSize != 0 {
				lua.ArgumentError(l, 3, "expected ciphertext to be a multiple of block size")
				return 0
			}

			block, err := aes.NewCipher(key)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("creating cipher: %s", err.Error()))
				return 0
			}

			mode := cipher.NewCBCDecrypter(block, iv)
			mode.CryptBlocks(data, data)

			length := len(data)
			if length == 0 {
				lua.Errorf(l, "invalid padding size")
				return 0
			}

			unpadding := int(data[length-1])
			if unpadding > length || unpadding == 0 {
				lua.Errorf(l, "invalid padding")
				return 0
			}

			for i := range unpadding {
				if data[length-1-i] != byte(unpadding) {
					lua.Errorf(l, "invalid padding")
					return 0
				}
			}

			data = data[:(length - unpadding)]

			l.PushString(string(data))
			return 1
		}},
	}
}

func rsaLibrary() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		{Name: "generate_pair", Function: func(l *lua.State) int {
			length := lua.OptInteger(l, 2, 2048)

			if length != 2048 && length != 3072 && length != 4096 {
				lua.ArgumentError(l, 2, "length expected to be 2048, 3072, or 4096")
				return 0
			}

			privKey, err := rsa.GenerateKey(rand.Reader, length)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("generating key pair: %s", err.Error()))
				return 0
			}

			privBytes := x509.MarshalPKCS1PrivateKey(privKey)
			pubBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("marshalling public key: %s", err.Error()))
				return 0
			}

			l.PushString(hex.EncodeToString(privBytes))
			l.PushString(hex.EncodeToString(pubBytes))
			return 2

		}},
		{Name: "encrypt", Function: func(l *lua.State) int {
			pubHex := lua.CheckString(l, 2)
			plaintext := lua.CheckString(l, 3)

			pubBytes, err := hex.DecodeString(pubHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected key to be hex encoded")
				return 0
			}

			pub, err := x509.ParsePKIXPublicKey(pubBytes)
			if err != nil {
				lua.ArgumentError(l, 2, fmt.Sprintf("parsing public key : %s", err.Error()))
				return 0
			}

			rsaPub, ok := pub.(*rsa.PublicKey)
			if !ok {
				lua.ArgumentError(l, 2, "expected key to be an RSA public key")
				return 0
			}

			ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPub, []byte(plaintext), nil)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("encrypting plaintext: %s", err.Error()))
				return 0
			}

			l.PushString(hex.EncodeToString(ciphertext))
			return 1
		}},

		{Name: "decrypt", Function: func(l *lua.State) int {
			keyHex := lua.CheckString(l, 2)
			cipherHex := lua.CheckString(l, 3)

			privBytes, err := hex.DecodeString(keyHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected key to be hex encoded")
				return 0
			}

			ciphertext, err := hex.DecodeString(cipherHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected ciphertext to be hex encoded")
				return 0
			}

			priv, err := x509.ParsePKCS1PrivateKey(privBytes)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("parsing private key: %s", err.Error()))
				return 0
			}

			plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, ciphertext, nil)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("decrypting ciphertext: %s", err.Error()))
				return 0
			}

			l.PushString(string(plaintext))
			return 1
		}},
	}
}

// ed25519Library returns a list of Lua functions for Ed25519 signing and verification.
func ed25519Library() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		// generate_pair generates a new Ed25519 private/public key pair.
		//
		// @return string Private Key (Hex encoded, 64 bytes)
		// @return string Public Key (Hex encoded, 32 bytes)
		{Name: "generate_pair", Function: func(l *lua.State) int {
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("generating key pair: %s", err.Error()))
				return 0
			}

			l.PushString(hex.EncodeToString(priv))
			l.PushString(hex.EncodeToString(pub))
			return 2
		}},

		// sign calculates the signature of a message using a private key.
		//
		// @param private_key string Hex encoded private key (64 bytes).
		// @param message string The message to sign.
		// @return string The signature (Hex encoded, 64 bytes).
		{Name: "sign", Function: func(l *lua.State) int {
			privHex := lua.CheckString(l, 2)
			message := lua.CheckString(l, 3)

			privBytes, err := hex.DecodeString(privHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected private key to be hex encoded")
				return 0
			}

			if len(privBytes) != ed25519.PrivateKeySize {
				lua.ArgumentError(l, 2, fmt.Sprintf("private key length should be %d", ed25519.PrivateKeySize))
				return 0
			}

			sig := ed25519.Sign(ed25519.PrivateKey(privBytes), []byte(message))
			l.PushString(hex.EncodeToString(sig))
			return 1
		}},

		// verify checks if a signature is valid for a given message and public key.
		//
		// @param public_key string Hex encoded public key (32 bytes).
		// @param message string The message that was signed.
		// @param signature string Hex encoded signature.
		// @return boolean True if valid, False otherwise.
		{Name: "verify", Function: func(l *lua.State) int {
			pubHex := lua.CheckString(l, 2)
			message := lua.CheckString(l, 3)
			sigHex := lua.CheckString(l, 4)

			pubBytes, err := hex.DecodeString(pubHex)
			if err != nil {
				lua.ArgumentError(l, 2, "expected public key to be hex encoded")
				return 0
			}

			if len(pubBytes) != ed25519.PublicKeySize {
				lua.ArgumentError(l, 2, fmt.Sprintf("public key length should be %d", ed25519.PublicKeySize))
				return 0
			}

			sigBytes, err := hex.DecodeString(sigHex)
			if err != nil {
				lua.ArgumentError(l, 4, "expected signature to be hex encoded")
				return 0
			}

			if len(sigBytes) != ed25519.SignatureSize {
				lua.ArgumentError(l, 4, fmt.Sprintf("signature length should be %d", ed25519.SignatureSize))
				return 0
			}

			valid := ed25519.Verify(ed25519.PublicKey(pubBytes), []byte(message), sigBytes)
			l.PushBoolean(valid)
			return 1
		}},
	}
}
