package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// startTestControlAddress starts a network address on loopback that serves
// api, and returns it with its control token and bound address.
func startTestControlAddress(t *testing.T, api http.Handler) (*ControlAddress, string, string) {
	t.Helper()
	control := &ControlAddress{
		api:       api,
		token:     NewControlToken(filepath.Join(t.TempDir(), "token"), func(string) error { return nil }),
		logWriter: io.Discard,
		events:    newEventBroadcaster(),
		status:    ControlListenerStatus{Status: ListenerInactive},
		conns:     map[net.Conn]*networkConn{},
	}
	address, port := "127.0.0.1", uint16(0)
	status, err := control.Start(context.Background(), ListenerSettings{Address: &address, Port: &port})
	if err != nil {
		t.Fatal(err)
	}
	token, err := control.token.Value()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Shutdown(context.Background()) })
	return control, token, *status.ControlListener
}

func TestControlAddressShutdown(t *testing.T) {
	t.Run("should not stall on a request in flight that reads the network address's state", func(t *testing.T) {
		entered := make(chan struct{})
		var control *ControlAddress
		api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			// Let Shutdown start waiting for this request first.
			time.Sleep(100 * time.Millisecond)
			_ = control.closesConnection(r)
			w.WriteHeader(http.StatusNoContent)
		})
		control, token, address := startTestControlAddress(t, api)

		request, err := http.NewRequest(http.MethodGet, "http://"+address+"/service/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		responded := make(chan error, 1)
		go func() {
			response, err := http.DefaultClient.Do(request)
			if err == nil {
				response.Body.Close()
			}
			responded <- err
		}()
		<-entered

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		started := time.Now()
		err = control.Shutdown(ctx)
		if elapsed := time.Since(started); err != nil || elapsed > time.Second {
			t.Fatalf("\nwanted:\nShutdown returns nil within 1s\ngot:\n%v after %s", err, elapsed)
		}
		if err := <-responded; err != nil {
			t.Fatalf("\nwanted:\nthe request in flight answered\ngot:\n%v", err)
		}
	})
}

func TestControlAddressResumeAfterHandover(t *testing.T) {
	t.Run("should make Shutdown wait for a request in flight when the address cannot be bound again", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var finished atomic.Bool
		api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			finished.Store(true)
			w.WriteHeader(http.StatusNoContent)
		})
		control, token, address := startTestControlAddress(t, api)

		request, err := http.NewRequest(http.MethodPost, "http://"+address+"/service/restart", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		go func() {
			if response, err := http.DefaultClient.Do(request); err == nil {
				response.Body.Close()
			}
		}()
		<-entered

		if _, err := control.suspendForHandover(); err != nil {
			t.Fatal(err)
		}
		occupier, err := net.Listen("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		defer occupier.Close()
		if err := control.resumeAfterHandover(context.Background()); err == nil {
			t.Fatal("\nwanted:\nresume fails on an occupied address\ngot:\nnil")
		}

		go func() {
			time.Sleep(100 * time.Millisecond)
			close(release)
		}()
		if err := control.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !finished.Load() {
			t.Fatal("\nwanted:\nShutdown returns after the request in flight finished\ngot:\nShutdown returned first")
		}
	})
}
