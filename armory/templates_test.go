package armory

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTemplateCtx_Payload(t *testing.T) {
	t.Run("should return the payload assigned to a position", func(t *testing.T) {
		ctx := templateCtx{payloads: map[int]string{0: "injected"}}

		got, err := ctx.Payload(0, "fallback")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != "injected" {
			t.Fatalf("\nwanted:\ninjected\ngot:\n%s", got)
		}
	})

	t.Run("should return the fallback for an unassigned position", func(t *testing.T) {
		ctx := templateCtx{payloads: map[int]string{}}

		got, err := ctx.Payload(0, "fallback")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != "fallback" {
			t.Fatalf("\nwanted:\nfallback\ngot:\n%s", got)
		}
	})

	t.Run("should preserve an assigned empty payload", func(t *testing.T) {
		ctx := templateCtx{payloads: map[int]string{0: ""}}

		got, err := ctx.Payload(0, "fallback")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != "" {
			t.Fatalf("\nwanted:\nempty string\ngot:\n%s", got)
		}
	})

	t.Run("should return an error for a negative position", func(t *testing.T) {
		ctx := templateCtx{}

		_, err := ctx.Payload(-1, "fallback")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "payload position cannot be negative") {
			t.Fatalf("\nwanted:\nerror containing 'payload position cannot be negative'\ngot:\n%v", err)
		}
	})
}

