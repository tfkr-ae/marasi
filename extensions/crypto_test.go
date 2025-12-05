package extensions

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
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
