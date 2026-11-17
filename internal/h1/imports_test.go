package h1_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsOnlyStdlibAndXNet pins the spec's rule that the proxy library
// imports only the standard library and golang.org/x/net, so its behaviour is
// Marasi's to read and change.
func TestImportsOnlyStdlibAndXNet(t *testing.T) {
	t.Run("should import only the standard library and golang.org/x/net", func(t *testing.T) {
		files, err := filepath.Glob("*.go")
		if err != nil {
			t.Fatalf("listing files: %v", err)
		}
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				first, _, _ := strings.Cut(path, "/")
				if strings.Contains(first, ".") && path != "golang.org/x/net" && !strings.HasPrefix(path, "golang.org/x/net/") {
					t.Fatalf("\nwanted:\nstandard library or golang.org/x/net\ngot:\n%s imports %s", name, path)
				}
			}
		}
	})
}
