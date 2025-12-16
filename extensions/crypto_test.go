package extensions

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func md5Helper(s string) string {
	hash := md5.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}

func sha1Helper(s string) string {
	hash := sha1.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}

func sha256Helper(s string) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])
}

func hmacHelper(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCryptoLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "crypto:md5 should return the correct hash",
			luaCode: `return marasi.crypto:md5("marasi")`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				want := md5Helper("marasi")
				if str != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%s", want, str)
				}
			},
		},
		{
			name:    "crypto:sha1 should return the correct hash",
			luaCode: `return marasi.crypto:sha1("marasi")`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				want := sha1Helper("marasi")
				if str != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%s", want, str)
				}
			},
		},
		{
			name:    "crypto:sha256 should return the correct hash",
			luaCode: `return marasi.crypto:sha256("marasi")`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				want := sha256Helper("marasi")
				if str != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%s", want, str)
				}
			},
		},
		{
			name:    "crypto:hmac_sha256 should return the correct hash",
			luaCode: `return marasi.crypto:hmac_sha256("s3cr3t", "marasi")`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				want := hmacHelper("s3cr3t", "marasi")
				if str != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%s", want, str)
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

func TestAESLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name: "aes:generate_key should return correct length keys",
			luaCode: `
				return {
					k16 = marasi.crypto.aes:generate_key(16),
					k24 = marasi.crypto.aes:generate_key(24),
					k32 = marasi.crypto.aes:generate_key(32)
				}
			`,
			validatorFunc: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
				if len(m["k16"].(string)) != 32 {
					t.Errorf("\nwanted:\n32 chars\ngot:\n%d", len(m["k16"].(string)))
				}
				if len(m["k24"].(string)) != 48 {
					t.Errorf("\nwanted:\n48 chars\ngot:\n%d", len(m["k24"].(string)))
				}
				if len(m["k32"].(string)) != 64 {
					t.Errorf("\nwanted:\n64 chars\ngot:\n%d", len(m["k32"].(string)))
				}
			},
		},
		{
			name: "aes:generate_key should fail on invalid length",
			luaCode: `
				local ok, res = pcall(marasi.crypto.aes.generate_key, marasi.crypto.aes, 10)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "key length expected to be 16, 24 or 32") {
					t.Errorf("\nwanted:\nerror containing 'key length expected to be 16, 24 or 32'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name:    "aes.gcm:generate_iv should return 12 bytes hex encoded",
			luaCode: `return marasi.crypto.aes.gcm:generate_iv()`,
			validatorFunc: func(t *testing.T, got any) {
				ivHex, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if len(ivHex) != 24 {
					t.Errorf("\nwanted:\n24 chars\ngot:\n%d", len(ivHex))
				}
			},
		},
		{
			name: "aes.gcm:encrypt and decrypt should work together",
			luaCode: `
				local key = marasi.crypto.aes:generate_key(32)
				local iv = marasi.crypto.aes.gcm:generate_iv()
				local plain = "marasi_secret"
				
				local cipher = marasi.crypto.aes.gcm:encrypt(key, plain, iv)
				local decrypted = marasi.crypto.aes.gcm:decrypt(key, cipher, iv)
				
				return decrypted
			`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := "marasi_secret"
				if str != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%s", want, str)
				}
			},
		},
		{
			name: "aes.gcm:encrypt should fail on invalid key hex",
			luaCode: ` local iv = marasi.crypto.aes.gcm:generate_iv()
				local ok, res = pcall(marasi.crypto.aes.gcm.encrypt, marasi.crypto.aes.gcm, "not_hex", "plain", iv)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "expected key to be hex encoded") {
					t.Errorf("\nwanted:\nerror containing 'expected key to be hex encoded'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "aes.gcm:encrypt should fail on invalid IV length",
			luaCode: `
				local key = marasi.crypto.aes:generate_key(32)
				local bad_iv = "1234"
				local ok, res = pcall(marasi.crypto.aes.gcm.encrypt, marasi.crypto.aes.gcm, key, "plain", bad_iv)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "iv length should be") {
					t.Errorf("\nwanted:\nerror containing 'iv length should be'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "aes.gcm:decrypt should fail on authentication failure",
			luaCode: `
				local key = marasi.crypto.aes:generate_key(32)
				local iv = marasi.crypto.aes.gcm:generate_iv()
				local cipher = marasi.crypto.aes.gcm:encrypt(key, "secret", iv)
				
				local bad_iv = marasi.crypto.aes.gcm:generate_iv()
				
				local ok, res = pcall(marasi.crypto.aes.gcm.decrypt, marasi.crypto.aes.gcm, key, cipher, bad_iv)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "message authentication failed") {
					t.Errorf("\nwanted:\nerror containing 'message authentication failed'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name:    "aes.cbc:generate_iv should return 16 bytes hex encoded",
			luaCode: `return marasi.crypto.aes.cbc:generate_iv()`,
			validatorFunc: func(t *testing.T, got any) {
				ivHex, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if len(ivHex) != 32 {
					t.Errorf("\nwanted:\n32 chars\ngot:\n%d", len(ivHex))
				}
			},
		},
		{
			name: "aes.cbc:encrypt and decrypt should work together",
			luaCode: `
				local key = marasi.crypto.aes:generate_key(32)
				local iv = marasi.crypto.aes.cbc:generate_iv()
				local plain = "marasi_secret_padded"
				
				local cipher = marasi.crypto.aes.cbc:encrypt(key, plain, iv)
				local decrypted = marasi.crypto.aes.cbc:decrypt(key, cipher, iv)
				
				return decrypted
			`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := "marasi_secret_padded"
				if str != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%s", want, str)
				}
			},
		},
		{
			name: "aes.cbc:decrypt should fail if ciphertext length is invalid",
			luaCode: `
				local key = marasi.crypto.aes:generate_key(32)
				local iv = marasi.crypto.aes.cbc:generate_iv()
				local bad_cipher = "12345678" 
				local ok, res = pcall(marasi.crypto.aes.cbc.decrypt, marasi.crypto.aes.cbc, key, bad_cipher, iv)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "multiple of block size") {
					t.Errorf("\nwanted:\nerror containing 'multiple of block size'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "aes.cbc:decrypt should fail on invalid padding",
			luaCode: `
				local key = marasi.crypto.aes:generate_key(32)
				local iv = marasi.crypto.aes.cbc:generate_iv()
				local cipher = marasi.crypto.aes.cbc:encrypt(key, "secret", iv)
				
				local bad_key = marasi.crypto.aes:generate_key(32)
				
				local ok, res = pcall(marasi.crypto.aes.cbc.decrypt, marasi.crypto.aes.cbc, bad_key, cipher, iv)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "invalid padding") {
					t.Errorf("\nwanted:\nerror containing 'invalid padding'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")
			if err := extension.ExecuteLua(tt.luaCode); err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}
			got := goValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}

func TestRSALibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name: "rsa:generate_pair should return keys",
			luaCode: `
				local priv, pub = marasi.crypto.rsa:generate_pair(2048)
				return {priv = priv, pub = pub}
			`,
			validatorFunc: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
				priv, _ := m["priv"].(string)
				pub, _ := m["pub"].(string)

				if len(priv) == 0 {
					t.Errorf("\nwanted:\nnon-empty private key\ngot:\nempty")
				}
				if len(pub) == 0 {
					t.Errorf("\nwanted:\nnon-empty public key\ngot:\nempty")
				}
			},
		},
		{
			name: "rsa:encrypt and decrypt should work together",
			luaCode: `
				local priv, pub = marasi.crypto.rsa:generate_pair(2048)
				local plain = "marasi_rsa_secret"
				
				local cipher = marasi.crypto.rsa:encrypt(pub, plain)
				local decrypted = marasi.crypto.rsa:decrypt(priv, cipher)
				
				return decrypted
			`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				want := "marasi_rsa_secret"
				if str != want {
					t.Errorf("\nwanted:\n%s\ngot:\n%s", want, str)
				}
			},
		},
		{
			name: "rsa:generate_pair should fail on invalid length",
			luaCode: `
				local ok, res = pcall(marasi.crypto.rsa.generate_pair, marasi.crypto.rsa, 1024)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "length expected to be 2048, 3072, or 4096") {
					t.Errorf("\nwanted:\nerror containing 'length expected to be 2048, 3072, or 4096'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "rsa:encrypt should fail on invalid public key hex",
			luaCode: `
				local ok, res = pcall(marasi.crypto.rsa.encrypt, marasi.crypto.rsa, "not_hex", "msg")
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "expected key to be hex encoded") {
					t.Errorf("\nwanted:\nerror containing 'expected key to be hex encoded'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "rsa:decrypt should fail on invalid private key hex",
			luaCode: `
				local ok, res = pcall(marasi.crypto.rsa.decrypt, marasi.crypto.rsa, "not_hex", "cipher")
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "expected key to be hex encoded") {
					t.Errorf("\nwanted:\nerror containing 'expected key to be hex encoded'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "rsa:decrypt should fail on invalid ciphertext hex",
			luaCode: `
				local priv, pub = marasi.crypto.rsa:generate_pair(2048)
				local ok, res = pcall(marasi.crypto.rsa.decrypt, marasi.crypto.rsa, priv, "not_hex")
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "expected ciphertext to be hex encoded") {
					t.Errorf("\nwanted:\nerror containing 'expected ciphertext to be hex encoded'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")
			if err := extension.ExecuteLua(tt.luaCode); err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}
			got := goValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}