func TestPreprocessTemplate(t *testing.T) {
	t.Run("should expand payload positions in order", func(t *testing.T) {
		raw := "username=@@admin@@&password=@@secret@@"
		want := `username={{.Payload 0 "admin"}}&password={{.Payload 1 "secret"}}`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 2 {
			t.Fatalf("\nwanted:\n2\ngot:\n%d", positionCount)
		}
	})

	t.Run("should expand chained payload functions", func(t *testing.T) {
		raw := "value=@@username@@.upper().reverse()"
		want := `value={{.Payload 0 "username" | upper | reverse}}`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve a non-function suffix", func(t *testing.T) {
		got, _, err := preprocessTemplate("value=@@username@@.example")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != `value={{.Payload 0 "username"}}.example` {
			t.Fatalf("\nwanted:\nvalue={{.Payload 0 \"username\"}}.example\ngot:\n%s", got)
		}
	})

	t.Run("should reject an undefined chained function", func(t *testing.T) {
		_, _, err := preprocessTemplate("@@username@@.missing()")
		if err == nil || !strings.Contains(err.Error(), "armory function missing is not defined") {
			t.Fatalf("\nwanted:\nundefined function error\ngot:\n%v", err)
		}
	})

	t.Run("should reject a function with an incompatible chain signature", func(t *testing.T) {
		_, _, err := preprocessTemplate("@@username@@.uuid()")
		if err == nil || !strings.Contains(err.Error(), "armory function uuid cannot be chained") {
			t.Fatalf("\nwanted:\nincompatible function error\ngot:\n%v", err)
		}
	})

	t.Run("should preserve a literal payload marker", func(t *testing.T) {
		raw := `value=\@\@`
		want := "value=@@"

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve a literal at sign", func(t *testing.T) {
		raw := `email=user\@example.com`
		want := "email=user@example.com"

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve a literal backslash", func(t *testing.T) {
		raw := `value=\\`
		want := `value=\`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve escaped marker syntax literally", func(t *testing.T) {
		raw := `value=\\\@\\\@`
		want := `value=\@\@`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve a backslash before a payload position", func(t *testing.T) {
		raw := `value=\\@@fallback@@`
		want := `value=\{{.Payload 0 "fallback"}}`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve escapes inside a fallback", func(t *testing.T) {
		raw := `value=@@prefix-\@\@-\\-suffix@@`
		want := `value={{.Payload 0 "prefix-@@-\\-suffix"}}`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if positionCount != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve unknown escapes", func(t *testing.T) {
		raw := `value=\n\t\x`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != raw {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", raw, got)
		}
		if positionCount != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", positionCount)
		}
	})

	t.Run("should preserve a trailing backslash", func(t *testing.T) {
		raw := `value=\`

		got, positionCount, err := preprocessTemplate(raw)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != raw {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", raw, got)
		}
		if positionCount != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", positionCount)
		}
	})

	t.Run("should return an error for an unclosed position", func(t *testing.T) {
		_, _, err := preprocessTemplate("value=@@fallback")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "payload position 0 is not closed") {
			t.Fatalf("\nwanted:\nerror containing 'payload position 0 is not closed'\ngot:\n%v", err)
		}
	})
}

func TestParseTemplate(t *testing.T) {
	t.Run("should parse a preprocessed request template", func(t *testing.T) {
		tmpl, err := parseTemplate("username=@@admin@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if tmpl.positionCount != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", tmpl.positionCount)
		}
	})

	t.Run("should count direct payload syntax", func(t *testing.T) {
		tmpl, err := parseTemplate(`username={{.Payload 0 "admin" | printf "%s"}}`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if tmpl.positionCount != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", tmpl.positionCount)
		}
	})

	t.Run("should count a nested payload call", func(t *testing.T) {
		tmpl, err := parseTemplate(`username={{printf "%s" (.Payload 0 "admin")}}`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if tmpl.positionCount != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", tmpl.positionCount)
		}
	})

	t.Run("should count payload calls in branches", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want int
		}{
			{
				name: "if",
				raw:  `{{if true}}{{.Payload 0 "admin"}}{{else}}{{.Payload 1 "secret"}}{{end}}`,
				want: 2,
			},
			{
				name: "range",
				raw:  `{{range .Payloads}}{{.Payload 0 "admin"}}{{else}}{{.Payload 1 "secret"}}{{end}}`,
				want: 2,
			},
			{
				name: "with",
				raw:  `{{with .Payloads}}{{.Payload 0 "admin"}}{{else}}{{.Payload 1 "secret"}}{{end}}`,
				want: 2,
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				tmpl, err := parseTemplate(test.raw)
				if err != nil {
					t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
				}
				if tmpl.positionCount != test.want {
					t.Fatalf("\nwanted:\n%d\ngot:\n%d", test.want, tmpl.positionCount)
				}
			})
		}
	})

	t.Run("should count payload calls in defined templates", func(t *testing.T) {
		tmpl, err := parseTemplate(`{{define "value"}}{{.Payload 0 "admin"}}{{end}}{{template "value"}}`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if tmpl.positionCount != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", tmpl.positionCount)
		}
	})

	t.Run("should count marker and direct payload positions", func(t *testing.T) {
		tmpl, err := parseTemplate(`username=@@admin@@&password={{.Payload 1 "secret"}}`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if tmpl.positionCount != 2 {
			t.Fatalf("\nwanted:\n2\ngot:\n%d", tmpl.positionCount)
		}
	})

	t.Run("should return an error for a duplicate payload position", func(t *testing.T) {
		_, err := parseTemplate(`@@admin@@{{.Payload 0 "secret"}}`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "payload position 0 is declared more than once") {
			t.Fatalf("\nwanted:\nerror containing 'payload position 0 is declared more than once'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error for duplicate direct payload positions", func(t *testing.T) {
		_, err := parseTemplate(`{{.Payload 0 "admin"}}{{.Payload 0 "secret"}}`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "payload position 0 is declared more than once") {
			t.Fatalf("\nwanted:\nerror containing 'payload position 0 is declared more than once'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error for a missing payload position", func(t *testing.T) {
		_, err := parseTemplate(`{{.Payload 0 "admin"}}{{.Payload 2 "secret"}}`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "payload position 1 is missing") {
			t.Fatalf("\nwanted:\nerror containing 'payload position 1 is missing'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error for a dynamic payload position", func(t *testing.T) {
		_, err := parseTemplate(`{{$position := 0}}{{.Payload $position "admin"}}`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), ".Payload position must be a static integer") {
			t.Fatalf("\nwanted:\nerror containing '.Payload position must be a static integer'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error for missing payload arguments", func(t *testing.T) {
		_, err := parseTemplate(`{{.Payload 0}}`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), ".Payload requires a position and fallback value") {
			t.Fatalf("\nwanted:\nerror containing '.Payload requires a position and fallback value'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error for a negative payload position", func(t *testing.T) {
		_, err := parseTemplate(`{{.Payload -1 "admin"}}`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), ".Payload position cannot be negative") {
			t.Fatalf("\nwanted:\nerror containing '.Payload position cannot be negative'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error for invalid Go template syntax", func(t *testing.T) {
		_, err := parseTemplate("{{if}}")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "parsing armory template") {
			t.Fatalf("\nwanted:\nerror containing 'parsing armory template'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error for an undefined manual function", func(t *testing.T) {
		_, err := parseTemplate(`{{missing "value"}}`)
		if err == nil || !strings.Contains(err.Error(), `function "missing" not defined`) {
			t.Fatalf("\nwanted:\nundefined function error\ngot:\n%v", err)
		}
	})
}

func TestArmoryTemplate_Render(t *testing.T) {
	t.Run("should render assigned payloads and fallbacks", func(t *testing.T) {
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("parsing template: %v", err)
		}

		got, err := tmpl.render(map[int]string{0: "injected"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "username=injected&password=secret"
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should render chained payload functions", func(t *testing.T) {
		tmpl, err := parseTemplate("value=@@fallback@@.upper().reverse()")
		if err != nil {
			t.Fatalf("parsing template: %v", err)
		}

		got, err := tmpl.render(map[int]string{0: "marasi"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != "value=ISARAM" {
			t.Fatalf("\nwanted:\nvalue=ISARAM\ngot:\n%s", got)
		}
	})

	t.Run("should expose manual template functions", func(t *testing.T) {
		tmpl, err := parseTemplate(`value={{" marasi " | trim | upper | replace "MAR" "mar"}}`)
		if err != nil {
			t.Fatalf("parsing template: %v", err)
		}

		got, err := tmpl.render(nil)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != "value=marASI" {
			t.Fatalf("\nwanted:\nvalue=marASI\ngot:\n%s", got)
		}
	})

	t.Run("should generate UUIDv7 values", func(t *testing.T) {
		tmpl, err := parseTemplate(`{{uuid}}`)
		if err != nil {
			t.Fatalf("parsing template: %v", err)
		}

		got, err := tmpl.render(nil)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		id, err := uuid.Parse(got)
		if err != nil {
			t.Fatalf("parsing generated UUID: %v", err)
		}
		if id.Version() != 7 {
			t.Fatalf("\nwanted:\nUUIDv7\ngot:\nUUIDv%d", id.Version())
		}
	})

	t.Run("should render a formatted direct payload", func(t *testing.T) {
		tmpl, err := parseTemplate(`value={{.Payload 0 "fallback" | printf "<%s>"}}`)
		if err != nil {
			t.Fatalf("parsing template: %v", err)
		}

		got, err := tmpl.render(map[int]string{0: "injected"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != "value=<injected>" {
			t.Fatalf("\nwanted:\nvalue=<injected>\ngot:\n%s", got)
		}
	})

	t.Run("should render an assigned empty payload", func(t *testing.T) {
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("parsing template: %v", err)
		}

		got, err := tmpl.render(map[int]string{0: ""})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got != "value=" {
			t.Fatalf("\nwanted:\nvalue=\ngot:\n%s", got)
		}
	})

	t.Run("should return an error when rendering fails", func(t *testing.T) {
		tmpl, err := parseTemplate(`{{index "x" 2}}`)
		if err != nil {
			t.Fatalf("parsing template: %v", err)
		}

		_, err = tmpl.render(nil)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "rendering armory template") {
			t.Fatalf("\nwanted:\nerror containing 'rendering armory template'\ngot:\n%v", err)
		}
	})
}

func TestTemplateFunctions(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "lower", raw: `{{"MARASI" | lower}}`, want: "marasi"},
		{name: "reverse unicode", raw: `{{"marasi界" | reverse}}`, want: "界isaram"},
		{name: "base64", raw: `{{"marasi" | base64}}`, want: "bWFyYXNp"},
		{name: "hex", raw: `{{"marasi" | hex}}`, want: "6d6172617369"},
		{name: "sha256", raw: `{{"marasi" | sha256}}`, want: "35c7134d79db008dee1ad4438c69196aaeda57d591d2507c80db8eaf01386a51"},
		{name: "urlencode", raw: `{{"hello world/" | urlencode}}`, want: "hello+world%2F"},
		{name: "pathescape", raw: `{{"hello world/" | pathescape}}`, want: "hello%20world%2F"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpl, err := parseTemplate(test.raw)
			if err != nil {
				t.Fatalf("parsing template: %v", err)
			}
			got, err := tmpl.render(nil)
			if err != nil {
				t.Fatalf("rendering template: %v", err)
			}
			if got != test.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", test.want, got)
			}
		})
	}
}
