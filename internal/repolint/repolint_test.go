package repolint

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// update rewrites baselineFile instead of checking against it.
var update = flag.Bool("update", false, "rewrite baseline.txt from the current violations")

// baselineFile lists the violations that predate the check, one per line as
// rule, file and quoted key separated by tabs. Keys are identifiers or
// strings, never line numbers, so unrelated edits do not churn it.
const baselineFile = "baseline.txt"

// updateCommand regenerates baselineFile.
const updateCommand = "GOWORK=off go test ./internal/repolint -update"

// rule names one mechanical rule of CODING_STANDARDS.md.
type rule string

const (
	ruleSubtestShould        rule = "subtest-should"
	ruleWrapColon            rule = "errorf-wrap-colon"
	ruleWrapLowercase        rule = "errorf-wrap-lowercase"
	ruleTransportCompression rule = "transport-disable-compression"
	ruleContextFirst         rule = "context-first"
	ruleSentinelName         rule = "sentinel-err-name"
)

// baselined reports whether violations of r may be listed in baselineFile.
// A transport without DisableCompression is never baselined: it is either
// fixed or exempted with a reviewed reason in transportExemptions.
func (r rule) baselined() bool {
	return r != ruleTransportCompression
}

// transportExemptions lists the http.Transport literals in non-test code that
// carry no captured, proxied, launched or Armory traffic, so the
// DisableCompression rule does not apply. The key is the file and the
// function that builds the literal; the value is the reason a reviewer
// accepted. See docs/agents/maps/request-path.md, "Who reaches the origin".
var transportExemptions = map[string]string{
	"chrome/cdp.go\tLauncher.LiveCDPEndpoint":  "reads the local Chrome DevTools /json/version endpoint, which is not proxy traffic",
	"internal/update/github.go\tNewHTTPClient": "fetches Marasi's own release metadata and assets from GitHub, which is not proxy traffic",
	"service/client.go\tNewEndpointClient":     "calls a service instance's control API over its socket or network address, which is not proxy traffic",
}

// violation is one breach of a rule at one place in the module.
type violation struct {
	rule    rule
	file    string // slash-separated, relative to the module root
	line    int
	key     string // the identifier or string the baseline keys on
	message string
	fix     string
}

func (v violation) entry() string {
	return string(v.rule) + "\t" + v.file + "\t" + strconv.Quote(v.key)
}

func (v violation) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s\n\tfix: %s", v.file, v.line, v.rule, v.message, v.fix)
}

// TestRepoRules checks every Go file in the module against the mechanical
// rules of CODING_STANDARDS.md. It fails on a violation missing from
// baseline.txt and on a baseline entry that no longer occurs, so the baseline
// only shrinks. After fixing violations, drop their entries with:
//
//	GOWORK=off go test ./internal/repolint -update
func TestRepoRules(t *testing.T) {
	root := moduleRoot(t)
	files, err := goFiles(root)
	if err != nil {
		t.Fatalf("listing Go files: %v", err)
	}
	var found []violation
	for _, file := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		vs, err := lintFile(file, src)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		found = append(found, vs...)
	}

	var live []violation
	usedExemptions := map[string]bool{}
	for _, v := range found {
		if v.rule == ruleTransportCompression {
			exemption := v.file + "\t" + v.key
			if _, ok := transportExemptions[exemption]; ok {
				usedExemptions[exemption] = true
				continue
			}
		}
		live = append(live, v)
	}
	for _, exemption := range sortedKeys(transportExemptions) {
		if !usedExemptions[exemption] {
			file, function, _ := strings.Cut(exemption, "\t")
			t.Errorf("%s: [%s] exemption for an http.Transport literal in %s matches no literal\n\tfix: delete its entry from transportExemptions in internal/repolint/repolint_test.go", file, ruleTransportCompression, function)
		}
	}

	if *update {
		var entries []string
		for _, v := range live {
			if v.rule.baselined() {
				entries = append(entries, v.entry())
			} else {
				t.Error(v)
			}
		}
		if err := writeBaseline(baselineFile, entries); err != nil {
			t.Fatalf("writing %s: %v", baselineFile, err)
		}
		return
	}

	baseline, err := readBaseline(baselineFile)
	if err != nil {
		t.Fatalf("reading %s: %v", baselineFile, err)
	}
	occurring := map[string]bool{}
	for _, v := range live {
		entry := v.entry()
		if baseline[entry] {
			occurring[entry] = true
			continue
		}
		t.Error(v)
	}
	for _, entry := range sortedKeys(baseline) {
		if occurring[entry] {
			continue
		}
		fields := strings.SplitN(entry, "\t", 3)
		t.Errorf("%s: [%s] baseline entry %s no longer occurs\n\tfix: run `%s` to drop it from internal/repolint/%s", fields[1], fields[0], fields[2], updateCommand, baselineFile)
	}
}

