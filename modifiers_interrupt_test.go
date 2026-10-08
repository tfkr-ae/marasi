package marasi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/martian"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

const blockingHookBody = `print("entered"); while true do end`

// hookCase names one Lua hook and the modifier or helper that calls it.
type hookCase struct {
	extension string
	function  string
}

// interruptibleHook gives ext a cancellable execution context and reports when Lua prints.
func interruptibleHook(t *testing.T, ext *extensions.Runtime) <-chan struct{} {
	t.Helper()
	ext.ExecutionContext, ext.CancelExecution = context.WithCancel(context.Background())
	t.Cleanup(ext.CancelExecution)
	entered := make(chan struct{})
	var once sync.Once
	ext.OnLog = func(extensions.ExtensionLog) error {
		once.Do(func() { close(entered) })
		return nil
	}
	return entered
}

// awaitEntered cancels once the hook is inside Lua, then waits for the caller to return.
func awaitEntered(t *testing.T, entered <-chan struct{}, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not enter Lua")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hook was not interrupted")
	}
}

// runRequestModifier runs modifier, then forwards req to its target unless the round trip
// is skipped, as martian does after the base pipeline turns ErrDropped into nil.
func runRequestModifier(proxy *Proxy, modifier RequestModifierFunc, req *http.Request) error {
	err := modifier(proxy, req)
	if !martian.NewContext(req).SkippingRoundTrip() {
		// The round trip ignores cancellation so the test sees the forwarding decision itself.
		forwarded := req.Clone(context.WithoutCancel(req.Context()))
		forwarded.RequestURI = ""
		if res, rtErr := http.DefaultTransport.RoundTrip(forwarded); rtErr == nil {
			res.Body.Close()
		}
	}
	return err
}

func TestRequestModifiersFailClosedWhenInterrupted(t *testing.T) {
	modifiers := []struct {
		hookCase
		modifier RequestModifierFunc
	}{
		{hookCase{"compass", "processRequest"}, CompassRequestModifier},
		{hookCase{"workshop", "processRequest"}, ExtensionsRequestModifier},
		{hookCase{"checkpoint", "interceptRequest"}, CheckpointRequestModifier},
	}
	for _, tc := range modifiers {
		for _, source := range []string{"execution context", "request context"} {
			t.Run(tc.extension+" "+tc.function+" interrupted by "+source+" should not reach the origin", func(t *testing.T) {
				var hits atomic.Int32
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
				}))
				defer origin.Close()

				proxy := newTestProxy(t, testExtensions[tc.extension])
				updateExtension(t, proxy, tc.extension, "function "+tc.function+"(request) "+blockingHookBody+" end")
				ext, _ := proxy.GetExtension(tc.extension)
				entered := interruptibleHook(t, ext)

				reqCtx, cancelRequest := context.WithCancel(context.Background())
				defer cancelRequest()
				req := httptest.NewRequest(http.MethodGet, origin.URL, nil).WithContext(reqCtx)
				_, remove, err := martian.TestContext(req, nil, nil)
				if err != nil {
					t.Fatalf("applying martian context : %v", err)
				}
				defer remove()

				cancel := ext.CancelExecution
				if source == "request context" {
					cancel = cancelRequest
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					err = runRequestModifier(proxy, tc.modifier, req)
				}()
				awaitEntered(t, entered, cancel, done)

				if got := hits.Load(); got != 0 {
					t.Fatalf("origin received %d requests after an interrupted hook", got)
				}
				if !errors.Is(err, ErrDropped) {
					t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
				}
			})
		}

		t.Run(tc.extension+" "+tc.function+" Lua error should still reach the origin", func(t *testing.T) {
			var hits atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
			}))
			defer origin.Close()

			proxy := newTestProxy(t, testExtensions[tc.extension])
			updateExtension(t, proxy, tc.extension, "function "+tc.function+`(request) error("boom") end`)
			req := httptest.NewRequest(http.MethodGet, origin.URL, nil)
			_, remove, err := martian.TestContext(req, nil, nil)
			if err != nil {
				t.Fatalf("applying martian context : %v", err)
			}
			defer remove()

			if err := runRequestModifier(proxy, tc.modifier, req); err != nil {
				t.Fatalf("wanted: nil\ngot: %v", err)
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("wanted origin to receive 1 request\ngot: %d", got)
			}
		})
	}
}

