package marasi

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/rawhttp"
)

func testBrotliBody(t *testing.T, content string) (io.ReadCloser, int) {
	t.Helper()
	var buf bytes.Buffer
	bw := brotli.NewWriter(&buf)
	if _, err := bw.Write([]byte(content)); err != nil {
		t.Fatalf("writing brotli data: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("reading brotli data: %v", err)
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes())), buf.Len()
}

func testGzipBody(t *testing.T, content string) (io.ReadCloser, int) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(content)); err != nil {
		t.Fatalf("writing gzip data: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writier: %v", err)
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes())), buf.Len()
}

func testResponse(body string) *http.Response {
	return &http.Response{
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader([]byte(body))),
		ContentLength: int64(len(body)),
	}
}

var forcedErr = errors.New("forced error")

// erroringReader will return an error on Reads
type erroringReader struct{}

func (er *erroringReader) Read(p []byte) (n int, err error) {
	return 0, forcedErr
}

func (er *erroringReader) Close() error {
	return nil
}

var testExtensions = map[string]*Extension{
	"compass": {
		Name: "compass",
		ID:   uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6"),
		LuaContent: `
			local scope = marasi:scope()
			scope:clear_rules()
			scope:add_rule("-blocked\\.com", "host")

			function processRequest(request)
			  if not scope:matches(request) then
				  request:Skip()
			  end
			end

			function processResponse(response)
			  if not scope:matches(response) then
				  response:Skip()
			  end
			end 
		`,
	},
	"workshop": {
		Name: "workshop",
		ID:   uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43"),
		LuaContent: `
			function processRequest(request)
				request:Headers():Set("x-workshop-ran", "true")
			end

			function processResponse(response)
				response:Headers():Set("x-workshop-ran-response", "true")
			end
		`,
	},
	"checkpoint": {
		Name: "checkpoint",
		ID:   uuid.MustParse("01937d13-9632-75b1-9e73-c5129b06fa8c"),
		LuaContent: `
			function interceptRequest(request)
				return false
			end

			function interceptResponse(response)
				return false
			end
		`,
	},
	"testExtension": {
		Name: "testExtension", ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		LuaContent: `
			function processRequest(request)
			  request:Headers():Set("x-testExtension-ran", "true")
			end

			function processResponse(response)
			  response:Headers():Set("x-testExtension-ran-response", "true")
			end
		`,
	},
}

func newTestProxy(t *testing.T, extensions ...*Extension) *Proxy {
	t.Helper()

	proxy := &Proxy{
		Scope:          NewScope(true),
		Extensions:     make([]*Extension, 0),
		DBWriteChannel: make(chan ProxyItem, 10),
	}

	onLogHandler := func(log ExtensionLog) error { return nil }

	for _, ext := range extensions {
		ext := &Extension{
			ID:         ext.ID,
			Name:       ext.Name,
			LuaContent: ext.LuaContent,
		}
		err := proxy.WithOptions(WithExtension(ext, ExtensionWithLogHandler(onLogHandler)))
		if err != nil {
			t.Fatalf("setting up %s : %v", ext.Name, err)
		}
	}
	return proxy
}

func updateExtension(t *testing.T, proxy *Proxy, name string, luaCode string) {
	t.Helper()
	if ext, ok := proxy.GetExtension(name); ok {
		err := ext.ExecuteLua(luaCode)
		if err != nil {
			t.Fatalf("updating %s extension: %v", name, err)
		}
	} else {
		t.Fatalf("getting %s extension", name)
	}
}

// RequestModifiers
func TestPreventLoopModifier(t *testing.T) {
	t.Run("request to 127.0.0.1:8080 with listener on 127.0.0.1:8080 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "127.0.0.1",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to localhost:8080 with listener on 127.0.0.1:8080 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "127.0.0.1",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to localhost:8080 with listener on localhost:8080 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "localhost",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to 127.0.0.1:8080 with listener on localhost:8080 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "localhost",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to [::1]:8080 with listener on [::1]:8080 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "::1",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://[::1]:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to 192.168.1.10:8080 with listener on 192.168.1.10:8080 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "192.168.1.10",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to 192.168.1.10:8080 with listener on localhost:8080 should work", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "localhost",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: False\ngot: %t", ctx.SkippingRoundTrip())
		}
	})

	t.Run("request to https://localhost with listener on localhost:443 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "localhost",
			Port: "443",
		}
		req := httptest.NewRequest(http.MethodGet, "https://localhost/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to http://localhost with listener on localhost:80 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "localhost",
			Port: "80",
		}
		req := httptest.NewRequest(http.MethodGet, "http://localhost/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
	t.Run("request to 192.168.1.10:8081 with listener on 192.168.1.10:8080 should work", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "192.168.1.10",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:8081/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: False\ngot: %t", ctx.SkippingRoundTrip())
		}
	})

	t.Run("request to marasi.app:8080 with listener on marasi.app:8080 should fail", func(t *testing.T) {
		proxy := &Proxy{
			Addr: "marasi.app",
			Port: "8080",
		}
		req := httptest.NewRequest(http.MethodGet, "http://marasi.app:8080/path", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		err = PreventLoopModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})
}

func TestSkipConnectModifier(t *testing.T) {
	t.Run("request with CONNECT method should be skipped by marasi", func(t *testing.T) {
		proxy := &Proxy{}
		req := httptest.NewRequest(http.MethodConnect, "https://marasi.app", nil)

		err := SkipConnectRequestModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
	})

	t.Run("request with method other than CONNECT should be processed", func(t *testing.T) {
		proxy := &Proxy{}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		err := SkipConnectRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %q", err)
		}
	})
}

