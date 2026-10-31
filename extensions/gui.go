package extensions

import (
	"errors"

	"github.com/Shopify/go-lua"
)

// ServiceGUIType is marasi.gui.type in a service instance. The Marasi App
// registers its own marasi.gui with type "marasi-app".
const ServiceGUIType = "marasi-service"

// GUI receives an extension's marasi.gui calls on behalf of the embedder that
// draws the extension's UI.
type GUI struct {
	// Type is marasi.gui.type, which tells Lua which embedder it runs in.
	Type string
	// Render receives marasi.gui:render(target, schema).
	Render func(target string, schema any)
	// Update receives marasi.gui:update(key, value).
	Update func(key string, value any)
}

// WithGUI returns an option that registers marasi.gui. It is an option rather
// than part of registerAPI because each embedder registers its own, and two
// would collide.
func WithGUI(gui GUI) func(*Runtime) error {
	return func(extension *Runtime) error {
		l := extension.LuaState
		l.Global("marasi")
		defer l.Pop(1)
		if !l.IsTable(-1) {
			return errors.New("registering gui: the marasi global is missing")
		}
		l.Field(-1, "gui")
		registered := !l.IsNil(-1)
		l.Pop(1)
		if registered {
			return errors.New("extension already has a gui library")
		}

		extension.newLibrary("marasi.gui", guiLibrary(gui))
		l.PushString(gui.Type)
		l.SetField(-2, "type")
		l.SetField(-2, "gui")
		return nil
	}
}

func guiLibrary(gui GUI) []APIFunction {
	return []APIFunction{
		{Name: "render", Signature: "(target: string, schema: table)", Summary: "Sends schema for the client to draw at target. \"toast\" shows a toast; any other target, such as \"menu\", \"main\" or \"panel\", replaces what this extension last drew there. marasi.gui.type is \"" + ServiceGUIType + "\" in a service instance, which announces the call as extension.gui.rendered on /events.", Function: func(l *lua.State) int {
			target := lua.CheckString(l, 2)
			lua.CheckType(l, 3, lua.TypeTable)
			gui.Render(target, ParseTable(l, 3, GoValue))
			return 0
		}},
		{Name: "update", Signature: "(key: string, value: any)", Summary: "Sends a new value for key in this extension's UI state. A service instance announces the call as extension.gui.updated on /events.", Function: func(l *lua.State) int {
			key := lua.CheckString(l, 2)
			gui.Update(key, GoValue(l, 3))
			return 0
		}},
	}
}