func TestEd25519Library(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name: "ed25519:generate_pair should return correct length keys",
			luaCode: `
				local priv, pub = marasi.crypto.ed25519:generate_pair()
				return {priv = priv, pub = pub}
			`,
			validatorFunc: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("\nwanted:\nmap\ngot:\n%T", got)
				}
				priv, _ := m["priv"].(string)
				pub, _ := m["pub"].(string)

				if len(priv) != 128 {
					t.Errorf("\nwanted:\n128 chars (priv)\ngot:\n%d", len(priv))
				}
				if len(pub) != 64 {
					t.Errorf("\nwanted:\n64 chars (pub)\ngot:\n%d", len(pub))
				}
			},
		},
		{
			name: "ed25519:sign and verify should succeed",
			luaCode: `
				local priv, pub = marasi.crypto.ed25519:generate_pair()
				local msg = "trust_verify"
				local sig = marasi.crypto.ed25519:sign(priv, msg)
				
				return marasi.crypto.ed25519:verify(pub, msg, sig)
			`,
			validatorFunc: func(t *testing.T, got any) {
				valid, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nbool\ngot:\n%T", got)
				}
				if !valid {
					t.Errorf("\nwanted:\ntrue\ngot:\nfalse")
				}
			},
		},
		{
			name: "ed25519:verify should fail on tampered signature",
			luaCode: `
				local priv, pub = marasi.crypto.ed25519:generate_pair()
				local msg = "trust_verify"
				
				local sig = marasi.crypto.ed25519:sign(priv, msg)
				
				local bad_sig = marasi.crypto.ed25519:sign(priv, "malicious_payload")
				
				return marasi.crypto.ed25519:verify(pub, msg, bad_sig)
			`,
			validatorFunc: func(t *testing.T, got any) {
				valid, ok := got.(bool)
				if !ok {
					t.Fatalf("\nwanted:\nbool\ngot:\n%T", got)
				}
				if valid {
					t.Errorf("\nwanted:\nfalse\ngot:\ntrue")
				}
			},
		},
		{
			name: "ed25519:sign should fail on invalid private key length",
			luaCode: `
				local ok, res = pcall(marasi.crypto.ed25519.sign, marasi.crypto.ed25519, "1234", "msg")
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "private key length should be") {
					t.Errorf("\nwanted:\nerror containing 'private key length should be'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "ed25519:verify should fail on invalid public key length",
			luaCode: `
				local priv, pub = marasi.crypto.ed25519:generate_pair()
				local sig = marasi.crypto.ed25519:sign(priv, "msg")
				local ok, res = pcall(marasi.crypto.ed25519.verify, marasi.crypto.ed25519, "1234", "msg", sig)
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "public key length should be") {
					t.Errorf("\nwanted:\nerror containing 'public key length should be'\ngot:\n%s", errStr)
				}
			},
		},
		{
			name: "ed25519:verify should fail on invalid signature length",
			luaCode: `
				local priv, pub = marasi.crypto.ed25519:generate_pair()
				local ok, res = pcall(marasi.crypto.ed25519.verify, marasi.crypto.ed25519, pub, "msg", "1234")
				if ok then
					return "expected error"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "signature length should be") {
					t.Errorf("\nwanted:\nerror containing 'signature length should be'\ngot:\n%s", errStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")
			if err := extension.ExecuteLua(tt.luaCode); err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}
			got := goValue(extension.LuaState, -1)
			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}
