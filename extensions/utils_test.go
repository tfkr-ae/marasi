package extensions

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUtilsLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "utils:uuid should return a valid uuid v7",
			luaCode: `return marasi.utils:uuid()`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}
				if _, err := uuid.Parse(str); err != nil {
					t.Errorf("\nwanted:\nvalid uuid\ngot:\n%s (err: %v)", str, err)
				}
			},
		},
		{
			name:    "utils:timestamp should return current time in millis",
			luaCode: `return marasi.utils:timestamp()`,
			validatorFunc: func(t *testing.T, got any) {
				ts, ok := got.(float64)
				if !ok {
					t.Fatalf("\nwanted:\nnumber\ngot:\n%T", got)
				}
				now := float64(time.Now().UnixMilli())
				if (now-ts) > 50 || ts > now {
					t.Errorf("\nwanted:\n~%v\ngot:\n%v", now, ts)
				}
			},
		},
		{
			name: "utils:sleep should sleep for specified duration",
			luaCode: `
				local start = marasi.utils:timestamp()
				marasi.utils:sleep(10)
				local finish = marasi.utils:timestamp()
				return finish - start
			`,
			validatorFunc: func(t *testing.T, got any) {
				diff, ok := got.(float64)
				if !ok {
					t.Fatalf("\nwanted:\nnumber\ngot:\n%T", got)
				}

				if diff < 10 {
					t.Errorf("\nwanted:\n>= 10ms\ngot:\n%vms", diff)
				}
			},
		},
		{
			name:    "utils:cookie should return a cookie userdata with default values",
			luaCode: `return marasi.utils:cookie("marasi_session", "123456")`,
			validatorFunc: func(t *testing.T, got any) {
				cookie, ok := got.(*http.Cookie)

				if !ok {
					t.Fatalf("\nwanted:\n*http.Cookie\ngot:\n%T", got)
				}

				want := &http.Cookie{
					Name:  "marasi_session",
					Value: "123456",
					Path:  "/",
				}
				if !reflect.DeepEqual(want, cookie) {
					t.Errorf("\nwanted:\n%v\ngot:\n%v", want, cookie)
				}
			},
		},
		{
			name:    "utils:url should return url userdata",
			luaCode: `return marasi.utils:url("https://marasi:password@marasi.app/path?query=1")`,
			validatorFunc: func(t *testing.T, got any) {
				u, ok := got.(*url.URL)
				if !ok {
					t.Fatalf("\nwanted:\n*url.URL\ngot:\n%T", got)
				}

				want, err := url.Parse("https://marasi:password@marasi.app/path?query=1")
				if err != nil {
					t.Fatalf("parsing url : %v", err)
				}
				if !reflect.DeepEqual(want, u) {
					t.Errorf("\nwanted:\n%#v\ngot:\n%#v", want, u)
				}
			},
		},
		{
			name: "utils:url should return an error when parsing an invalid URL",
			luaCode: `
                local ok, res = pcall(marasi.utils.url, marasi.utils, "%")
				if ok then
					return "expected nil value"
				end
				return res
            `,
			validatorFunc: func(t *testing.T, got any) {
				errStr, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errStr, "invalid URL escape") {
					t.Errorf("wanted error containing 'invalid URL escape', got: %q", errStr)
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

			got := GoValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}
