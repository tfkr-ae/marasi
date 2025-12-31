package extensions

import (
	"net/http"
	"net/url"
	"time"

	"github.com/Shopify/go-lua"
	"github.com/google/uuid"
)

func registerUtilsLibrary(l *lua.State) {
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	lua.NewLibrary(l, utilsLibrary())

	l.SetField(-2, "utils")
	l.Pop(1)
}

// utilsLibrary returns a list of Lua functions that provide utility
// functionalities. These functions are available under the `marasi.utils`
// table in Lua scripts.
func utilsLibrary() []lua.RegistryFunction {
	return []lua.RegistryFunction{
		// uuid generates a new UUIDv7 and returns it as a string.
		//
		// @return string The new UUID.
		{Name: "uuid", Function: func(l *lua.State) int {
			id, err := uuid.NewV7()
			if err != nil {
				lua.Errorf(l, "generating uuid: %s", err.Error())
				return 0
			}
			l.PushString(id.String())
			return 1
		}},
		// timestamp returns the current time as a Unix timestamp in milliseconds.
		//
		// @return number The current timestamp.
		{Name: "timestamp", Function: func(l *lua.State) int {
			l.PushNumber(float64(time.Now().UnixMilli()))
			return 1
		}},
		// sleep pauses the execution for a given number of milliseconds.
		//
		// @param milliseconds int The number of milliseconds to sleep.
		// @param limit int (optional) The maximum number of milliseconds to sleep.
		{Name: "sleep", Function: func(l *lua.State) int {
			milliseconds := lua.CheckInteger(l, 2)
			limit := lua.OptInteger(l, 3, 60000)

			if milliseconds < limit {
				time.Sleep(time.Duration(milliseconds) * time.Millisecond)
			}
			return 0
		}},
		// cookie creates a new cookie object.
		//
		// @param name string The name of the cookie.
		// @param value string The value of the cookie.
		// @return Cookie The new cookie object.
		{Name: "cookie", Function: func(l *lua.State) int {
			name := lua.CheckString(l, 2)
			value := lua.CheckString(l, 3)

			cookie := &http.Cookie{
				Name:  name,
				Value: value,
				Path:  "/",
			}

			l.PushUserData(cookie)
			lua.SetMetaTableNamed(l, "cookie")
			return 1
		}},
		// url creates a new URL object from a string.
		//
		// @param url string The URL string.
		// @return URL The new URL object.
		{Name: "url", Function: func(l *lua.State) int {
			inputString := lua.CheckString(l, 2)
			parsed, err := url.Parse(inputString)

			if err != nil {
				lua.Errorf(l, "parsing URL: %s", err.Error())
				return 0
			}

			l.PushUserData(parsed)
			lua.SetMetaTableNamed(l, "url")
			return 1
		}},
	}
}
