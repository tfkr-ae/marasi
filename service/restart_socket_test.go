package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

const socketRestartPassword = "test password"

// socketRestart is an instance with a proxy listener and a control listener
// network address on loopback ports the kernel picked.
type socketRestart struct {
	server   *Server
	listener *listenerLifecycle
	proxy    string // proxy listener address
	control  string // control listener network address
}

func newSocketRestart(t *testing.T, start SuccessorStart) *socketRestart {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot hand a socket to a successor, so a restart binds the address again")
	}
	lifecycle, proxy, dir := newTestProjectLifecycle(t)
	current := canonicalProjectPath(t, filepath.Join(dir, "current.marasi"))
	if err := lifecycle.Open(context.Background(), current); err != nil {
		t.Fatalf("opening current project: %v", err)
	}
	listener := NewListenerLifecycle(proxy, io.Discard)
	listener.BindProject(lifecycle)
	server := NewServer(proxy, listener, lifecycle, func() {}, "01.10.2026", "work", current)
	t.Cleanup(func() {
		server.Close()
		_ = listener.Shutdown()
	})
	status, err := listener.Start(context.Background(), listenerSettings("127.0.0.1", 0))
	if err != nil {
		t.Fatalf("starting proxy listener: %v", err)
	}
	control := server.EnableControlAddress(socketRestartPassword, nil, io.Discard)
	t.Cleanup(func() { _ = control.Shutdown(context.Background()) })
	controlStatus, err := control.Start(context.Background(), listenerSettings("127.0.0.1", 0), ControlAccess{})
	if err != nil {
		t.Fatalf("starting control listener network address: %v", err)
	}
	server.EnableRestart(start)
	return &socketRestart{server: server, listener: listener, proxy: *status.ProxyListener, control: *controlStatus.ControlListener}
}

// assertAddressHeld fails unless address is still bound, so no other socket
// on the host can be given it.
func assertAddressHeld(t *testing.T, name, address string) {
	t.Helper()
	occupier, err := net.Listen("tcp", address)
	if err == nil {
		occupier.Close()
		t.Errorf("\nwanted:\n%s %s still bound\ngot:\nfree for anyone to bind", name, address)
		return
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("\nwanted:\naddress already in use for %s %s\ngot:\n%v", name, address, err)
	}
}

// occupyIfFree binds address when the restart left it free, as any other
// socket on the host could, and keeps it until the test ends.
func occupyIfFree(t *testing.T, address string) {
	t.Helper()
	if occupier, err := net.Listen("tcp", address); err == nil {
		t.Cleanup(func() { occupier.Close() })
	}
}

// assertReachable fails unless a dial to address is accepted by a listener
// serving socket, the handed-over socket.
func assertReachable(t *testing.T, name, address string, socket *os.File) {
	t.Helper()
	if socket == nil {
		t.Errorf("\nwanted:\n%s socket handed over\ngot:\nnone", name)
		return
	}
	listener, err := net.FileListener(socket)
	if err != nil {
		t.Errorf("serving the handed-over %s socket: %v", name, err)
		return
	}
	defer listener.Close()
	client, err := net.Dial("tcp", address)
	if err != nil {
		t.Errorf("\nwanted:\na dial to %s %s accepted\ngot:\n%v", name, address, err)
		return
	}
	defer client.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Errorf("\nwanted:\nthe handed-over %s socket accepts the dial\ngot:\n%v", name, err)
		return
	}
	accepted.Close()
}

// statusCode sends GET path to the control listener network address.
func statusCode(t *testing.T, address, path string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+socketRestartPassword)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("\nwanted:\n%s answered on %s\ngot:\n%v", path, address, err)
	}
	response.Body.Close()
	return response.StatusCode
}

