package marasi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// UpgradeHandler takes over a pair whose origin switched protocols. The proxy
// calls it after the response chain has run and captured the pair, once it has
// written the 101 to the client. The handler owns both connections and closes
// them before it returns.
type UpgradeHandler func(proxy *Proxy, pair *Pair, client, upstream io.ReadWriteCloser) error

// SetUpgradeHandler registers the handler that every switched pair is handed to.
func (proxy *Proxy) SetUpgradeHandler(handler UpgradeHandler) {
	proxy.upgradeHandler = handler
}

// handOffUpgrade hands a pair whose response switched protocols on: a
// WebSocket upgrade to the registered upgrade handler, any other protocol to
// relayOpaque. It does nothing for any other pair. Only the proxy takes the
// client connection: no modifier touches a socket.
func (proxy *Proxy) handOffUpgrade(pair *Pair) error {
	if pair == nil || pair.Response == nil || pair.Response.StatusCode != http.StatusSwitchingProtocols {
		return nil
	}
	upstream, handler := pair.upgradeUpstream, UpgradeHandler(relayOpaque)
	if upstream == nil {
		if pair.control != controlNone {
			// A WebSocket pair the chain stopped early was never prepared.
			return nil
		}
		upstream, handler = pair.webSocketUpstream, proxy.upgradeHandler
	}
	if upstream == nil {
		return nil
	}
	if pair.client == nil {
		return errors.Join(errNoClientConnection, upstream.Close())
	}
	conn, buffered, err := pair.client.Hijack()
	if err != nil {
		return errors.Join(fmt.Errorf("hijacking upgraded client connection: %w", err), upstream.Close())
	}
	client := &marasiws.HijackedConn{Conn: conn, ReadWriter: buffered}
	closeBoth := func(err error) error {
		return errors.Join(err, client.Close(), upstream.Close())
	}
	if handler == nil {
		return closeBoth(errors.New("no upgrade handler is registered"))
	}
	// The engine's per-connection deadline would cut a long session off.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return closeBoth(fmt.Errorf("clearing upgraded client deadline: %w", err))
	}
	if err := writeSwitch(pair, buffered); err != nil {
		return closeBoth(fmt.Errorf("writing switching protocols response: %w", err))
	}
	if err := buffered.Flush(); err != nil {
		return closeBoth(fmt.Errorf("flushing switching protocols response: %w", err))
	}
	return handler(proxy, pair, client, upstream)
}

// detachOpaqueUpgrade takes the origin connection out of the body of a 101 to
// any protocol but WebSocket, such as h2c, before the response chain runs, so
// no modifier reads the connection as a body. WebSocketPrepareModifier
// detaches a WebSocket upgrade's.
func detachOpaqueUpgrade(pair *Pair) {
	res := pair.Response
	if res == nil || res.StatusCode != http.StatusSwitchingProtocols || marasiws.IsUpgradeResponse(res) {
		return
	}
	upstream, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		return
	}
	pair.upgradeUpstream = upstream
	res.Body = http.NoBody
}

// relayOpaque carries the bytes of an upgrade to a protocol Marasi does not
// read, both ways and unchanged, as a tunnel would, until either side ends.
// The pair holds only the 101.
func relayOpaque(_ *Proxy, _ *Pair, client, upstream io.ReadWriteCloser) error {
	done := make(chan struct{}, 2)
	relay := func(dst io.Writer, src io.Reader) {
		io.Copy(dst, src)
		done <- struct{}{}
	}
	go relay(upstream, client)
	go relay(client, upstream)
	<-done
	err := errors.Join(client.Close(), upstream.Close())
	<-done
	return err
}

// writeSwitch writes the 101 to the client: the origin's head with the
// response chain's edits merged, the same bytes capture stored. A pair with
// no wire form gets net/http's form of the view.
func writeSwitch(pair *Pair, w io.Writer) error {
	wire := pair.responseWire
	if wire == nil {
		return pair.Response.Write(w)
	}
	out, err := outboundResponse(wire, pair.Response)
	if err != nil {
		return err
	}
	if _, err := out.head.WriteTo(w); err != nil {
		return err
	}
	_, err = w.Write(out.body)
	return err
}
