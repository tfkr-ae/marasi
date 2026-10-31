package extensions

import (
	"sort"
	"sync"

	"github.com/Shopify/go-lua"
	"github.com/tfkr-ae/marasi/domain"
)

// Hook names are the global functions the proxy calls in an extension's Lua.
const (
	hookStartup                   = "startup"
	hookProcessRequest            = "processRequest"
	hookProcessResponse           = "processResponse"
	hookProcessWebSocketMessage   = "processWebSocketMessage"
	hookInterceptRequest          = "interceptRequest"
	hookInterceptResponse         = "interceptResponse"
	hookInterceptWebSocketMessage = "interceptWebSocketMessage"
)

// hooks documents every hook constant above. Hooks are looked up by name, so
// they are the one part of the reference that is not recorded at registration.
var hooks = []Entry{
	{Name: hookStartup, Signature: "()", Summary: "Called once after the extension's source runs. An error stops the extension from loading."},
	{Name: hookProcessRequest, Signature: "(request: req)", Summary: "Called for each request before it is sent upstream. Change the request in place, or call request:drop() or request:skip(). compass runs first, then other enabled extensions. Not called in checkpoint."},
	{Name: hookProcessResponse, Signature: "(response: res)", Summary: "Called for each response before it reaches the client. Change the response in place, or call response:drop() or response:skip(). compass runs first, then other enabled extensions. Not called in checkpoint."},
	{Name: hookProcessWebSocketMessage, Signature: "(message: wsmsg)", Summary: "Called for each WebSocket message in either direction. Change the message in place, or call message:drop() or message:skip(). Called in enabled extensions other than checkpoint."},
	{Name: hookInterceptRequest, Signature: "(request: req) -> boolean", Summary: "Called only in checkpoint. Return true to hold the request for review."},
	{Name: hookInterceptResponse, Signature: "(response: res) -> boolean", Summary: "Called only in checkpoint. Return true to hold the response for review."},
	{Name: hookInterceptWebSocketMessage, Signature: "(message: wsmsg) -> boolean", Summary: "Called only in checkpoint. Return true to hold the WebSocket message for review."},
}

// APIFunction is a Lua function registered together with its reference entry.
// Signature lists parameters and results in the form "(name: type, optional?: type) -> type".
type APIFunction struct {
	Name      string
	Signature string
	Summary   string
	Function  lua.Function
}

// Entry documents one Lua hook, global, function, or method.
type Entry struct {
	Name      string `json:"name"`
	Signature string `json:"signature,omitempty"`
	Summary   string `json:"summary"`
}

// Library documents a table of functions. Name is its full Lua path, such as marasi.crypto.aes.
type Library struct {
	Name      string  `json:"name"`
	Functions []Entry `json:"functions"`
}

// Type documents an object type that the proxy passes to Lua and its methods.
type Type struct {
	Name    string  `json:"name"`
	Summary string  `json:"summary"`
	Methods []Entry `json:"methods"`
}

// APIReference documents what an extension's Lua can call.
type APIReference struct {
	// Hooks are the global functions the proxy calls when an extension defines them.
	Hooks []Entry `json:"hooks"`
	// Globals are the non-library globals Marasi adds.
	Globals []Entry `json:"globals"`
	// Libraries are the tables under the marasi global.
	Libraries []Library `json:"libraries"`
	// Types are the object types passed to hooks or returned by functions.
	Types []Type `json:"types"`
	// Standard lists the Lua standard library names left in the sandbox.
	// The library named base holds the standard global functions.
	Standard []Library `json:"standard"`
}

// Reference returns the Lua API reference. It records what a new extension
// runtime registers, so it matches the running code.
var Reference = sync.OnceValue(func() APIReference {
	ref := APIReference{Hooks: hooks}

	// Registration only stores the proxy in closures, so a nil proxy is safe
	// in a runtime that never runs Lua.
	extension := &Runtime{Data: &domain.Extension{}, LuaState: lua.NewState(), reference: &ref}
	registerAPI(extension, nil)
	// A service instance adds marasi.gui as an option, so record it the same way.
	_ = WithGUI(GUI{Type: ServiceGUIType})(extension)

	bare := lua.NewState()
	openStandardLibraries(bare)
	ref.Standard = standardLibraries(bare, ref.Globals)

	sort.Slice(ref.Libraries, func(i, j int) bool { return ref.Libraries[i].Name < ref.Libraries[j].Name })
	sort.Slice(ref.Types, func(i, j int) bool { return ref.Types[i].Name < ref.Types[j].Name })
	return ref
})

