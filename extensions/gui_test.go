package extensions

import (
	"reflect"
	"strings"
	"testing"
)

func TestWithGUI(t *testing.T) {
	t.Run("should pass render and update calls to the handlers", func(t *testing.T) {
		type call struct {
			name  string
			key   string
			value any
		}
		var calls []call
		ext, _ := setupTestExtension(t, "", WithGUI(GUI{
			Type:   "marasi-test",
			Render: func(target string, schema any) { calls = append(calls, call{"render", target, schema}) },
			Update: func(key string, value any) { calls = append(calls, call{"update", key, value}) },
		}))

		err := ext.ExecuteLua(`assert(marasi.gui.type == "marasi-test")
marasi.gui:render("main", {title = "Scan", items = {"a", "b"}})
marasi.gui:update("progress", 42)`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []call{
			{"render", "main", map[string]any{"title": "Scan", "items": []any{"a", "b"}}},
			{"update", "progress", float64(42)},
		}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("\nwanted:\n%#v\ngot:\n%#v", want, calls)
		}
	})

	t.Run("should reject a render schema that is not a table", func(t *testing.T) {
		ext, _ := setupTestExtension(t, "", WithGUI(GUI{Type: "marasi-test", Render: func(string, any) {}, Update: func(string, any) {}}))

		if err := ext.ExecuteLua(`marasi.gui:render("main", "not a table")`); err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should return error if the extension already has a gui library", func(t *testing.T) {
		gui := GUI{Type: "marasi-test", Render: func(string, any) {}, Update: func(string, any) {}}
		ext, _ := setupTestExtension(t, "", WithGUI(gui))

		err := WithGUI(gui)(ext)
		if err == nil || !strings.Contains(err.Error(), "already has a gui library") {
			t.Fatalf("\nwanted:\nerror containing 'already has a gui library'\ngot:\n%v", err)
		}
	})
}