// moduleRoot returns the directory of the nearest go.mod above the test.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("finding go.mod above %s: none found", dir)
		}
		dir = parent
	}
}

// goFiles lists the module's Go files, relative to root and slash-separated,
// skipping what the go tool skips (testdata, and directories starting with
// "." or "_"), build output, and nested modules.
func goFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if name == "testdata" || name == "dist" || name == "vendor" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// lintFile returns the violations in one Go file, in source order.
func lintFile(file string, src []byte) ([]violation, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	l := &linter{
		fset:    fset,
		file:    file,
		isTest:  strings.HasSuffix(file, "_test.go"),
		imports: importNames(parsed),
	}
	for _, decl := range parsed.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			name := funcDeclName(decl)
			if !l.isTest {
				l.checkContextFirst(decl.Type, name, decl.Name.Pos())
			}
			l.inspect(decl, name)
		case *ast.GenDecl:
			l.checkGenDecl(decl)
			l.inspect(decl, "package scope")
		}
	}
	sort.SliceStable(l.found, func(i, j int) bool { return l.found[i].line < l.found[j].line })
	return l.found, nil
}

type linter struct {
	fset    *token.FileSet
	file    string
	isTest  bool
	imports map[string]string // import path to the name the file uses
	found   []violation
}

func (l *linter) report(r rule, pos token.Pos, key, message, fix string) {
	l.found = append(l.found, violation{
		rule:    r,
		file:    l.file,
		line:    l.fset.Position(pos).Line,
		key:     key,
		message: message,
		fix:     fix,
	})
}

// isPkgSel reports whether expr is pkg.name for the package imported from
// importPath.
func (l *linter) isPkgSel(expr ast.Expr, importPath, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	local := l.imports[importPath]
	return ok && local != "" && ident.Name == local
}

// inspect runs the expression-level checks on node, whose enclosing
// declaration is called scope.
func (l *linter) inspect(node ast.Node, scope string) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if l.isPkgSel(n.Fun, "fmt", "Errorf") && len(n.Args) > 0 {
				if format, ok := stringValue(n.Args[0]); ok {
					l.checkWrap(format, n.Args[0].Pos())
				}
			}
			if l.isTest {
				l.checkSubtest(n)
			}
		case *ast.CompositeLit:
			if !l.isTest && l.isPkgSel(n.Type, "net/http", "Transport") {
				l.checkTransport(n, scope)
			}
		}
		return true
	})
}

// checkWrap applies the Errors rule "Wrap with a lowercase gerund phrase" to
// an fmt.Errorf format that wraps with %w. A format starting with %w, such
// as "%w: %s", wraps a sentinel with detail and is left alone.
func (l *linter) checkWrap(format string, pos token.Pos) {
	if !strings.Contains(format, "%w") {
		return
	}
	if first, _ := utf8.DecodeRuneInString(format); unicode.IsUpper(first) {
		l.report(ruleWrapLowercase, pos, format,
			fmt.Sprintf("fmt.Errorf wrap message %q starts with an uppercase letter", format),
			`start it with a lowercase gerund phrase, such as "reading configuration: %w"`)
	}
	for i := 0; i < len(format); i++ {
		if !strings.HasPrefix(format[i:], "%w") || i == 0 {
			continue
		}
		before := format[:i]
		if strings.HasSuffix(before, ": ") && !strings.HasSuffix(strings.TrimSuffix(before, ": "), " ") {
			continue
		}
		l.report(ruleWrapColon, pos, format,
			fmt.Sprintf("fmt.Errorf wrap message %q does not put \": %%w\" directly after the phrase", format),
			fmt.Sprintf(`write the wrap as %q: no space before the colon, one space after it`, suggestWrap(format)))
		return
	}
}

// suggestWrap rewrites each separator before %w as ": ".
func suggestWrap(format string) string {
	var b strings.Builder
	for {
		i := strings.Index(format, "%w")
		if i < 0 {
			b.WriteString(format)
			return b.String()
		}
		before := format[:i]
		if i > 0 {
			before = strings.TrimRight(before, " :") + ": "
		}
		b.WriteString(before + "%w")
		format = format[i+2:]
	}
}