// openStandardLibraries opens the sandboxed subset of the Lua standard library.
func openStandardLibraries(l *lua.State) {
	lua.BaseOpen(l)
	l.Pop(1)

	lua.TableOpen(l)
	l.SetGlobal("table")

	lua.MathOpen(l)
	l.SetGlobal("math")

	lua.Bit32Open(l)
	l.SetGlobal("bit32")

	for _, name := range []string{"loadfile", "dofile", "load", "require", "collectgarbage"} {
		l.PushNil()
		l.SetGlobal(name)
	}
}

// registerAPI adds Marasi's globals, types, and libraries to an extension's state.
func registerAPI(extension *Runtime, proxy ProxyService) {
	extension.LuaState.PushString(extension.Data.ID.String())
	extension.LuaState.SetGlobal("extension_id")
	extension.recordGlobal(Entry{Name: "extension_id", Summary: "This extension's UUID, as a string."})

	RegisterCustomPrint(extension)
	RegisterRequestType(extension)
	RegisterResponseType(extension)
	RegisterWebSocketMessageType(extension)
	RegisterURLType(extension)
	RegisterHeaderType(extension)
	RegisterCookieType(extension)
	RegisterRequestBuilderType(extension)
	RegisterRegexType(extension)
	RegisterScopeType(extension)

	registerMarasiLibrary(extension, proxy)
}

// standardLibraries lists the globals of a state that has only the standard
// libraries open, leaving out the globals Marasi replaces.
func standardLibraries(l *lua.State, replaced []Entry) []Library {
	skip := map[string]bool{}
	for _, global := range replaced {
		skip[global.Name] = true
	}
	base := Library{Name: "base"}
	var libraries []Library
	l.PushGlobalTable()
	l.PushNil()
	for l.Next(-2) {
		name, _ := l.ToString(-2)
		if l.IsTable(-1) && name != "_G" {
			library := Library{Name: name}
			l.PushNil()
			for l.Next(-2) {
				function, _ := l.ToString(-2)
				library.Functions = append(library.Functions, Entry{Name: function})
				l.Pop(1)
			}
			sortEntries(library.Functions)
			libraries = append(libraries, library)
		} else if !skip[name] {
			base.Functions = append(base.Functions, Entry{Name: name})
		}
		l.Pop(1)
	}
	l.Pop(1)
	sortEntries(base.Functions)
	sort.Slice(libraries, func(i, j int) bool { return libraries[i].Name < libraries[j].Name })
	return append([]Library{base}, libraries...)
}

// newLibrary pushes a table holding functions. When the reference is being
// built, it records the functions under path.
func (extension *Runtime) newLibrary(path string, functions []APIFunction) {
	registry := make([]lua.RegistryFunction, len(functions))
	for i, function := range functions {
		registry[i] = lua.RegistryFunction{Name: function.Name, Function: function.Function}
	}
	lua.NewLibrary(extension.LuaState, registry)
	if extension.reference != nil {
		extension.reference.Libraries = append(extension.reference.Libraries, Library{Name: path, Functions: entries(functions)})
	}
}

func (extension *Runtime) recordGlobal(entry Entry) {
	if extension.reference != nil {
		extension.reference.Globals = append(extension.reference.Globals, entry)
	}
}

func (extension *Runtime) recordType(name, summary string, methods []APIFunction) {
	if extension.reference != nil {
		extension.reference.Types = append(extension.reference.Types, Type{Name: name, Summary: summary, Methods: entries(methods)})
	}
}

func entries(functions []APIFunction) []Entry {
	out := make([]Entry, len(functions))
	for i, function := range functions {
		out[i] = Entry{Name: function.Name, Signature: function.Signature, Summary: function.Summary}
	}
	sortEntries(out)
	return out
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}
