package marasi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tfkr-ae/marasi/core"
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

// handOffUpgrade hands a pair whose response switched protocols to the upgrade
// handler. It does nothing for any other pair. Only the proxy takes the client
// connection: no modifier touches a socket.
func (proxy *Proxy) handOffUpgrade(pair *Pair) error {
	if pair == nil || pair.Response == nil || pair.Response.StatusCode != http.StatusSwitchingProtocols {
		return nil
	}
	upstream := pair.webSocketUpstream
	if upstream == nil {
		return nil
	}
	session, ok := core.SessionFromContext(pair.Request.Context())
	if !ok {
		return errors.Join(ErrSessionContext, upstream.Close())
	}
	conn, buffered, err := session.Hijack()
	if err != nil {
		return errors.Join(fmt.Errorf("hijacking upgraded client connection: %w", err), upstream.Close())
	}
	client := &marasiws.HijackedConn{Conn: conn, ReadWriter: buffered}
	closeBoth := func(err error) error {
		return errors.Join(err, client.Close(), upstream.Close())
	}
	if proxy.upgradeHandler == nil {
		return closeBoth(errors.New("no upgrade handler is registered"))
	}
	// Martian's per-connection deadline would cut a long session off.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return closeBoth(fmt.Errorf("clearing upgraded client deadline: %w", err))
	}
	if err := pair.Response.Write(buffered); err != nil {
		return closeBoth(fmt.Errorf("writing switching protocols response: %w", err))
	}
	if err := buffered.Flush(); err != nil {
		return closeBoth(fmt.Errorf("flushing switching protocols response: %w", err))
	}
	return proxy.upgradeHandler(proxy, pair, client, upstream)
}