func TestResponseModifiersFailClosedWhenInterrupted(t *testing.T) {
	modifiers := []struct {
		hookCase
		modifier ResponseModifierFunc
	}{
		{hookCase{"compass", "processResponse"}, CompassResponseModifier},
		{hookCase{"workshop", "processResponse"}, ExtensionsResponseModifier},
		{hookCase{"checkpoint", "interceptResponse"}, CheckpointResponseModifier},
	}
	for _, tc := range modifiers {
		for _, source := range []string{"execution context", "request context"} {
			t.Run(tc.extension+" "+tc.function+" interrupted by "+source+" should drop the response", func(t *testing.T) {
				proxy := newTestProxy(t, testExtensions[tc.extension])
				updateExtension(t, proxy, tc.extension, "function "+tc.function+"(response) "+blockingHookBody+" end")
				ext, _ := proxy.GetExtension(tc.extension)
				entered := interruptibleHook(t, ext)

				reqCtx, cancelRequest := context.WithCancel(context.Background())
				defer cancelRequest()
				res := testResponse("held")
				res.Request = httptest.NewRequest(http.MethodGet, "https://marasi.app", nil).WithContext(reqCtx)

				cancel := ext.CancelExecution
				if source == "request context" {
					cancel = cancelRequest
				}
				var err error
				done := make(chan struct{})
				go func() {
					defer close(done)
					err = tc.modifier(proxy, res)
				}()
				awaitEntered(t, entered, cancel, done)

				if !errors.Is(err, ErrDropped) {
					t.Fatalf("wanted: %v\ngot: %v", ErrDropped, err)
				}
			})
		}

		t.Run(tc.extension+" "+tc.function+" Lua error should still return the response", func(t *testing.T) {
			proxy := newTestProxy(t, testExtensions[tc.extension])
			updateExtension(t, proxy, tc.extension, "function "+tc.function+`(response) error("boom") end`)
			res := testResponse("held")
			res.Request = httptest.NewRequest(http.MethodGet, "https://marasi.app", nil)

			if err := tc.modifier(proxy, res); err != nil {
				t.Fatalf("wanted: nil\ngot: %v", err)
			}
		})
	}
}

func TestWebSocketHooksFailClosedWhenInterrupted(t *testing.T) {
	hooks := []struct {
		hookCase
		run func(*Proxy, *marasiws.Message)
	}{
		{hookCase{"workshop", "processWebSocketMessage"}, processWebSocketMessageExtensions},
		{hookCase{"checkpoint", "interceptWebSocketMessage"}, func(proxy *Proxy, message *marasiws.Message) {
			shouldInterceptWebSocketMessage(proxy, message)
		}},
	}
	for _, tc := range hooks {
		t.Run(tc.extension+" "+tc.function+" interrupted by execution context should drop the message", func(t *testing.T) {
			proxy := newTestProxy(t, &domain.Extension{
				ID:         testExtensions[tc.extension].ID,
				Name:       tc.extension,
				LuaContent: "function " + tc.function + "(message) " + blockingHookBody + " end",
			})
			ext, _ := proxy.GetExtension(tc.extension)
			entered := interruptibleHook(t, ext)

			message := &marasiws.Message{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				tc.run(proxy, message)
			}()
			awaitEntered(t, entered, ext.CancelExecution, done)

			if !message.Dropped {
				t.Fatal("wanted interrupted hook to drop the message")
			}
		})

		t.Run(tc.extension+" "+tc.function+" Lua error should not drop the message", func(t *testing.T) {
			proxy := newTestProxy(t, &domain.Extension{
				ID:         testExtensions[tc.extension].ID,
				Name:       tc.extension,
				LuaContent: "function " + tc.function + `(message) error("boom") end`,
			})

			message := &marasiws.Message{}
			tc.run(proxy, message)

			if message.Dropped {
				t.Fatal("wanted script error to leave the message undropped")
			}
		})
	}
}