// checkSubtest applies the Tests rule "Subtests start with \"should\"" to a
// t.Run call whose name is a string constant.
func (l *linter) checkSubtest(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Run" || len(call.Args) != 2 {
		return
	}
	name, ok := stringValue(call.Args[0])
	if !ok || !l.isSubtestCall(sel.X, call.Args[1]) {
		return
	}
	if strings.HasPrefix(name, "should") {
		return
	}
	l.report(ruleSubtestShould, call.Args[0].Pos(), name,
		fmt.Sprintf("subtest name %q does not start with \"should\"", name),
		`rename it to "should …", stating the behaviour the subtest pins`)
}

// isSubtestCall reports whether recv.Run(_, fn) is testing.T.Run: the
// receiver is named t, or fn is a func literal taking a *testing.T.
func (l *linter) isSubtestCall(recv, fn ast.Expr) bool {
	if ident, ok := recv.(*ast.Ident); ok && ident.Name == "t" {
		return true
	}
	lit, ok := fn.(*ast.FuncLit)
	if !ok || len(lit.Type.Params.List) != 1 {
		return false
	}
	star, ok := lit.Type.Params.List[0].Type.(*ast.StarExpr)
	return ok && l.isPkgSel(star.X, "testing", "T")
}

// checkTransport applies the Proxy traffic rule "Add no headers" to an
// http.Transport literal built in function scope.
func (l *linter) checkTransport(lit *ast.CompositeLit, scope string) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "DisableCompression" {
			continue
		}
		if value, ok := kv.Value.(*ast.Ident); ok && value.Name == "false" {
			break
		}
		return
	}
	l.report(ruleTransportCompression, lit.Pos(), scope,
		fmt.Sprintf("http.Transport literal in %s does not set DisableCompression: true", scope),
		"add DisableCompression: true so Go's transport adds no Accept-Encoding (docs/adr/2026-10-19-the-proxy-adds-no-headers-to-a-request.md). "+
			"Only if it carries no captured, proxied, launched or Armory traffic, add an entry with the reason to transportExemptions in internal/repolint/repolint_test.go for review instead")
}

// checkContextFirst applies the rule "Context first" to the parameters of a
// function, method or function type called name.
func (l *linter) checkContextFirst(fn *ast.FuncType, name string, pos token.Pos) {
	index := 0
	for _, field := range fn.Params.List {
		count := max(len(field.Names), 1)
		if (index > 0 || count > 1) && l.isPkgSel(field.Type, "context", "Context") {
			l.report(ruleContextFirst, pos, name,
				fmt.Sprintf("%s takes a context.Context that is not its first parameter", name),
				"move the context.Context parameter to the front of the list and update the callers")
			return
		}
		index += count
	}
}

// checkGenDecl applies the declaration-level rules: "Context first" for
// function types and interface methods, and "Sentinel errors" for
// package-level errors.New values.
func (l *linter) checkGenDecl(decl *ast.GenDecl) {
	for _, spec := range decl.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			if l.isTest {
				continue
			}
			switch typ := spec.Type.(type) {
			case *ast.FuncType:
				l.checkContextFirst(typ, spec.Name.Name, spec.Name.Pos())
			case *ast.InterfaceType:
				for _, method := range typ.Methods.List {
					fn, ok := method.Type.(*ast.FuncType)
					if !ok || len(method.Names) == 0 {
						continue
					}
					l.checkContextFirst(fn, spec.Name.Name+"."+method.Names[0].Name, method.Names[0].Pos())
				}
			}
		case *ast.ValueSpec:
			if decl.Tok != token.VAR {
				continue
			}
			for i, name := range spec.Names {
				if i >= len(spec.Values) || name.Name == "_" {
					continue
				}
				call, ok := spec.Values[i].(*ast.CallExpr)
				if !ok || !l.isPkgSel(call.Fun, "errors", "New") {
					continue
				}
				if strings.HasPrefix(name.Name, "Err") || strings.HasPrefix(name.Name, "err") {
					continue
				}
				l.report(ruleSentinelName, name.Pos(), name.Name,
					fmt.Sprintf("package-level errors.New value %s is not named Err… or err…", name.Name),
					fmt.Sprintf("rename it to Err%s if exported or err%s if not, and compare it with errors.Is", upperFirst(name.Name), upperFirst(name.Name)))
			}
		}
	}
}

// importNames maps each import path of file to the name the file uses for it.
func importNames(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(importPath)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		names[importPath] = name
	}
	return names
}

// funcDeclName returns Name for a function and Type.Name for a method.
func funcDeclName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return decl.Name.Name
	}
	recv := decl.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	switch typ := recv.(type) {
	case *ast.IndexExpr:
		recv = typ.X
	case *ast.IndexListExpr:
		recv = typ.X
	}
	if ident, ok := recv.(*ast.Ident); ok {
		return ident.Name + "." + decl.Name.Name
	}
	return decl.Name.Name
}

