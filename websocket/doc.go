// Package websocket implements WebSocket framing, connection relay, and
// interception helpers used by the Marasi proxy.
//
// It covers RFC 6455 frame encoding and decoding, upgrade detection, live
// bidirectional connections, a connection registry, and message interception
// for manual inspection and modification.
package websocket
