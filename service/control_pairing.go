package service

import (
	"net"
	"net/http"
	"net/url"
)

// Pairing is what a client needs to connect to the control listener's
// network address: its URL, the control password when one is set, and the
// instance's name. A QR code of it is how Pairing hands it to another device.
type Pairing struct {
	URL      string `json:"url"`
	Password string `json:"password,omitempty"`
	Instance string `json:"instance"`
}

// Addresses that only the default route reaches, one per address family.
// Connecting a UDP socket to one picks the interface without sending a
// packet. They are documentation addresses, so nothing would answer if a
// packet were sent.
const (
	defaultRouteProbeIPv4 = "192.0.2.1:9"
	defaultRouteProbeIPv6 = "[2001:db8::1]:9"
)

// servePairing answers with the Pairing payload, only on the socket: the
// password it carries must never reach a client that does not already
// hold it, and only someone on the instance machine with socket access may
// read it from the instance.
func (s *Server) servePairing(w http.ResponseWriter, r *http.Request) {
	if _, onNetwork := r.Context().Value(networkConnKey{}).(*networkConn); onNetwork {
		writeRefusal(w, r, http.StatusForbidden, "socket_only", "pairing is available only through the socket")
		return
	}
	status := s.control.Status()
	if status.ControlListener == nil {
		writeRefusal(w, r, http.StatusConflict, "listener_inactive", "the control listener has no network address running")
		return
	}
	host, port, err := net.SplitHostPort(*status.ControlListener)
	if err != nil {
		writeListenerError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, Pairing{
		URL:      (&url.URL{Scheme: "http", Host: net.JoinHostPort(reachableHost(host), port)}).String(),
		Password: s.control.Password(),
		Instance: s.instance,
	})
}

// reachableHost is the host another device should use for a network
// address bound on host. A wildcard address is replaced with the IP of the
// interface on the default route, or loopback when there is no default
// route, since no device elsewhere can connect then anyway.
func reachableHost(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return host
	}
	network, probe, loopback := "udp6", defaultRouteProbeIPv6, net.IPv6loopback
	if ip.To4() != nil {
		network, probe, loopback = "udp4", defaultRouteProbeIPv4, net.IPv4(127, 0, 0, 1)
	}
	conn, err := net.Dial(network, probe)
	if err != nil {
		return loopback.String()
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