func TestServiceRestartKeepsListenerSockets(t *testing.T) {
	t.Run("should serve again from the same sockets when the successor fails", func(t *testing.T) {
		restart := newSocketRestart(t, func(_ context.Context, handover Handover) (Successor, error) {
			occupyIfFree(t, *handover.ProxyListener)
			occupyIfFree(t, *handover.ControlListener)
			return Successor{}, &RestartError{Log: "successor.log", Err: errors.New("successor failed to start")}
		})

		response := requestUpdate(restart.server, http.MethodPost, "/service/restart", "")

		if response.Code != http.StatusInternalServerError {
			t.Fatalf("\nwanted:\n500 restart_failed\ngot:\n%d %s", response.Code, response.Body.String())
		}
		if status := restart.listener.Status(); status.ProxyListener == nil || *status.ProxyListener != restart.proxy {
			t.Fatalf("\nwanted:\nproxy listener active on %s\ngot:\n%+v\n%s", restart.proxy, status, response.Body.String())
		}
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "origin") }))
		defer origin.Close()
		proxyURL, err := url.Parse("http://" + restart.proxy)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableCompression: true}}
		proxied, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("\nwanted:\na request through %s answered\ngot:\n%v", restart.proxy, err)
		}
		body, _ := io.ReadAll(proxied.Body)
		proxied.Body.Close()
		if proxied.StatusCode != http.StatusOK || string(body) != "origin" {
			t.Fatalf("\nwanted:\n200 origin\ngot:\n%d %q", proxied.StatusCode, body)
		}
		if code := statusCode(t, restart.control, "/service/status"); code != http.StatusOK {
			t.Fatalf("\nwanted:\n200 from the control listener network address %s\ngot:\n%d", restart.control, code)
		}
	})

	t.Run("should keep the proxy and control sockets bound while the successor starts", func(t *testing.T) {
		var restart *socketRestart
		started := false
		restart = newSocketRestart(t, func(_ context.Context, handover Handover) (Successor, error) {
			started = true
			assertAddressHeld(t, "proxy listener", *handover.ProxyListener)
			assertAddressHeld(t, "control listener network address", *handover.ControlListener)
			assertReachable(t, "proxy listener", *handover.ProxyListener, handover.ProxyListenerSocket)
			assertReachable(t, "control listener network address", *handover.ControlListener, handover.ControlListenerSocket)
			return Successor{Version: "21.10.2026", ProxyListener: handover.ProxyListener}, nil
		})

		response := requestUpdate(restart.server, http.MethodPost, "/service/restart", "")

		if response.Code != http.StatusOK || !started {
			t.Fatalf("\nwanted:\n200 after the successor started\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("should release handed-over sockets and retry by address when the successor rejects socket handover", func(t *testing.T) {
		var restart *socketRestart
		attempts := 0
		restart = newSocketRestart(t, func(_ context.Context, handover Handover) (Successor, error) {
			attempts++
			if attempts == 1 {
				if handover.ProxyListenerSocket == nil || handover.ControlListenerSocket == nil {
					t.Fatalf("\nwanted:\nboth listener sockets on the first attempt\ngot:\n%+v", handover)
				}
				return Successor{}, &RestartError{Log: "successor.log", Err: ErrSocketHandoverRejected}
			}
			if handover.ProxyListenerSocket != nil || handover.ControlListenerSocket != nil {
				t.Fatalf("\nwanted:\nno listener sockets on the retry\ngot:\n%+v", handover)
			}
			for _, endpoint := range []string{*handover.ProxyListener, *handover.ControlListener} {
				listener, err := net.Listen("tcp", endpoint)
				if err != nil {
					t.Fatalf("\nwanted:\n%s released for the retry to bind\ngot:\n%v", endpoint, err)
				}
				listener.Close()
			}
			return Successor{Version: "30.10.2026", ProxyListener: handover.ProxyListener}, nil
		})

		response := requestUpdate(restart.server, http.MethodPost, "/service/restart", "")

		if response.Code != http.StatusOK || attempts != 2 {
			t.Fatalf("\nwanted:\n200 after 2 attempts\ngot:\n%d after %d attempts: %s", response.Code, attempts, response.Body.String())
		}
	})

	t.Run("should rebind both addresses on rollback when the retry after rejected socket handover fails", func(t *testing.T) {
		var restart *socketRestart
		attempts := 0
		restart = newSocketRestart(t, func(_ context.Context, handover Handover) (Successor, error) {
			attempts++
			if attempts == 1 {
				return Successor{}, &RestartError{Log: "successor.log", Err: ErrSocketHandoverRejected}
			}
			return Successor{}, &RestartError{Log: "successor.log", Err: errors.New("successor failed to start: exit status 1: broken build")}
		})

		response := requestUpdate(restart.server, http.MethodPost, "/service/restart", "")

		if response.Code != http.StatusInternalServerError || attempts != 2 {
			t.Fatalf("\nwanted:\n500 after 2 attempts\ngot:\n%d after %d attempts: %s", response.Code, attempts, response.Body.String())
		}
		if status := restart.listener.Status(); status.ProxyListener == nil || *status.ProxyListener != restart.proxy {
			t.Fatalf("\nwanted:\nproxy listener active on %s\ngot:\n%+v", restart.proxy, status)
		}
		if code := statusCode(t, restart.control, "/service/status"); code != http.StatusOK {
			t.Fatalf("\nwanted:\n200 from the rebound control listener network address %s\ngot:\n%d", restart.control, code)
		}
	})
}
