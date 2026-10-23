package marasi

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
)

// SOCKS5 protocol values from RFC 1928 and RFC 1929.
const (
	socks5Version             byte = 0x05
	socks5UserPassVersion     byte = 0x01
	socks5MethodNoAuth        byte = 0x00
	socks5MethodUserPass      byte = 0x02
	socks5MethodNoneAccepted  byte = 0xff
	socks5CommandConnect      byte = 0x01
	socks5AddressIPv4         byte = 0x01
	socks5AddressDomain       byte = 0x03
	socks5AddressIPv6         byte = 0x04
	socks5ReplySucceeded      byte = 0x00
	socks5UserPassSucceeded   byte = 0x00
	socks5MaxCredentialLength      = 255
	socks5MaxDomainLength          = 255
	// socks5OfferedMethods is how many methods the greeting offers: one,
	// no authentication or username/password, never both.
	socks5OfferedMethods byte = 1
	// socks5PortLength is the size of the port that ends an address.
	socks5PortLength = 2
)

// socks5Replies names the reply codes of RFC 1928, section 6.
var socks5Replies = map[byte]string{
	0x01: "general SOCKS server failure",
	0x02: "connection not allowed by ruleset",
	0x03: "network unreachable",
	0x04: "host unreachable",
	0x05: "connection refused by the target",
	0x06: "TTL expired",
	0x07: "command not supported",
	0x08: "address type not supported",
}

// socks5Connect negotiates authentication on conn and asks the SOCKS5
// upstream proxy to connect to address. A hostname is sent unresolved, so
// the proxy resolves it: internal-only names work and target names never
// reach the local resolver.
func (route *Route) socks5Connect(ctx context.Context, conn net.Conn, address string) (net.Conn, error) {
	request, err := socks5Request(address)
	if err != nil {
		return nil, route.fail(UpstreamProxyInvalid, err.Error(), err)
	}
	err = route.exchange(ctx, conn, func() error {
		if err := route.socks5Authenticate(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.Write(request); err != nil {
			return route.classify("sending the SOCKS5 request", contextCause(ctx, err))
		}
		// VER, REP, RSV, ATYP, then the bound address, which is not needed.
		reply := make([]byte, 4)
		if _, err := io.ReadFull(conn, reply); err != nil {
			return route.classify("reading the SOCKS5 reply", contextCause(ctx, err))
		}
		if reply[0] != socks5Version {
			return route.fail(UpstreamProxyFailed, fmt.Sprintf("answered with SOCKS version %d", reply[0]), nil)
		}
		if reply[1] != socks5ReplySucceeded {
			reason, ok := socks5Replies[reply[1]]
			if !ok {
				reason = fmt.Sprintf("reply code %d", reply[1])
			}
			return route.fail(UpstreamProxyRejected, "refused to connect to "+address+": "+reason, nil)
		}
		if err := discardSOCKS5Address(conn, reply[3]); err != nil {
			return route.classify("reading the SOCKS5 reply", contextCause(ctx, err))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// socks5Authenticate offers no authentication, or only username/password
// when the entry has credentials, and runs the method the proxy picks.
func (route *Route) socks5Authenticate(ctx context.Context, conn net.Conn) error {
	method := socks5MethodNoAuth
	if route.credentials != nil {
		method = socks5MethodUserPass
	}
	if _, err := conn.Write([]byte{socks5Version, socks5OfferedMethods, method}); err != nil {
		return route.classify("sending the SOCKS5 greeting", contextCause(ctx, err))
	}
	chosen := make([]byte, 2)
	if _, err := io.ReadFull(conn, chosen); err != nil {
		return route.classify("reading the SOCKS5 method", contextCause(ctx, err))
	}
	switch {
	case chosen[0] != socks5Version:
		return route.fail(UpstreamProxyFailed, fmt.Sprintf("answered with SOCKS version %d", chosen[0]), nil)
	case chosen[1] == socks5MethodNoneAccepted && route.credentials == nil:
		return route.fail(UpstreamProxyAuthRejected, "wants credentials, and the entry has none", nil)
	case chosen[1] == socks5MethodNoneAccepted:
		return route.fail(UpstreamProxyAuthRejected, "refused username/password authentication", nil)
	case chosen[1] != method:
		return route.fail(UpstreamProxyFailed, fmt.Sprintf("chose authentication method %d, which was not offered", chosen[1]), nil)
	case method == socks5MethodNoAuth:
		return nil
	}

	username := route.credentials.Username()
	password, _ := route.credentials.Password()
	if len(username) == 0 || len(username) > socks5MaxCredentialLength || len(password) == 0 || len(password) > socks5MaxCredentialLength {
		return route.fail(UpstreamProxyInvalid, "SOCKS5 credentials must each be 1 to 255 bytes", nil)
	}
	request := []byte{socks5UserPassVersion, byte(len(username))}
	request = append(request, username...)
	request = append(request, byte(len(password)))
	request = append(request, password...)
	if _, err := conn.Write(request); err != nil {
		return route.classify("sending the SOCKS5 credentials", contextCause(ctx, err))
	}
	status := make([]byte, 2)
	if _, err := io.ReadFull(conn, status); err != nil {
		return route.classify("reading the SOCKS5 authentication status", contextCause(ctx, err))
	}
	if status[1] != socks5UserPassSucceeded {
		return route.fail(UpstreamProxyAuthRejected, "rejected the credentials", nil)
	}
	return nil
}

// socks5Request builds a CONNECT request for address. An IP literal is sent
// as an IP and anything else as a domain name, unresolved.
func socks5Request(address string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("splitting the target %q: %w", address, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("parsing the target port %q: %w", portText, err)
	}
	request := []byte{socks5Version, socks5CommandConnect, 0x00}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Is4() || ip.Is4In6() {
			request = append(request, socks5AddressIPv4)
			request = append(request, ip.Unmap().AsSlice()...)
		} else {
			request = append(request, socks5AddressIPv6)
			request = append(request, ip.AsSlice()...)
		}
	} else {
		if len(host) == 0 || len(host) > socks5MaxDomainLength {
			return nil, fmt.Errorf("the target host %q must be 1 to 255 bytes", host)
		}
		request = append(request, socks5AddressDomain, byte(len(host)))
		request = append(request, host...)
	}
	return binary.BigEndian.AppendUint16(request, uint16(port)), nil
}

// discardSOCKS5Address reads past a reply's bound address and port.
func discardSOCKS5Address(conn net.Conn, addressType byte) error {
	var length int
	switch addressType {
	case socks5AddressIPv4:
		length = net.IPv4len
	case socks5AddressIPv6:
		length = net.IPv6len
	case socks5AddressDomain:
		size := make([]byte, 1)
		if _, err := io.ReadFull(conn, size); err != nil {
			return err
		}
		length = int(size[0])
	default:
		return fmt.Errorf("reading a bound address of unknown type %d", addressType)
	}
	_, err := io.ReadFull(conn, make([]byte, length+socks5PortLength))
	return err
}