func TestCompassRequestModifier(t *testing.T) {
	t.Run("request that matches blocked rule should be skipped", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		req := httptest.NewRequest(http.MethodGet, "https://www.blocked.com/examplePage", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = CompassRequestModifier(proxy, req)

		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}

		if skip, ok := SkipFlagFromContext(req.Context()); !ok || !skip {
			t.Errorf("expected skipped flag to be set in context and true")
		}
	})

	t.Run("request that doesn't match rule should be allowed when default policy is true", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = CompassRequestModifier(proxy, req)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if skip, ok := SkipFlagFromContext(req.Context()); ok && skip {
			t.Errorf("expected skipflag to not be set and should not be true")
		}
	})

	t.Run("request that doesn't match rule should be skipped when default policy is false", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		proxy.Scope.DefaultAllow = false
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = CompassRequestModifier(proxy, req)

		if err == nil {
			t.Fatalf("wanted: %v\ngot: nil", ErrSkipPipeline)
		}

		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}

		if skip, ok := SkipFlagFromContext(req.Context()); !ok || !skip {
			t.Errorf("expected skipflag to be set in context and to be equal to true")
		}
	})

	t.Run("request that matches blocked rule should be dropped when :Drop() method is used", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		updateExtension(t, proxy, "compass", `
			local scope = marasi:scope()
			scope:clear_rules()
			scope:add_rule("-blocked\\.com", "host")

			function processRequest(request)
			  if not scope:matches(request) then
				  request:Drop()
			  end
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://www.blocked.com/examplePage", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = CompassRequestModifier(proxy, req)

		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrDropped)
		}
		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %q\ngot: %v", ErrDropped, err)
		}

		if drop, ok := DroppedFlagFromContext(req.Context()); !ok || !drop {
			t.Errorf("expected dropped flag to be set in context and to be true")
		}
	})

	t.Run("CompassRequestModifier should return an error if the proxy has no compass extension configured", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		err := CompassRequestModifier(proxy, req)

		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrExtensionNotFound)
		}

		if !errors.Is(err, ErrExtensionNotFound) {
			t.Fatalf("wanted: %q\ngot: %v", ErrExtensionNotFound, err)
		}
	})
}

func TestSetupRequestModifier(t *testing.T) {
	t.Run("request should have all the context keys and data", func(t *testing.T) {
		proxy := &Proxy{}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context: %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if _, ok := RequestIDFromContext(req.Context()); !ok {
			t.Errorf("expected RequestIDKey to be set in context")
		}

		if _, ok := RequestTimeFromContext(req.Context()); !ok {
			t.Errorf("expected RequestTimeKey to be set in context")
		}

		if metadata, ok := MetadataFromContext(req.Context()); !ok {
			t.Errorf("expected Metadatakey to be set ")
		} else if len(metadata) != 0 {
			t.Errorf("expected metadata to be {} with length 0, but got length %d", len(metadata))
		}

		if _, ok := SessionFromContext(req.Context()); !ok {
			t.Errorf("expected MartianSessionKey to be set in context")
		}

		if _, ok := LaunchpadIDFromContext(req.Context()); ok {
			t.Errorf("expected LaunchpadIDKey to not be set in context")
		}
	})

	t.Run("requests sent from launchpad should set the ID in context and remove the x-launchpad-id from header", func(t *testing.T) {
		proxy := &Proxy{}
		want, err := uuid.NewRandom()
		if err != nil {
			t.Fatalf("generating UUID for test : %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		req.Header.Set("x-launchpad-id", want.String())

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context: %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if got, ok := LaunchpadIDFromContext(req.Context()); !ok || got != want {
			t.Fatalf("wanted: %q\ngot: %q", want, got)
		}

		if metadata, ok := MetadataFromContext(req.Context()); !ok || metadata["launchpad"] != true || metadata["launchpad_id"] != want {
			t.Fatalf("wanted: %v\ngot: %v", want, metadata)
		}

		if req.Header.Get("x-launchpad-id") != "" {
			t.Fatalf("expected x-launchpad-id to be removed")
		}

	})
}

func TestOverrideWaypointsModifier(t *testing.T) {
	proxy := &Proxy{
		Waypoints: map[string]string{
			"marasi.app:80":   "127.0.0.1:9000",
			"marasi.app:443":  "127.0.0.1:8000",
			"marasi.app:8000": "127.0.0.1:7000",
		},
	}

	t.Run("request to host (HTTP) in waypoint map should be overriden and metadata is set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://marasi.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context: %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = OverrideWaypointsModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if metadata, ok := MetadataFromContext(req.Context()); !ok {
			t.Fatalf("expected metadata to be set on request")
		} else if metadata["original_host"] != "marasi.app:80" || metadata["override_host"] != "127.0.0.1:9000" {
			t.Fatalf("wanted:\noriginal_host: %q\noverride_host: %q\ngot:\noriginal_host: %q\noverride_host: %q", "marasi.app:80", "127.0.0.1:9000", metadata["original_host"], metadata["override_host"])
		}
	})

	t.Run("request to host (HTTPS) in waypoint map should be overriden and metadata is set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context: %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = OverrideWaypointsModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if metadata, ok := MetadataFromContext(req.Context()); !ok {
			t.Fatalf("expected metadata to be set on request")
		} else if metadata["original_host"] != "marasi.app:443" || metadata["override_host"] != "127.0.0.1:8000" {
			t.Fatalf("wanted:\noriginal_host: %q\noverride_host: %q\ngot:\noriginal_host: %q\noverride_host: %q", "marasi.app:443", "127.0.0.1:8000", metadata["original_host"], metadata["override_host"])
		}
	})

	t.Run("request to host on non-standard port in waypoint map should be overriden and metadata is set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app:8000", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context: %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = OverrideWaypointsModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if metadata, ok := MetadataFromContext(req.Context()); !ok {
			t.Fatalf("expected metadata to be set on request")
		} else if metadata["original_host"] != "marasi.app:8000" || metadata["override_host"] != "127.0.0.1:7000" {
			t.Fatalf("wanted:\noriginal_host: %q\noverride_host: %q\ngot:\noriginal_host: %q\noverride_host: %q", "marasi.app:8000", "127.0.0.1:7000", metadata["original_host"], metadata["override_host"])
		}
	})

	t.Run("request to host that is not on waypoint list should be processed normally", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://marasi2.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context: %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = OverrideWaypointsModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if metadata, ok := MetadataFromContext(req.Context()); !ok {
			t.Fatalf("expected metadata to be set on request")
		} else {
			host, originalHost := metadata["original_host"]
			override, overrideHost := metadata["override_host"]
			if originalHost || overrideHost {
				t.Fatalf("wanted:\noriginal_host: %q\noverride_host: %q\ngot:\noriginal_host: %q\noverride_host: %q", "", "", host, override)
			}
		}
	})
}

func TestExtensionsRequestModifier(t *testing.T) {
	t.Run("multiple extensions should run on and modify requests", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "compass", `
			function processRequest(request)
				request:Drop()
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		err := ExtensionsRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if req.Header.Get("x-workshop-ran") != "true" {
			t.Errorf("expected x-workshop-ran header to be set to true but got %q", req.Header.Get("x-workshop-ran"))
		}

		if req.Header.Get("x-testExtension-ran") != "true" {
			t.Errorf("expected x-testExtension-ran header to be set to true but got %q", req.Header.Get("x-testExtension-ran"))
		}
	})

	t.Run("if first extension skips the remaining should not run", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", `
			function processRequest(request)
				request:Headers():Set("x-workshop-ran", "true")
				request:Skip()
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		err := ExtensionsRequestModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}

		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}

		if req.Header.Get("x-workshop-ran") != "true" {
			t.Errorf("expected x-workshop-ran header to be set to true but got %q", req.Header.Get("x-workshop-ran"))
		}

		if req.Header.Get("x-testExtension-ran") == "true" {
			t.Errorf("expected x-testExtension-ran header to not be set but got %q", req.Header.Get("x-testExtension-ran"))
		}
	})

	t.Run("if first extension drops the remaining should not run", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", `
			function processRequest(request)
				request:Headers():Set("x-workshop-ran", "true")
				request:Drop()
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		if err != nil {
			t.Fatalf("updating workshop extension for test : %v", err)
		}

		err = ExtensionsRequestModifier(proxy, req)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrDropped)
		}

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %q\ngot: %v", ErrDropped, err)
		}

		if req.Header.Get("x-workshop-ran") != "true" {
			t.Errorf("expected x-workshop-ran header to be set to true but got %q", req.Header.Get("x-workshop-ran"))
		}

		if req.Header.Get("x-testExtension-ran") == "true" {
			t.Errorf("expected x-testExtension-ran header to not be set but got %q", req.Header.Get("x-testExtension-ran"))
		}

		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: true\ngot: %t", ctx.SkippingRoundTrip())
		}
	})

	t.Run("if request x-extension-id matches extensionID it should skip execution", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}

		defer remove()

		req.Header.Set("x-extension-id", testExtensions["workshop"].ID.String())
		err = ExtensionsRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if req.Header.Get("x-workshop-ran") == "true" {
			t.Errorf("expected x-workshop-ran header to not be set but got %q", req.Header.Get("x-workshop-ran"))
		}

		if req.Header.Get("x-testExtension-ran") != "true" {
			t.Errorf("expected x-testExtension-ran header to be set to true but got %q", req.Header.Get("x-testExtension-ran"))
		}

		if req.Header.Get("x-extension-id") != "" {
			t.Errorf("expected the x-extension-id header to be removed but got %q", req.Header.Get("x-extension-id"))
		}
	})

	t.Run("extensions without processRequest defined should not be executed on requests", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", "processRequest = nil")
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		err := ExtensionsRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if req.Header.Get("x-workshop-ran") == "true" {
			t.Errorf("expected x-workshop-ran header to not be set but got %q", req.Header.Get("x-workshop-ran"))
		}

		if req.Header.Get("x-testExtension-ran") != "true" {
			t.Errorf("expected x-testExtension-ran header to be set to true but got %q", req.Header.Get("x-testExtension-ran"))
		}

	})

	t.Run("extensions with a lua error should not crash the proxy", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", `
			function processRequest(request)
				request:Headers():St("x-workshop-ran", "true")
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		err := ExtensionsRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if req.Header.Get("x-workshop-ran") == "true" {
			t.Errorf("expected x-workshop-ran header to not be set but got %q", req.Header.Get("x-workshop-ran"))
		}

		if req.Header.Get("x-testExtension-ran") != "true" {
			t.Errorf("expected x-testExtension-ran header to be set to true but got %q", req.Header.Get("x-testExtension-ran"))
		}

	})

	t.Run("extensions should run in order", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "testExtension", `
			function processRequest(request)
				request:Headers():Set("x-workshop-ran", "overwritten")
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		err := ExtensionsRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if req.Header.Get("x-workshop-ran") == "true" {
			t.Errorf("expected x-workshop-ran header to not be set to true")
		}

		if req.Header.Get("x-testExtension-ran") == "true" {
			t.Errorf("expected x-testExtension-ran header to not be set to true but got %q", req.Header.Get("x-testExtension-ran"))
		}

		if req.Header.Get("x-workshop-ran") != "overwritten" {
			t.Errorf("expected x-workshop-ran header to be set to overwritten, but got : %q", req.Header.Get("x-workshop-ran"))
		}
	})
}

// TODO need to review these once the InterceptedQueue is refactored
func TestCheckpointRequestModifier(t *testing.T) {
	t.Run("should return ErrExtensionNotFound if no checkpoint extension is loaded", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		err := CheckpointRequestModifier(proxy, req)

		if !errors.Is(err, ErrExtensionNotFound) {
			t.Fatalf("wanted: %v, got: %v", ErrExtensionNotFound, err)
		}
	})

	t.Run("should not intercept if checkpoint returns false and the global flag is false (default)", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.InterceptedQueue) != 0 {
			t.Fatalf("expected intercept queue to be empty, but got length %d", len(proxy.InterceptedQueue))
		}

		if metadata, _ := MetadataFromContext(req.Context()); metadata["intercepted"] == true {
			t.Fatalf("wanted: nil\ngot: %v", metadata["intercepted"])
		}
	})

	t.Run("should drop request if interceptHandler is not defined and the request is intercepted", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if err == nil {
			t.Fatalf("wanted: %v\ngot: nil", ErrDropped)
		}

		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})

	t.Run("should intercept request if checkpoint extension returns true", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		updateExtension(t, proxy, "checkpoint", `
			function interceptRequest(request)		
				return true
			end
		`)
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		original, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request : %v", err)
		}

		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}

		if metadata, ok := MetadataFromContext(req.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-request"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-request"])
			}

			if metadata["dropped"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["dropped"])
			}
		}
	})

	t.Run("should intercept request if global intercept flag is set", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		original, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request : %v", err)
		}

		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
		if metadata, ok := MetadataFromContext(req.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-request"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-request"])
			}

			if metadata["dropped"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["dropped"])
			}
		}
	})

	t.Run("should drop request the request if the resume action is false", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume: false,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		original, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request : %v", err)
		}

		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}

		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(req.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-request"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-request"])
			}

			if metadata["dropped"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["dropped"])
			}
		}
	})

	t.Run("should not set intercept flag if ShouldInterceptResponse is set to false and the request is resumed", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume:                  true,
					ShouldInterceptResponse: false,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		original, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request : %v", err)
		}

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(req.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-request"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-request"])
			}

			if metadata["dropped"] == true {
				t.Fatalf("wanted: nil\ngot: %v", metadata["dropped"])
			}
		}

		if flag, ok := InterceptFlagFromContext(req.Context()); ok && flag {
			t.Fatalf("wanted: false\ngot: %v", flag)
		}

	})

	t.Run("should set intercept flag if ShouldInterceptResponse is set to true and the request is resumed", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume:                  true,
					ShouldInterceptResponse: true,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		original, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request : %v", err)
		}

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(req.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-request"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-request"])
			}

			if metadata["dropped"] == true {
				t.Fatalf("wanted: nil\ngot: %v", metadata["dropped"])
			}
		}

		if flag, ok := InterceptFlagFromContext(req.Context()); !ok || !flag {
			t.Fatalf("wanted: true\ngot: %v", flag)
		}

	})

	t.Run("resumed request should be updated after modification", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		modifiedRequest := "POST / HTTP/1.1\r\nHost: marasi.app\r\nContent-Length: 12\r\nContent-Type: text/plain\r\n\r\nhello marasi"
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			intercepted.Raw = modifiedRequest
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume:                  true,
					ShouldInterceptResponse: false,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		original, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request : %v", err)
		}

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(req.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-request"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-request"])
			}

			if metadata["dropped"] == true {
				t.Fatalf("wanted: nil\ngot: %v", metadata["dropped"])
			}
		}

		if flag, ok := InterceptFlagFromContext(req.Context()); ok || flag {
			t.Fatalf("wanted: false\ngot: %v", flag)
		}

		got, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatalf("dumping request after modification: %v", err)
		}

		if string(got) != modifiedRequest {
			t.Fatalf("wanted:\n%q\ngot:\n%q", modifiedRequest, string(got))
		}

	})

	t.Run("modifier should return an error if the modified request is invalid / malformed", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		modifiedRequest := "POST /HTTP/1.1\r\nHost: marasi.app\r\nContent-Length: 12\r\nContent-Type: text/plain\r\n\r\nhello marasi"
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			intercepted.Raw = modifiedRequest
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume:                  true,
					ShouldInterceptResponse: false,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = CheckpointRequestModifier(proxy, req)

		if !errors.Is(err, ErrRebuildRequest) {
			t.Fatalf("wanted: %v\ngot: %v", ErrRebuildRequest, err)
		}
		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}
	})
}

func TestWriteRequestModifier(t *testing.T) {
	t.Run("requests missing a request ID should cause the modifier to return an error", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		err := WriteRequestModifier(proxy, req)
		if !errors.Is(err, ErrRequestIDNotFound) {
			t.Fatalf("wanted: %v\ngot: %v", ErrRequestIDNotFound, err)
		}

		if len(proxy.DBWriteChannel) > 0 {
			t.Fatalf("wanted: 0\ngot: %d", len(proxy.DBWriteChannel))
		}

	})

	t.Run("requests should still be written to the DB when onrequest is undefined", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}

		err = WriteRequestModifier(proxy, req)

		if !errors.Is(err, ErrRequestHandlerUndefined) {
			t.Fatalf("wanted: %v\ngot: %v", ErrRequestHandlerUndefined, err)
		}

		if len(proxy.DBWriteChannel) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.DBWriteChannel))
		}

	})

	t.Run("requests without a timestamp should return an error", func(t *testing.T) {
		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generating uuid : %v", err)
		}
		proxy := newTestProxy(t)
		proxy.OnRequest = func(req ProxyRequest) error {
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app/blog", nil)

		*req = *ContextWithRequestID(req, wantID)
		*req = *ContextWithMetadata(req, make(Metadata))

		err = WriteRequestModifier(proxy, req)
		if !errors.Is(err, ErrProxyRequest) {
			t.Fatalf("wanted: %v\ngot: %v", ErrProxyRequest, err)
		}

		if len(proxy.DBWriteChannel) != 0 {
			t.Fatalf("wanted: 0\ngot: %d", len(proxy.DBWriteChannel))
		}
	})

	t.Run("requests with a request ID should be written to the DBWriteChannel", func(t *testing.T) {
		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generating uuid : %v", err)
		}
		wantTime := time.Now()
		want := &ProxyRequest{
			ID:          wantID,
			Scheme:      "https",
			Method:      "GET",
			Host:        "marasi.app",
			Path:        "/blog",
			Metadata:    make(Metadata),
			RequestedAt: wantTime,
		}
		proxy := newTestProxy(t)
		proxy.OnRequest = func(req ProxyRequest) error {
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app/blog", nil)

		raw, _, err := rawhttp.DumpRequest(req)
		if err != nil {
			t.Fatalf("dumping http request (rawhttp) : %v", err)
		}
		want.Raw = raw

		*req = *ContextWithRequestID(req, wantID)
		*req = *ContextWithRequestTime(req, wantTime)
		*req = *ContextWithMetadata(req, make(Metadata))

		err = WriteRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.DBWriteChannel) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.DBWriteChannel))
		}

		got := <-proxy.DBWriteChannel
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("wanted: %v\ngot: %v", want, got)
		}

	})

	t.Run("requests coming from launchpad should write a LaunchpadRequest to the DBWriteChannel", func(t *testing.T) {
		wantRequestID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generating uuid : %v", err)
		}

		wantLaunchpadID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generating uuid : %v", err)
		}

		want := LaunchpadRequest{
			LaunchpadID: wantLaunchpadID,
			RequestID:   wantRequestID,
		}

		proxy := newTestProxy(t)
		proxy.OnRequest = func(req ProxyRequest) error {
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app/blog", nil)

		*req = *ContextWithRequestID(req, wantRequestID)
		*req = *ContextWithRequestTime(req, time.Now())
		*req = *ContextWithMetadata(req, make(Metadata))
		*req = *ContextWithLaunchpadID(req, wantLaunchpadID)

		err = WriteRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		// DBWriteChannel should now hold the proxyRequest and then the launchpadRequest
		if len(proxy.DBWriteChannel) != 2 {
			t.Fatalf("wanted: 2\ngot: %d", len(proxy.DBWriteChannel))
		}

		// first Read off the proxy request and discard it
		_ = <-proxy.DBWriteChannel
		got := <-proxy.DBWriteChannel
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("wanted: %v\ngot: %v", want, got)
		}
	})

	t.Run("modifier should return nil when OnRequest is defined and a standard request comes in", func(t *testing.T) {
		requestChannel := make(chan ProxyRequest, 1)
		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generating uuid : %v", err)
		}
		wantTime := time.Now()
		want := &ProxyRequest{
			ID:          wantID,
			Scheme:      "https",
			Method:      "GET",
			Host:        "marasi.app",
			Path:        "/blog",
			Metadata:    make(Metadata),
			RequestedAt: wantTime,
		}
		proxy := newTestProxy(t)
		proxy.OnRequest = func(req ProxyRequest) error {
			requestChannel <- req
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app/blog", nil)

		raw, _, err := rawhttp.DumpRequest(req)
		if err != nil {
			t.Fatalf("dumping http request (rawhttp) : %v", err)
		}
		want.Raw = raw

		*req = *ContextWithRequestID(req, wantID)
		*req = *ContextWithRequestTime(req, wantTime)
		*req = *ContextWithMetadata(req, make(Metadata))

		err = WriteRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.DBWriteChannel) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.DBWriteChannel))
		}

		got := <-proxy.DBWriteChannel
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("wanted: %v\ngot: %v", want, got)
		}

		select {
		case gotFromChannel := <-requestChannel:
			if !reflect.DeepEqual(*want, gotFromChannel) {
				t.Fatalf("wanted: %v\ngot: %v", want, gotFromChannel)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("expected onRequest to be called")
		}
	})
}

// Response Modifiers
func TestResponseFilterModifier(t *testing.T) {
	t.Run("response to CONNECT request should be skipped by marasi", func(t *testing.T) {
		proxy := &Proxy{}
		req := httptest.NewRequest(http.MethodConnect, "https://marasi.app", nil)
		res := &http.Response{Request: req}

		err := ResponseFilterModifier(proxy, res)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
	})

	t.Run("responses to requests that were marked as skipped should be skipped by marasi", func(t *testing.T) {
		proxy := &Proxy{}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		ctx, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		ctx.SkipRoundTrip()

		res := &http.Response{Request: req}

		err = ResponseFilterModifier(proxy, res)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
		if !ctx.SkippingRoundTrip() {
			t.Fatalf("wanted: True\ngot: %t", ctx.SkippingRoundTrip())
		}
	})

	t.Run("responses to requests that have SkipKey set in context should be skipped", func(t *testing.T) {
		proxy := &Proxy{}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()
		*req = *ContextWithSkipFlag(req, true)
		res := &http.Response{Request: req}

		err = ResponseFilterModifier(proxy, res)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}
		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}
	})

	t.Run("responses to standard requests should be processed and timestamped", func(t *testing.T) {
		proxy := &Proxy{}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		res := &http.Response{Request: req}

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = ResponseFilterModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if _, ok := ResponseTimeFromContext(res.Request.Context()); !ok {
			t.Fatalf("wanted response time to be set on ResponseTimeKey in context")
		}
	})
}

func TestBufferedStreamingResponseModifier(t *testing.T) {
	proxy := &Proxy{}
	t.Run("chunked response modifier should return an error if it fails to read the body", func(t *testing.T) {
		res := &http.Response{
			Header: make(http.Header),
			Body:   &erroringReader{},
		}

		err := BufferStreamingBodyModifier(proxy, res)
		if !errors.Is(err, ErrReadBody) {
			t.Fatalf("wanted: %v\ngot: %v", ErrReadBody, err)
		}
	})

	t.Run("should read the entire body and set the content length + remove TransferEncoding", func(t *testing.T) {

		testReader, testWriter := io.Pipe()

		res := &http.Response{
			Header:           make(http.Header),
			TransferEncoding: []string{"chunked"},
			Body:             testReader,
		}

		want := "this is streamed marasi"
		go func() {
			defer testWriter.Close()
			testWriter.Write([]byte("this is s"))
			time.Sleep(10 * time.Millisecond)
			testWriter.Write([]byte("treamed marasi"))
		}()

		err := BufferStreamingBodyModifier(proxy, res)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		got, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if string(got) != want {
			t.Fatalf("wanted: %q\ngot: %q", want, string(got))
		}

		if res.ContentLength != int64(len(want)) {
			t.Fatalf("wanted: %d\ngot: %d", len(want), len(got))
		}

		if res.Header.Get("Content-Length") != fmt.Sprintf("%d", len(want)) {
			t.Fatalf("wanted: %d\ngot: %s", len(want), res.Header.Get("Content-Length"))
		}

		if res.TransferEncoding != nil {
			t.Fatalf("wanted: nil\ngot: %v", res.TransferEncoding)
		}
	})
}

func TestCompressedResponseModifier(t *testing.T) {
	proxy := &Proxy{}

	t.Run("response with nil body not be modified and return nil", func(t *testing.T) {
		res := &http.Response{
			Header:        make(http.Header),
			Body:          nil,
			ContentLength: 10,
		}

		res.Header.Set("Content-Encoding", "gzip")

		err := CompressedResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("wanted: gzip\ngot: %v", res.Header.Get("Content-Encoding"))
		}

		if res.ContentLength != 10 {
			t.Fatalf("wanted: 10\ngot: %v", res.ContentLength)
		}
	})

	t.Run("response with with content length 0 should not be modified and return nil", func(t *testing.T) {
		res := testResponse("")
		res.Header.Set("Content-Encoding", "gzip")

		err := CompressedResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("wanted: gzip\ngot: %v", res.Header.Get("Content-Encoding"))
		}

		if res.ContentLength != 0 {
			t.Fatalf("wanted: 0\ngot: %v", res.ContentLength)
		}
	})

	t.Run("responses with no content-encoding should not be modified and return nil", func(t *testing.T) {
		want := "test marasi response"
		res := testResponse(want)

		err := CompressedResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		got, _ := io.ReadAll(res.Body)
		if string(got) != want {
			t.Fatalf("wanted: %q\ngot: %q", want, got)
		}
	})

	t.Run("should return an error if the modifier fails to create a GzipReader", func(t *testing.T) {
		badGzip := []byte("marasi")
		res := testResponse(string(badGzip))
		res.Header.Set("Content-Encoding", "gzip")

		err := CompressedResponseModifier(proxy, res)

		if err == nil {
			t.Fatal("wanted: err\ngot: nil")
		}

		if !strings.Contains(err.Error(), "creating gzip reader") {
			t.Fatalf("wanted message to contain : %q\ngot: %v", "creating gzip reader", err)
		}
	})

	t.Run("should return an error if the modifier fails to read the gzipped content", func(t *testing.T) {
		gzipBody, bodyLen := testGzipBody(t, "marasi")
		headerBytes := make([]byte, 10) // Gzip header is 10 bytes
		bytesRead, _ := gzipBody.Read(headerBytes)
		gzipBody.Close()

		failingBody := io.MultiReader(bytes.NewReader(headerBytes[:bytesRead]), &erroringReader{})

		res := &http.Response{
			Header:        make(http.Header),
			Body:          io.NopCloser(failingBody),
			ContentLength: int64(bodyLen),
		}
		res.Header.Set("Content-Encoding", "gzip")

		err := CompressedResponseModifier(proxy, res)

		if err == nil {
			t.Fatal("wanted: error\ngot: nil")
		}

		if !strings.Contains(err.Error(), "reading gzip content") {
			t.Fatalf("wanted message to contain: %q\ngot: %v", "reading gzip content", err)
		}

	})

	t.Run("should replace the res.Body, and update the fields after reading the gzipped content", func(t *testing.T) {
		want := "gzipped marasi content should be decompressed"
		compressed, length := testGzipBody(t, want)

		res := &http.Response{
			Header:        make(http.Header),
			Body:          compressed,
			ContentLength: int64(length),
		}
		res.Header.Set("Content-Encoding", "gzip")
		res.Header.Set("Content-Length", fmt.Sprintf("%d", length))

		err := CompressedResponseModifier(proxy, res)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		got, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("reading response body after modifier : %v", err)
		}

		if string(got) != want {
			t.Fatalf("wanted: %q\ngot: %q", want, string(got))
		}

		if res.Header.Get("Content-Encoding") != "" {
			t.Fatalf("wanted: ''\ngot: %v", res.Header.Get("Content-Encoding"))
		}

		if res.Header.Get("Content-Length") != fmt.Sprintf("%d", len(want)) {
			t.Fatalf("wanted: %d\ngot: %s", len(want), res.Header.Get("Content-Length"))
		}
	})

	t.Run("should return an error if the modifier fails to read the brotli content", func(t *testing.T) {
		res := &http.Response{
			Header:        make(http.Header),
			Body:          &erroringReader{},
			ContentLength: 10,
		}

		res.Header.Set("Content-Encoding", "br")

		err := CompressedResponseModifier(proxy, res)
		if err == nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if !strings.Contains(err.Error(), "reading brotli content") {
			t.Fatalf("wanted message to contain: %q\ngot: %v", "reading brotli content", err)
		}

	})

	t.Run("should replace the res.Body, and update the fields after reading the brotli content", func(t *testing.T) {
		want := "brotlied marasi content should be decompressed"
		compressed, length := testBrotliBody(t, want)

		res := &http.Response{
			Header:        make(http.Header),
			Body:          compressed,
			ContentLength: int64(length),
		}
		res.Header.Set("Content-Encoding", "br")
		res.Header.Set("Content-Length", fmt.Sprintf("%d", length))

		err := CompressedResponseModifier(proxy, res)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		got, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("reading response body after modifier : %v", err)
		}

		if string(got) != want {
			t.Fatalf("wanted: %q\ngot: %q", want, string(got))
		}

		if res.Header.Get("Content-Encoding") != "" {
			t.Fatalf("wanted: ''\ngot: %v", res.Header.Get("Content-Encoding"))
		}

		if res.Header.Get("Content-Length") != fmt.Sprintf("%d", len(want)) {
			t.Fatalf("wanted: %d\ngot: %s", len(want), res.Header.Get("Content-Length"))
		}
	})

	t.Run("should not modify the repsonse and return nil if the content-encoding is unsupported", func(t *testing.T) {
		want := "unsupported encodings should not be modified"
		res := testResponse(want)
		res.Header.Set("Content-Encoding", "marasi")

		err := CompressedResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("Content-Encoding") != "marasi" {
			t.Fatalf("wanted: marasi\ngot: %v", res.Header.Get("Content-Encoding"))
		}

		got, _ := io.ReadAll(res.Body)
		if string(got) != want {
			t.Fatalf("wanted: %q\ngot: %q", want, got)
		}
	})
}

func TestCompassResponseModifier(t *testing.T) {
	t.Run("should return ErrExtensionNotFound if no compass extension was loaded", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		res := &http.Response{
			Request: req,
		}

		err := CompassResponseModifier(proxy, res)
		if !errors.Is(err, ErrExtensionNotFound) {
			t.Fatalf("wanted: %q\ngot: %q", ErrExtensionNotFound, err)
		}
	})

	t.Run("responses matching rule should be skipped", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		req := httptest.NewRequest(http.MethodGet, "https://www.blocked.com/examplePage", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Request: req,
		}

		err = CompassResponseModifier(proxy, res)

		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}

		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}

		if skip, ok := SkipFlagFromContext(res.Request.Context()); !ok || !skip {
			t.Errorf("expected skipped flag to be set in context and true")
		}
	})

	t.Run("responses matching rule should be dropped if :Drop() is used", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		updateExtension(t, proxy, "compass", `
			local scope = marasi:scope()
			scope:clear_rules()
			scope:add_rule("-blocked\\.com", "host")

			function processResponse(response)
			  if not scope:matches(response) then
				  response:Drop()
			  end
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://www.blocked.com/examplePage", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Request: req,
		}

		err = CompassResponseModifier(proxy, res)

		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrDropped)
		}
		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %q\ngot: %v", ErrDropped, err)
		}

		if drop, ok := DroppedFlagFromContext(res.Request.Context()); !ok || !drop {
			t.Errorf("expected dropped flag to be set in context and to be true")
		}
	})

	t.Run("responses that don't match rule should be allowed when default policy is true", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Request: req,
		}

		err = CompassResponseModifier(proxy, res)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if skip, ok := SkipFlagFromContext(res.Request.Context()); ok && skip {
			t.Errorf("expected skipflag to not be set and should not be true")
		}
	})

	t.Run("responses that don't match rule should be skipped when default policy is false", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["compass"])
		proxy.Scope.DefaultAllow = false
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Request: req,
		}

		err = CompassResponseModifier(proxy, res)

		if err == nil {
			t.Fatalf("wanted: %v\ngot: nil", ErrSkipPipeline)
		}

		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}

		if skip, ok := SkipFlagFromContext(res.Request.Context()); !ok || !skip {
			t.Errorf("expected skipflag to be set in context and to be equal to true")
		}
	})
}

