package upstreamtest

import (
	"encoding/binary"
	"io"
	"net"
	"slices"
	"strconv"
	"testing"
)

// SOCKS5 address types (RFC 1928, section 5).
const (
	AddressIPv4   byte = 0x01
	AddressDomain byte = 0x03
	AddressIPv6   byte = 0x04
)

// SOCKS5 authentication methods (RFC 1928, section 3).
const (
	MethodNoAuthentication byte = 0x00
	MethodUsernamePassword byte = 0x02
	methodNoneAcceptable   byte = 0xff
)

const (
	socksVersion       byte = 0x05
	socksAuthVersion   byte = 0x01
	socksCommandTCP    byte = 0x01
	socksSucceeded     byte = 0x00
	socksHostRefused   byte = 0x05
	socksAuthSucceeded byte = 0x00
	socksAuthFailed    byte = 0x01
)

// NewSOCKS5 starts a fake SOCKS5 upstream proxy that answers CONNECT and is
// closed when the test ends. It asks for username/password authentication
// when WithCredentials is set and the client offers it.
func NewSOCKS5(t testing.TB, options ...Option) *Proxy {
	t.Helper()
	return start(t, func(proxy *Proxy) func(net.Conn) { return proxy.serveSOCKS5 }, options)
}

func (proxy *Proxy) serveSOCKS5(conn net.Conn) {
	if proxy.behaviour == Hang {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	tunnel := &Tunnel{}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil || greeting[0] != socksVersion {
		return
	}
	tunnel.Methods = make([]byte, greeting[1])
	if _, err := io.ReadFull(conn, tunnel.Methods); err != nil {
		return
	}
	method := proxy.chooseMethod(tunnel.Methods)
	if _, err := conn.Write([]byte{socksVersion, method}); err != nil || method == methodNoneAcceptable {
		return
	}
	if method == MethodUsernamePassword {
		if !readUsernamePassword(conn, tunnel) {
			return
		}
		if !proxy.credentialsAccepted(tunnel) {
			_, _ = conn.Write([]byte{socksAuthVersion, socksAuthFailed})
			return
		}
		if _, err := conn.Write([]byte{socksAuthVersion, socksAuthSucceeded}); err != nil {
			return
		}
	}
	if !readSOCKSRequest(conn, tunnel) {
		return
	}
	proxy.record(tunnel)
	target, err := proxy.dialTarget(tunnel.Target)
	if err != nil {
		_, _ = conn.Write(socksReply(socksHostRefused))
		return
	}
	if !proxy.track(target) {
		target.Close()
		return
	}
	defer proxy.untrack(target)
	if _, err := conn.Write(socksReply(socksSucceeded)); err != nil {
		return
	}
	Splice(conn, conn, target, tunnel)
}

// chooseMethod picks username/password when the fake wants credentials and
// the client offers it. With RejectCredentials, a client that offers no
// credentials is refused every method.
func (proxy *Proxy) chooseMethod(offered []byte) byte {
	wantsCredentials := proxy.username != ""
	switch {
	case wantsCredentials && slices.Contains(offered, MethodUsernamePassword):
		return MethodUsernamePassword
	case wantsCredentials && proxy.behaviour == RejectCredentials:
		return methodNoneAcceptable
	case slices.Contains(offered, MethodNoAuthentication):
		return MethodNoAuthentication
	}
	return methodNoneAcceptable
}

// readUsernamePassword reads an RFC 1929 request into tunnel.
func readUsernamePassword(conn net.Conn, tunnel *Tunnel) bool {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != socksAuthVersion {
		return false
	}
	username := make([]byte, header[1])
	if _, err := io.ReadFull(conn, username); err != nil {
		return false
	}
	length := make([]byte, 1)
	if _, err := io.ReadFull(conn, length); err != nil {
		return false
	}
	password := make([]byte, length[0])
	if _, err := io.ReadFull(conn, password); err != nil {
		return false
	}
	tunnel.HasCredentials = true
	tunnel.Username, tunnel.Password = string(username), string(password)
	return true
}

// readSOCKSRequest reads a CONNECT request into tunnel's Target and
// AddressType, keeping a domain name as sent.
func readSOCKSRequest(conn net.Conn, tunnel *Tunnel) bool {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != socksVersion || header[1] != socksCommandTCP {
		return false
	}
	tunnel.AddressType = header[3]
	var host string
	switch tunnel.AddressType {
	case AddressIPv4, AddressIPv6:
		size := net.IPv4len
		if tunnel.AddressType == AddressIPv6 {
			size = net.IPv6len
		}
		ip := make(net.IP, size)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return false
		}
		host = ip.String()
	case AddressDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return false
		}
		name := make([]byte, length[0])
		if _, err := io.ReadFull(conn, name); err != nil {
			return false
		}
		host = string(name)
	default:
		return false
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return false
	}
	tunnel.Target = net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port))))
	return true
}

// socksReply is a reply with code and an all-zero IPv4 bound address.
func socksReply(code byte) []byte {
	return []byte{socksVersion, code, 0x00, AddressIPv4, 0, 0, 0, 0, 0, 0}
}