// stringValue returns the value of a string literal or a + concatenation of
// string literals.
func stringValue(expr ast.Expr) (string, bool) {
	switch expr := expr.(type) {
	case *ast.BasicLit:
		if expr.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(expr.Value)
		return value, err == nil
	case *ast.BinaryExpr:
		if expr.Op != token.ADD {
			return "", false
		}
		left, ok := stringValue(expr.X)
		if !ok {
			return "", false
		}
		right, ok := stringValue(expr.Y)
		return left + right, ok
	case *ast.ParenExpr:
		return stringValue(expr.X)
	}
	return "", false
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readBaseline(name string) (map[string]bool, error) {
	f, err := os.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("parsing entry %q: want rule, file and key separated by tabs", line)
		}
		if !rule(fields[0]).baselined() {
			return nil, fmt.Errorf("parsing entry %q: rule %s cannot be baselined", line, fields[0])
		}
		entries[line] = true
	}
	return entries, scanner.Err()
}

func writeBaseline(name string, entries []string) error {
	sort.Strings(entries)
	var b strings.Builder
	b.WriteString("# Violations of CODING_STANDARDS.md that predate internal/repolint.\n")
	b.WriteString("# One per line: rule, file and quoted key, separated by tabs. Do not add\n")
	b.WriteString("# entries by hand; fix the code, then regenerate with:\n")
	b.WriteString("#\t" + updateCommand + "\n")
	previous := ""
	for _, entry := range entries {
		if entry == previous {
			continue
		}
		b.WriteString(entry + "\n")
		previous = entry
	}
	return os.WriteFile(name, []byte(b.String()), 0o644)
}

func TestLintFile(t *testing.T) {
	cases := []struct {
		name string
		file string
		src  string
		want []string // "rule key"
	}{
		{
			name: "should report a subtest name that does not start with should",
			file: "a_test.go",
			src:  `package a; import "testing"; func TestA(t *testing.T) { t.Run("returns ok", func(t *testing.T) {}); t.Run("should return ok", func(t *testing.T) {}) }`,
			want: []string{`subtest-should "returns ok"`},
		},
		{
			name: "should ignore t.Run in non-test files",
			file: "a.go",
			src:  `package a; func f(t interface{ Run(string, func()) }) { t.Run("x", func() {}) }`,
		},
		{
			name: "should report a space before the wrap colon and a missing colon",
			file: "a.go",
			src:  `package a; import "fmt"; func f(err error) { _ = fmt.Errorf("reading x : %w", err); _ = fmt.Errorf("reading x %w", err); _ = fmt.Errorf("reading x: %w", err); _ = fmt.Errorf("%w: %s", err, "x") }`,
			want: []string{`errorf-wrap-colon "reading x : %w"`, `errorf-wrap-colon "reading x %w"`},
		},
		{
			name: "should report a wrap message that starts with an uppercase letter",
			file: "a.go",
			src:  `package a; import "fmt"; func f(err error) { _ = fmt.Errorf("Reading x: %w", err); _ = fmt.Errorf("Reading x: %v", err) }`,
			want: []string{`errorf-wrap-lowercase "Reading x: %w"`},
		},
		{
			name: "should report an http.Transport literal without DisableCompression in non-test code",
			file: "a.go",
			src:  `package a; import nethttp "net/http"; func f() { _ = &nethttp.Transport{}; _ = &nethttp.Transport{DisableCompression: false}; _ = &nethttp.Transport{DisableCompression: true} }`,
			want: []string{`transport-disable-compression "f"`, `transport-disable-compression "f"`},
		},
		{
			name: "should report a context.Context that is not the first parameter",
			file: "a.go",
			src:  `package a; import "context"; type S struct{}; func (S) M(a, ctx context.Context) {}; func g(ctx context.Context, a int) {}; type I interface{ M(int, context.Context) }; type F func(int, context.Context)`,
			want: []string{`context-first "S.M"`, `context-first "I.M"`, `context-first "F"`},
		},
		{
			name: "should report a package-level errors.New value not named Err",
			file: "a.go",
			src:  `package a; import "errors"; var notFound = errors.New("not found"); var ErrGone, errLost = errors.New("gone"), errors.New("lost")`,
			want: []string{`sentinel-err-name "notFound"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found, err := lintFile(c.file, []byte(c.src))
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			var got []string
			for _, v := range found {
				got = append(got, string(v.rule)+" "+strconv.Quote(v.key))
			}
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", strings.Join(c.want, "\n"), strings.Join(got, "\n"))
			}
		})
	}
}
