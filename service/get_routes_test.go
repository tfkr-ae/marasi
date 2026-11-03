package service

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi"
)

// readOnlyGetRoutes is every control API route served on GET, each audited to
// change no state, because a page can send a GET with no Origin through
// <img src> (docs/adr/2026-11-03-the-control-listener-network-address-is-guarded-by-an-optional-password.md).
// The key is the registered pattern, and the value the paths that reach it.
// A pattern with no method answers only GET in its handler. A nil value is
// audited but not served here:
//   - GET /certificate reads the CA certificate the proxy loaded at start,
//     which the test proxy has not loaded.
//   - GET /status and GET /token live on the control address, which the test
//     server does not have. GET /token writes the control token file when it
//     is missing. It is served only on the socket, and goes with the control
//     token, which the control password replaces.
var readOnlyGetRoutes = map[string][]string{
	"GET /armory/run/{id}":                   {"/armory/run/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /armory/run/{id}/traffic":           {"/armory/run/0190f0c4-7b6e-7000-8000-000000000001/traffic"},
	"GET /armory/template":                   {"/armory/template"},
	"GET /armory/template/{id}":              {"/armory/template/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /armory/template/{id}/run":          {"/armory/template/0190f0c4-7b6e-7000-8000-000000000001/run"},
	"GET /artifact":                          {"/artifact"},
	"GET /artifact/{id}":                     {"/artifact/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /artifact/{id}/content":             {"/artifact/0190f0c4-7b6e-7000-8000-000000000001/content"},
	"GET /certificate":                       nil,
	"GET /checkpoint":                        {"/checkpoint"},
	"GET /checkpoint/{id}":                   {"/checkpoint/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /chrome/path":                       {"/chrome/path"},
	"GET /chrome/profile":                    {"/chrome/profile"},
	"GET /client-settings/{client}":          {"/client-settings/app"},
	"GET /client-settings/{client}/{key...}": {"/client-settings/app/theme"},
	"GET /dashboard":                         {"/dashboard"},
	"GET /extension":                         {"/extension"},
	"GET /extension/{id}":                    {"/extension/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /extension/{id}/logs":               {"/extension/0190f0c4-7b6e-7000-8000-000000000001/logs"},
	"GET /extension/{id}/settings":           {"/extension/0190f0c4-7b6e-7000-8000-000000000001/settings"},
	"GET /extension/{id}/ui":                 {"/extension/0190f0c4-7b6e-7000-8000-000000000001/ui"},
	"GET /finding":                           {"/finding"},
	"GET /finding/{id}":                      {"/finding/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /jobs":                              {"/jobs"},
	"GET /launch":                            {"/launch"},
	"GET /launch/{id}":                       {"/launch/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /launchpad":                         {"/launchpad"},
	"GET /launchpad/{id}":                    {"/launchpad/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /notes":                             {"/notes"},
	"GET /notes/{id}":                        {"/notes/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /report/template":                   {"/report/template"},
	"GET /scope":                             {"/scope"},
	"GET /status":                            nil,
	"GET /test-case":                         {"/test-case"},
	"GET /test-case/checklist":               {"/test-case/checklist"},
	"GET /test-case/{id}":                    {"/test-case/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /token":                             nil,
	"GET /traffic":                           {"/traffic"},
	"GET /traffic/content-types":             {"/traffic/content-types"},
	"GET /traffic/count":                     {"/traffic/count"},
	"GET /traffic/count/by-field":            {"/traffic/count/by-field?field=host"},
	"GET /traffic/query":                     {"/traffic/query?q=host"},
	"GET /traffic/sitemap":                   {"/traffic/sitemap"},
	"GET /traffic/{id}":                      {"/traffic/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /traffic/{id}/metadata":             {"/traffic/0190f0c4-7b6e-7000-8000-000000000001/metadata"},
	"GET /traffic/{request_id}/websocket":    {"/traffic/0190f0c4-7b6e-7000-8000-000000000001/websocket"},
	"GET /update":                            {"/update"},
	"GET /upstream-proxy":                    {"/upstream-proxy"},
	"GET /waypoint":                          {"/waypoint"},
	"GET /websocket":                         {"/websocket"},
	"GET /websocket/{connection_id}":         {"/websocket/0190f0c4-7b6e-7000-8000-000000000001"},
	"GET /websocket/{connection_id}/message": {"/websocket/0190f0c4-7b6e-7000-8000-000000000001/message"},
	"GET /wordlist":                          {"/wordlist"},
	"GET /wordlist/":                         {"/wordlist/"},
	"GET /wordlist/{name}":                   {"/wordlist/common.txt"},
	"/events":                                {"/events"},
	"/interfaces":                            {"/listener/interfaces"},
	"/list":                                  {"/project/list"},
	"/logs":                                  {"/logs"},
	"/status":                                {"/service/status", "/listener/status"},
}

// nonGetRoutes are the patterns with no method that refuse GET in their
// handler, each with a path that reaches it.
var nonGetRoutes = map[string]string{
	"/open":   "/project/open",
	"/start":  "/listener/start",
	"/stop":   "/listener/stop",
	"/update": "/listener/update",
}

func TestGetRoutesChangeNoState(t *testing.T) {
	t.Run("should list every route the control API serves on GET as audited", func(t *testing.T) {
		var unaudited []string
		for _, pattern := range registeredGetPatterns(t) {
			_, readOnly := readOnlyGetRoutes[pattern]
			_, nonGet := nonGetRoutes[pattern]
			if !readOnly && !nonGet {
				unaudited = append(unaudited, pattern)
			}
		}
		if len(unaudited) != 0 {
			t.Fatalf("\nwanted:\nevery GET route audited in readOnlyGetRoutes\ngot:\nunaudited %v", unaudited)
		}
	})

	t.Run("should refuse GET on a route with no method that changes state", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{ConfigDir: t.TempDir()}, func() {})
		for _, pattern := range slices.Sorted(maps.Keys(nonGetRoutes)) {
			response := httptest.NewRecorder()

			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, nonGetRoutes[pattern], nil))

			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("\nwanted:\nGET %s %d\ngot:\n%d", nonGetRoutes[pattern], http.StatusMethodNotAllowed, response.Code)
			}
		}
	})

	t.Run("should leave the config dir and events unchanged when every GET route is served", func(t *testing.T) {
		configDir := t.TempDir()
		proxy, err := marasi.New(marasi.WithConfigDir(configDir))
		if err != nil {
			t.Fatal(err)
		}
		server := newTestServer(proxy, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		before := configDirState(t, configDir)

		for _, pattern := range slices.Sorted(maps.Keys(readOnlyGetRoutes)) {
			for _, path := range readOnlyGetRoutes[pattern] {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				if pattern == "/events" {
					// The stream runs until its client leaves.
					ctx, cancel := context.WithCancel(request.Context())
					cancel()
					request = request.WithContext(ctx)
				}
				server.ServeHTTP(httptest.NewRecorder(), request)
			}
		}

		after := configDirState(t, configDir)
		var changed []string
		for name := range maps.Keys(after) {
			if content, ok := before[name]; !ok || content != after[name] {
				changed = append(changed, name)
			}
		}
		for name := range maps.Keys(before) {
			if _, ok := after[name]; !ok {
				changed = append(changed, name)
			}
		}
		slices.Sort(changed)
		if len(changed) != 0 {
			t.Fatalf("\nwanted:\nno config dir file changed\ngot:\nchanged %v", changed)
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%v", event)
		default:
		}
	})
}

// registeredGetPatterns returns the GET patterns and the method-less
// patterns the service package registers, read from its source so a route
// added later cannot skip the audit.
func registeredGetPatterns(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var patterns []string
	fileSet := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "HandleFunc" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			// A pattern with no method also matches GET.
			if strings.HasPrefix(pattern, "GET ") || strings.HasPrefix(pattern, "/") {
				patterns = append(patterns, pattern)
			}
			return true
		})
	}
	return patterns
}

// configDirState maps each file under dir to its contents. Lock files are
// left out: taking a lock creates an empty one, which holds no state.
func configDirState(t *testing.T, dir string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.HasSuffix(path, ".lock") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		state[relative] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