func TestExtensionsResponseModifier(t *testing.T) {
	t.Run("multiple extensions should run on and modify responses", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"])
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		*req = *ContextWithExtensionID(req, "")

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = ExtensionsResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("x-workshop-ran-response") != "true" {
			t.Errorf("expected x-workshop-ran-response header to be set to true but got %q", req.Header.Get("x-workshop-ran-response"))
		}

		if res.Header.Get("x-testExtension-ran-response") != "true" {
			t.Errorf("expected x-testExtension-ran-response header to be set to true but got %q", req.Header.Get("x-testExtension-ran-response"))
		}
	})
	t.Run("extensions should run in the order they are defined", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "testExtension", `
			function processResponse(response)
				response:Headers():Set("x-workshop-ran-response", "overwritten")
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		*req = *ContextWithExtensionID(req, "")

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = ExtensionsResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("x-workshop-ran-response") == "true" {
			t.Errorf("expected x-workshop-ran-response header to not be set to true")
		}

		if res.Header.Get("x-testExtension-ran-response") == "true" {
			t.Errorf("expected x-testExtension-ran-response header to not be set to true but got %q", res.Header.Get("x-testExtension-ran-response"))
		}

		if res.Header.Get("x-workshop-ran-response") != "overwritten" {
			t.Errorf("expected x-workshop-ran-response header to be set to overwritten, but got : %q", res.Header.Get("x-workshop-ran-response"))
		}
	})

	t.Run("if first extension skips the remaining should not run", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", `
			function processResponse(response)
				response:Headers():Set("x-workshop-ran-response", "true")
				response:Skip()
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		*req = *ContextWithExtensionID(req, "")

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = ExtensionsResponseModifier(proxy, res)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrSkipPipeline)
		}

		if !errors.Is(err, ErrSkipPipeline) {
			t.Fatalf("wanted: %q\ngot: %v", ErrSkipPipeline, err)
		}

		if res.Header.Get("x-workshop-ran-response") != "true" {
			t.Errorf("expected x-workshop-ran header to be set to true but got %q", res.Header.Get("x-workshop-ran-response"))
		}

		if res.Header.Get("x-testExtension-ran-response") == "true" {
			t.Errorf("expected x-testExtension-ran-response header to not be set but got %q", res.Header.Get("x-testExtension-ran-response"))
		}
	})

	t.Run("if first extension drops the remaining should not run", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", `
			function processResponse(response)
				response:Headers():Set("x-workshop-ran-response", "true")
				response:Drop()
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		*req = *ContextWithExtensionID(req, "")

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = ExtensionsResponseModifier(proxy, res)
		if err == nil {
			t.Fatalf("wanted: %q\ngot: nil", ErrDropped)
		}

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %q\ngot: %v", ErrDropped, err)
		}

		if res.Header.Get("x-workshop-ran-response") != "true" {
			t.Errorf("expected x-workshop-ran header to be set to true but got %q", res.Header.Get("x-workshop-ran-response"))
		}

		if res.Header.Get("x-testExtension-ran-response") == "true" {
			t.Errorf("expected x-testExtension-ran-response header to not be set but got %q", res.Header.Get("x-testExtension-ran-response"))
		}
	})

	t.Run("if response x-extension-id matches extensionID it should skip execution", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		*req = *ContextWithExtensionID(req, testExtensions["workshop"].ID.String())

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}

		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = ExtensionsResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("x-workshop-ran-response") == "true" {
			t.Errorf("expected x-workshop-ran-response header to not be set but got %q", res.Header.Get("x-workshop-ran-response"))
		}

		if res.Header.Get("x-testExtension-ran-response") != "true" {
			t.Errorf("expected x-testExtension-ran-response header to be set to true but got %q", res.Header.Get("x-testExtension-ran-response"))
		}
	})

	t.Run("extensions without processResponse defined should not be executed on responses", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", "processResponse = nil")
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		*req = *ContextWithExtensionID(req, "")

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}

		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = ExtensionsResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("x-workshop-ran-response") == "true" {
			t.Errorf("expected x-workshop-ran-response header to not be set but got %q", res.Header.Get("x-workshop-ran-response"))
		}

		if res.Header.Get("x-testExtension-ran-response") != "true" {
			t.Errorf("expected x-testExtension-ran-response header to be set to true but got %q", res.Header.Get("x-testExtension-ran-response"))
		}

	})

	t.Run("extensions with a lua error should not crash the proxy", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["workshop"], testExtensions["testExtension"], testExtensions["compass"])
		updateExtension(t, proxy, "workshop", `
			function processResponse(response)
				response:Headers():St("x-workshop-ran", "true")
			end
		`)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		*req = *ContextWithExtensionID(req, "")

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}

		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = ExtensionsResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if res.Header.Get("x-workshop-ran-response") == "true" {
			t.Errorf("expected x-workshop-ran-response header to not be set but got %q", res.Header.Get("x-workshop-ran-response"))
		}

		if res.Header.Get("x-testExtension-ran-response") != "true" {
			t.Errorf("expected x-testExtension-ran-response header to be set to true but got %q", res.Header.Get("x-testExtension-ran-response"))
		}
	})
}

