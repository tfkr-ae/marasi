package service

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
)

// AddressFamily is the IP version of an address on a network interface.
type AddressFamily string

const (
	AddressFamilyIPv4 AddressFamily = "ipv4"
	AddressFamilyIPv6 AddressFamily = "ipv6"
)

// ListenerInterface is one address on the instance machine that the proxy
// listener can bind.
type ListenerInterface struct {
	Address   string        `json:"address"`
	Family    AddressFamily `json:"family"`
	Interface string        `json:"interface"`
}

// hostInterface is one network interface and the addresses on it.
type hostInterface struct {
	name  string
	addrs []net.Addr
}

// readHostInterfaces reads the network interfaces of the machine the instance
// runs on, which on a remote instance is not the client's machine.
func readHostInterfaces() ([]hostInterface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing network interfaces: %w", err)
	}
	hosts := make([]hostInterface, 0, len(interfaces))
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			// One unreadable interface should not hide the addresses of the others.
			continue
		}
		hosts = append(hosts, hostInterface{name: iface.Name, addrs: addrs})
	}
	return hosts, nil
}

// listenerInterfaces lists the IP addresses on hosts, IPv4 first, then by
// interface name and address.
func listenerInterfaces(hosts []hostInterface) []ListenerInterface {
	type entry struct {
		addr  netip.Addr
		iface string
	}
	var entries []entry
	for _, host := range hosts {
		for _, addr := range host.addrs {
			var ip net.IP
			switch addr := addr.(type) {
			case *net.IPNet:
				ip = addr.IP
			case *net.IPAddr:
				ip = addr.IP
			}
			parsed, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}
			parsed = parsed.Unmap()
			// A link-local address binds only with its zone.
			if parsed.Is6() && (parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast()) {
				parsed = parsed.WithZone(host.name)
			}
			entries = append(entries, entry{addr: parsed, iface: host.name})
		}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if a.addr.Is4() != b.addr.Is4() {
			if a.addr.Is4() {
				return -1
			}
			return 1
		}
		if order := strings.Compare(a.iface, b.iface); order != 0 {
			return order
		}
		return a.addr.Compare(b.addr)
	})
	items := make([]ListenerInterface, 0, len(entries))
	for _, entry := range entries {
		family := AddressFamilyIPv6
		if entry.addr.Is4() {
			family = AddressFamilyIPv4
		}
		items = append(items, ListenerInterface{Address: entry.addr.String(), Family: family, Interface: entry.iface})
	}
	return items
}

func (s *Server) serveListenerInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	hosts, err := s.interfaces()
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading listener interfaces: %v\n", err)
		writeListenerError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, struct {
		Items []ListenerInterface `json:"items"`
	}{Items: listenerInterfaces(hosts)})
}
