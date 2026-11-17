package h2client

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// serveTLS listens on loopback and completes a TLS handshake with config
// on every connection, after answer when it is set, then closes it.
func serveTLS(t *testing.T, config *tls.Config, answer func(net.Conn) error) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if answer != nil && answer(conn) != nil {
					return
				}
				_ = tls.Server(conn, config).Handshake()
			}()
		}
	}()
	return listener.Addr().String()
}

// answerConnect reads a CONNECT request and answers 200, as a proxy that
// then intercepts the TLS inside it does.
func answerConnect(conn net.Conn) error {
	request, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return err
	}
	if request.Method != http.MethodConnect {
		return errors.New("not CONNECT")
	}
	_, err = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	return err
}

func serverTLS(t *testing.T, ca *conformance.CA) *tls.Config {
	t.Helper()
	config, err := ca.ServerTLS(TargetHost)
	if err != nil {
		t.Fatal(err)
	}
	config.NextProtos = []string{"h2"}
	return config
}

func newCA(t *testing.T) *conformance.CA {
	t.Helper()
	ca, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func startShim(t *testing.T, options shim.Options) *shim.Shim {
	t.Helper()
	options.Target = TargetHost + ":443"
	s, err := shim.Start(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCheckRoute(t *testing.T) {
	harnessCA, proxyCA := newCA(t), newCA(t)
	nginx := serveTLS(t, serverTLS(t, harnessCA), nil)
	intercepting := serveTLS(t, serverTLS(t, proxyCA), answerConnect)

	t.Run("should fail a proxied run whose shim does not go through the proxy", func(t *testing.T) {
		s := startShim(t, shim.Options{Dial: nginx})
		job := conformance.Job{Run: conformance.RunProxy, Proxy: intercepting, CA: harnessCA, ProxyCA: proxyCA.PEM()}
		if err := checkRoute(context.Background(), s.Address(), job); !errors.Is(err, conformance.ErrNotProxied) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", conformance.ErrNotProxied, err)
		}
	})

	t.Run("should pass a proxied run whose TLS the proxy intercepts", func(t *testing.T) {
		s := startShim(t, shim.Options{Proxy: intercepting})
		job := conformance.Job{Run: conformance.RunProxy, Proxy: intercepting, CA: harnessCA, ProxyCA: proxyCA.PEM()}
		if err := checkRoute(context.Background(), s.Address(), job); err != nil {
			t.Fatalf("\nwanted:\nno error\ngot:\n%v", err)
		}
	})

	t.Run("should fail a proxied run when the proxy gives no CA certificate", func(t *testing.T) {
		s := startShim(t, shim.Options{Proxy: intercepting})
		job := conformance.Job{Run: conformance.RunProxy, Proxy: intercepting, CA: harnessCA}
		if err := checkRoute(context.Background(), s.Address(), job); !errors.Is(err, conformance.ErrNotProxied) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", conformance.ErrNotProxied, err)
		}
	})

	t.Run("should pass a direct run that reaches nginx", func(t *testing.T) {
		s := startShim(t, shim.Options{Dial: nginx})
		job := conformance.Job{Run: conformance.RunDirect, CA: harnessCA}
		if err := checkRoute(context.Background(), s.Address(), job); err != nil {
			t.Fatalf("\nwanted:\nno error\ngot:\n%v", err)
		}
	})
}