func TestCheckpointResponseModifier(t *testing.T) {
	t.Run("should return ErrExtensionNotFound if no checkpoint extension is loaded", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}

		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = CheckpointResponseModifier(proxy, res)

		if !errors.Is(err, ErrExtensionNotFound) {
			t.Fatalf("wanted: %v, got: %v", ErrExtensionNotFound, err)
		}
	})

	t.Run("should not intercept if checkpoint returns false and the global flag is false (default)", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}

		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("setting up request : %v", err)
		}

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = CheckpointResponseModifier(proxy, res)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.InterceptedQueue) != 0 {
			t.Fatalf("expected intercept queue to be empty, but got length %d", len(proxy.InterceptedQueue))
		}

		if metadata, _ := MetadataFromContext(res.Request.Context()); metadata["intercepted"] == true {
			t.Fatalf("wanted: nil\ngot: %v", metadata["intercepted"])
		}
	})

	t.Run("should drop response if interceptHandler is not defined and the response is intercepted", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("setting up request: %v", err)
		}

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		err = CheckpointResponseModifier(proxy, res)

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}
	})

	t.Run("should intercept response if checkpoint extension returns true", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		updateExtension(t, proxy, "checkpoint", `
			function interceptResponse(response)		
				return true
			end
		`)
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("setting up request: %v", err)
		}

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		original, err := httputil.DumpResponse(res, true)
		if err != nil {
			t.Fatalf("dumping response : %v", err)
		}

		err = CheckpointResponseModifier(proxy, res)

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(res.Request.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-response"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-response"])
			}

			if metadata["dropped"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["dropped"])
			}
		}
	})

	t.Run("should intercept response if global intercept flag is set", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("setting up request: %v", err)
		}

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		original, err := httputil.DumpResponse(res, true)
		if err != nil {
			t.Fatalf("dumping response : %v", err)
		}

		err = CheckpointResponseModifier(proxy, res)

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(res.Request.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-response"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-response"])
			}

			if metadata["dropped"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["dropped"])
			}
		}
	})

	t.Run("should drop response if the resume action is false", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume: false,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("setting up request: %v", err)
		}

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}

		original, err := httputil.DumpResponse(res, true)
		if err != nil {
			t.Fatalf("dumping response : %v", err)
		}

		err = CheckpointResponseModifier(proxy, res)

		if !errors.Is(err, ErrDropped) {
			t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(res.Request.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-response"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-response"])
			}

			if metadata["dropped"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["dropped"])
			}
		}
	})

	t.Run("resumed response should be updated after modification", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		modifiedResponse := "HTTP/1.1 200 OK\r\n" +
			"Content-Length: 12\r\n" +
			"Content-Type: text/plain\r\n" +
			"X-Modified: true\r\n" +
			"\r\n" +
			"hello marasi"
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			intercepted.Raw = modifiedResponse
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume: true,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("setting up request: %v", err)
		}
		originalBody := "original body"
		res := &http.Response{
			Header:        make(http.Header),
			Request:       req,
			StatusCode:    http.StatusNotFound,
			Proto:         "HTTP/1.1",
			Body:          io.NopCloser(strings.NewReader(originalBody)),
			ContentLength: int64(len(originalBody)),
		}
		res.Header.Set("Content-Length", fmt.Sprintf("%d", res.ContentLength))

		original, err := httputil.DumpResponse(res, true)
		if err != nil {
			t.Fatalf("dumping response : %v", err)
		}

		err = CheckpointResponseModifier(proxy, res)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}

		if metadata, ok := MetadataFromContext(req.Context()); ok {
			if metadata["intercepted"] != true {
				t.Fatalf("wanted: true\ngot: %v", metadata["intercepted"])
			}

			if metadata["original-response"] != string(original) {
				t.Fatalf("wanted:\n%q\ngot:\n%q", string(original), metadata["original-response"])
			}

			if metadata["dropped"] == true {
				t.Fatalf("wanted: nil\ngot: %v", metadata["dropped"])
			}
		}

		got, err := httputil.DumpResponse(res, true)
		if err != nil {
			t.Fatalf("dumping response after modification: %v", err)
		}

		if string(got) != modifiedResponse {
			t.Fatalf("wanted:\n%q\ngot:\n%q", modifiedResponse, string(got))
		}

	})

	t.Run("modifier should return an error if the modified request is invalid / malformed", func(t *testing.T) {
		proxy := newTestProxy(t, testExtensions["checkpoint"])
		modifiedResponse := "HTTP/1.1200 OK\r\n" +
			"Content-Length: 12\r\n" +
			"Content-Type: text/plain\r\n" +
			"X-Modified: true\r\n" +
			"\r\n" +
			"hello marasi"
		proxy.InterceptFlag = true
		proxy.OnIntercept = func(intercepted *Intercepted) error {
			intercepted.Raw = modifiedResponse
			go func() {
				intercepted.Channel <- InterceptionTuple{
					Resume: true,
				}
			}()
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("setting up request: %v", err)
		}
		originalBody := "marasi original body"
		res := &http.Response{
			Header:        make(http.Header),
			Request:       req,
			StatusCode:    http.StatusNotFound,
			Proto:         "HTTP/1.1",
			Body:          io.NopCloser(strings.NewReader(originalBody)),
			ContentLength: int64(len(originalBody)),
		}
		res.Header.Set("Content-Length", fmt.Sprintf("%d", res.ContentLength))

		err = CheckpointResponseModifier(proxy, res)

		if !errors.Is(err, ErrRebuildResponse) {
			t.Fatalf("wanted: %v\ngot: %v", ErrRebuildResponse, err)
		}
		if len(proxy.InterceptedQueue) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.InterceptedQueue))
		}
	})
}

