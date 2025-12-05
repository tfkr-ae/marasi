package extensions

import (
	"fmt"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
	"github.com/google/uuid"
)

func registerSettingsLibrary(l *lua.State, proxy ProxyService) {
	l.Global("marasi")

	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	lua.NewLibrary(l, settingsLibrary(proxy))

	l.SetField(-2, "settings")

	l.Pop(1)
}

// settingsLibrary returns a list of Lua functions for managing extension
// settings. These functions are available under the `marasi.settings` table
// in Lua scripts.
func settingsLibrary(proxy ProxyService) []lua.RegistryFunction {
	return []lua.RegistryFunction{
		// get returns the settings for the current extension.
		//
		// @return table The extension's settings as a Lua table.
		{Name: "get", Function: func(l *lua.State) int {
			repo, err := proxy.GetExtensionRepo()
			if err != nil {
				lua.Errorf(l, "getting extension repo: %s", err.Error())
				return 0
			}

			extID := getExtensionID(l)
			if extID == uuid.Nil {
				lua.Errorf(l, "extension ID is nil")
				return 0
			}

			settings, err := repo.GetExtensionSettingsByUUID(extID)
			if err != nil {
				lua.Errorf(l, "getting extension %s settings: %s", extID, err.Error())
				return 0
			}

			util.DeepPush(l, settings)
			return 1
		}},
		// set updates the settings for the current extension.
		//
		// @param settings table The new settings for the extension.
		// @return boolean True if the settings were updated successfully.
		{Name: "set", Function: func(l *lua.State) int {
			// util.PullTable cannot handle mixed keys
			val := goValue(l, 2)

			// empty tables in lua are cast as []any, need to convert this to map
			settingsMap := asMap(val)
			if settingsMap == nil {
				lua.Errorf(l,
					fmt.Sprintf("getting table(map) got: %T", val),
				)
				return 0
			}

			repo, err := proxy.GetExtensionRepo()
			if err != nil {
				lua.Errorf(l, "getting extension repo: %s", err.Error())
				return 0
			}

			extID := getExtensionID(l)
			if extID == uuid.Nil {
				lua.Errorf(l, "extension ID is nil")
				return 0
			}

			err = repo.SetExtensionSettingsByUUID(extID, settingsMap)
			if err != nil {
				lua.Errorf(l, "updating settings for extension %s: %s", extID.String(), err.Error())
				return 0
			}

			l.PushBoolean(true)
			return 1
		}},
	}
}
