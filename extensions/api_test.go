package extensions

import (
	"sort"
	"strings"
	"testing"

	"github.com/Shopify/go-lua"
)

// TestReferenceMatchesLiveState walks the Lua state an extension runs in and
// compares every name it finds with Reference, in both directions.
func TestReferenceMatchesLiveState(t *testing.T) {
	// marasi.gui is an embedder's option rather than part of registerAPI, so
	// the live state gets it the way a service instance does.
	ext, _ := setupTestExtension(t, "", WithGUI(GUI{Type: ServiceGUIType, Render: func(string, any) {}, Update: func(string, any) {}}))
	l := ext.LuaState
	ref := Reference()

	documented := map[string]bool{}
	for _, library := range ref.Libraries {
		for _, function := range library.Functions {
			documented[library.Name+":"+function.Name] = true
		}
	}
	live := map[string]bool{}
	l.Global("marasi")
	walkLibrary(l, "marasi", live)
	l.Pop(1)
	compareNames(t, "library function", live, documented)

	documented = map[string]bool{}
	for _, global := range ref.Globals {
		documented[global.Name] = true
	}
	standard := map[string]bool{"marasi": true}
	for _, library := range ref.Standard {
		if library.Name == "base" {
			for _, function := range library.Functions {
				standard[function.Name] = true
			}
			continue
		}
		standard[library.Name] = true
	}
	live = map[string]bool{}
	l.PushGlobalTable()
	l.PushNil()
	for l.Next(-2) {
		if name, ok := l.ToString(-2); ok && !standard[name] {
			live[name] = true
		}
		l.Pop(1)
	}
	l.Pop(1)
	compareNames(t, "global", live, documented)

	documented = map[string]bool{}
	for _, typ := range ref.Types {
		documented[typ.Name] = true
		for _, method := range typ.Methods {
			if !hasMethod(l, typ.Name, method.Name) {
				t.Errorf("\nwanted:\n%s:%s in the live state\ngot:\nno such method", typ.Name, method.Name)
			}
		}
	}
	live = map[string]bool{}
	l.PushValue(lua.RegistryIndex)
	l.PushNil()
	for l.Next(-2) {
		if l.TypeOf(-2) == lua.TypeString && l.IsTable(-1) {
			name, _ := l.ToString(-2)
			l.Field(-1, "__index")
			if l.IsGoFunction(-1) {
				live[name] = true
			}
			l.Pop(1)
		}
		l.Pop(1)
	}
	l.Pop(1)
	compareNames(t, "type", live, documented)
}

// TestReferenceEntriesAreDescribed checks that no entry has an empty summary or
// a signature that does not start with its parameter list.
func TestReferenceEntriesAreDescribed(t *testing.T) {
	ref := Reference()
	check := func(where string, entry Entry) {
		if strings.TrimSpace(entry.Summary) == "" {
			t.Errorf("\nwanted:\na summary for %s\ngot:\nnone", where)
		}
		if !strings.HasPrefix(entry.Signature, "(") {
			t.Errorf("\nwanted:\na signature starting with ( for %s\ngot:\n%q", where, entry.Signature)
		}
	}
	for _, hook := range ref.Hooks {
		check("hook "+hook.Name, hook)
	}
	for _, global := range ref.Globals {
		if global.Signature == "" {
			continue
		}
		check("global "+global.Name, global)
	}
	for _, library := range ref.Libraries {
		for _, function := range library.Functions {
			check(library.Name+":"+function.Name, function)
		}
	}
	for _, typ := range ref.Types {
		if strings.TrimSpace(typ.Summary) == "" {
			t.Errorf("\nwanted:\na summary for type %s\ngot:\nnone", typ.Name)
		}
		for _, method := range typ.Methods {
			check(typ.Name+":"+method.Name, method)
		}
	}
}

// TestReferenceCallsUseColon calls one function from each library with the
// colon form the reference documents, so a function that reads its first
// argument at index 1 fails here.
func TestReferenceCallsUseColon(t *testing.T) {
	ext, _ := setupTestExtension(t, "")
	calls := []string{
		`assert(marasi.strings:upper("abc") == "ABC")`,
		`assert(marasi.encoding.hex:encode("a") == "61")`,
		`assert(marasi.crypto:md5("a") == "0cc175b9c0f1b6a831c399e269772661")`,
		`assert(type(marasi.utils:uuid()) == "string")`,
		`assert(type(marasi.random:int(1, 2)) == "number")`,
	}
	for _, call := range calls {
		if err := ext.ExecuteLua(call); err != nil {
			t.Errorf("\nwanted:\n%s to run\ngot:\n%v", call, err)
		}
	}
}

func walkLibrary(l *lua.State, path string, found map[string]bool) {
	l.PushNil()
	for l.Next(-2) {
		name, _ := l.ToString(-2)
		switch {
		case l.IsGoFunction(-1):
			found[path+":"+name] = true
		case l.IsTable(-1):
			walkLibrary(l, path+"."+name, found)
		}
		l.Pop(1)
	}
}

func hasMethod(l *lua.State, typ, method string) bool {
	lua.MetaTableNamed(l, typ)
	defer l.Pop(1)
	l.Field(-1, "__index")
	l.PushNil()
	l.PushString(method)
	l.Call(2, 1)
	defer l.Pop(1)
	return l.IsGoFunction(-1)
}

func compareNames(t *testing.T, kind string, live, documented map[string]bool) {
	t.Helper()
	for _, name := range sortedNames(live) {
		if !documented[name] {
			t.Errorf("\nwanted:\na reference entry for %s %s\ngot:\nnone", kind, name)
		}
	}
	for _, name := range sortedNames(documented) {
		if !live[name] {
			t.Errorf("\nwanted:\n%s %s in the live state\ngot:\nonly in the reference", kind, name)
		}
	}
}

func sortedNames(names map[string]bool) []string {
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	return sorted
}