func TestWriteResponseModifier(t *testing.T) {
	t.Run("modifier should return ErrProxyResponse when it fails to build a ProxyResponse from the response", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
		}
		err := WriteResponseModifier(proxy, res)
		if !errors.Is(err, ErrProxyResponse) {
			t.Fatalf("wanted: %v\ngot: %v", ErrProxyResponse, err)
		}

		if len(proxy.DBWriteChannel) > 0 {
			t.Fatalf("wanted: 0\ngot: %d", len(proxy.DBWriteChannel))
		}

	})

	t.Run("responses should still be written to the DB when onresponse is undefined", func(t *testing.T) {
		proxy := newTestProxy(t)
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)
		_, remove, err := martian.TestContext(req, nil, nil)
		if err != nil {
			t.Fatalf("applying martian context : %v", err)
		}
		defer remove()

		res := &http.Response{
			Header:  make(http.Header),
			Request: req,
			Body:    http.NoBody,
		}

		err = SetupRequestModifier(proxy, req)
		if err != nil {
			t.Fatalf("running SetupRequestModifier : %v", err)
		}
		res.Request = ContextWithResponseTime(res.Request, time.Now())

		err = WriteResponseModifier(proxy, res)

		if !errors.Is(err, ErrResponseHandlerUndefined) {
			t.Fatalf("wanted: %v\ngot: %v", ErrResponseHandlerUndefined, err)
		}

		if len(proxy.DBWriteChannel) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.DBWriteChannel))
		}

	})

	t.Run("proxy response should be written to DBWriteChannel", func(t *testing.T) {
		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generating uuid : %v", err)
		}
		wantTime := time.Now()
		want := &ProxyResponse{
			ID:          wantID,
			Status:      "200 OK",
			StatusCode:  200,
			ContentType: "text/plain",
			Length:      "12",
			Metadata:    make(Metadata),
			RespondedAt: wantTime,
		}
		proxy := newTestProxy(t)
		proxy.OnResponse = func(res ProxyResponse) error {
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app/blog", nil)
		responseBody := "hello marasi"
		res := &http.Response{
			Header:        make(http.Header),
			Request:       req,
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(strings.NewReader(responseBody)),
			ContentLength: int64(len(responseBody)),
		}
		res.Header.Set("Content-Type", "text/plain")
		res.Header.Set("Content-Length", fmt.Sprintf("%d", (len(responseBody))))

		raw, _, err := rawhttp.DumpResponse(res)
		if err != nil {
			t.Fatalf("dumping http response (rawhttp) : %v", err)
		}
		want.Raw = raw

		*req = *ContextWithRequestID(req, wantID)
		*req = *ContextWithRequestTime(req, wantTime)
		*req = *ContextWithMetadata(req, make(Metadata))
		*req = *ContextWithResponseTime(req, wantTime)

		err = WriteResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.DBWriteChannel) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.DBWriteChannel))
		}

		got := <-proxy.DBWriteChannel
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("wanted:\n%v\ngot:\n%v", want, got)
		}

	})

	t.Run("modifier should return nil when OnResponse is defined and a standard response comes in", func(t *testing.T) {
		responseChannel := make(chan ProxyResponse, 1)
		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("generating uuid : %v", err)
		}
		wantTime := time.Now()
		want := &ProxyResponse{
			ID:          wantID,
			Status:      "200 OK",
			StatusCode:  200,
			ContentType: "text/plain",
			Length:      "12",
			Metadata:    make(Metadata),
			RespondedAt: wantTime,
		}
		proxy := newTestProxy(t)
		proxy.OnResponse = func(res ProxyResponse) error {
			responseChannel <- res
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app/blog", nil)
		responseBody := "hello marasi"
		res := &http.Response{
			Header:        make(http.Header),
			Request:       req,
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(strings.NewReader(responseBody)),
			ContentLength: int64(len(responseBody)),
		}
		res.Header.Set("Content-Type", "text/plain")
		res.Header.Set("Content-Length", fmt.Sprintf("%d", (len(responseBody))))

		raw, _, err := rawhttp.DumpResponse(res)
		if err != nil {
			t.Fatalf("dumping http response (rawhttp) : %v", err)
		}
		want.Raw = raw

		*req = *ContextWithRequestID(req, wantID)
		*req = *ContextWithRequestTime(req, wantTime)
		*req = *ContextWithMetadata(req, make(Metadata))
		*req = *ContextWithResponseTime(req, wantTime)

		err = WriteResponseModifier(proxy, res)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if len(proxy.DBWriteChannel) != 1 {
			t.Fatalf("wanted: 1\ngot: %d", len(proxy.DBWriteChannel))
		}

		got := <-proxy.DBWriteChannel
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("wanted: %v\ngot: %v", want, got)
		}

		select {
		case gotFromChannel := <-responseChannel:
			if !reflect.DeepEqual(*want, gotFromChannel) {
				t.Fatalf("wanted: %v\ngot: %v", want, gotFromChannel)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("expected onResponse to be called")
		}
	})
}
