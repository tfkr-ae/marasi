package websocket

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/domain"
)

// ExtensionsHeader is the handshake header that offers and accepts WebSocket extensions.
const ExtensionsHeader = "Sec-WebSocket-Extensions"

// deflateExtension is the only WebSocket extension the proxy negotiates (RFC 7692).
const deflateExtension = "permessage-deflate"

// FilterExtensionOffers keeps only the permessage-deflate offers in header,
// in their original order and text, and returns the names of the other
// WebSocket extensions it removed. Each removed name is listed once.
func FilterExtensionOffers(header http.Header) []string {
	values := header.Values(ExtensionsHeader)
	if len(values) == 0 {
		return nil
	}

	var kept []string
	var removed []string
	for _, value := range values {
		for _, element := range splitOutsideQuotes(value, ',') {
			element = strings.TrimSpace(element)
			if element == "" {
				continue
			}
			name := extensionName(element)
			if strings.EqualFold(name, deflateExtension) {
				kept = append(kept, element)
				continue
			}
			if !slices.Contains(removed, name) {
				removed = append(removed, name)
			}
		}
	}

	header.Del(ExtensionsHeader)
	if len(kept) > 0 {
		header.Set(ExtensionsHeader, strings.Join(kept, ", "))
	}
	return removed
}

// ErrDeflateNegotiation reports an upstream extension response that the proxy
// cannot honour: it is malformed, names an extension other than
// permessage-deflate, or accepts something no offer allowed.
var ErrDeflateNegotiation = errors.New("websocket extension negotiation failed")

// NegotiateDeflate checks the upstream's Sec-WebSocket-Extensions response
// against the offers sent upstream (RFC 7692 section 7). It returns nil when the
// upstream declined deflate, and the negotiated configuration when it accepted
// one of the offers. Window sizes the upstream did not limit are 15.
func NegotiateDeflate(offers, response []string) (*domain.WebSocketDeflate, error) {
	var elements []string
	for _, value := range response {
		for _, element := range splitOutsideQuotes(value, ',') {
			// HTTP list syntax allows empty elements (RFC 9110 section 5.6.1).
			if strings.TrimSpace(element) != "" {
				elements = append(elements, element)
			}
		}
	}
	if len(elements) == 0 {
		return nil, nil
	}
	if len(elements) > 1 {
		return nil, fmt.Errorf("%w: upstream accepted %d extensions, want one", ErrDeflateNegotiation, len(elements))
	}

	accepted, err := parseDeflateElement(elements[0])
	if err != nil {
		return nil, fmt.Errorf("%w: upstream response %q: %w", ErrDeflateNegotiation, strings.TrimSpace(elements[0]), err)
	}
	if accepted.clientMaxWindowBits == windowBitsNoValue {
		return nil, fmt.Errorf("%w: upstream response has client_max_window_bits without a value", ErrDeflateNegotiation)
	}

	for _, value := range offers {
		for _, element := range splitOutsideQuotes(value, ',') {
			offer, err := parseDeflateElement(element)
			if err != nil {
				continue
			}
			if accepted.answers(offer) {
				return accepted.params(), nil
			}
		}
	}
	return nil, fmt.Errorf("%w: upstream response %q answers no permessage-deflate offer", ErrDeflateNegotiation, strings.TrimSpace(elements[0]))
}

const (
	// windowBitsAbsent marks a *_max_window_bits parameter that is not present.
	windowBitsAbsent = 0
	// windowBitsNoValue marks a client_max_window_bits parameter with no value.
	windowBitsNoValue = -1
)

// deflateElement is one parsed permessage-deflate offer or response.
type deflateElement struct {
	serverNoContextTakeover bool
	clientNoContextTakeover bool
	serverMaxWindowBits     int
	clientMaxWindowBits     int
}

// answers reports whether response r is a valid acceptance of offer o.
func (r deflateElement) answers(o deflateElement) bool {
	if o.serverNoContextTakeover && !r.serverNoContextTakeover {
		return false
	}
	if o.serverMaxWindowBits != windowBitsAbsent &&
		(r.serverMaxWindowBits == windowBitsAbsent || r.serverMaxWindowBits > o.serverMaxWindowBits) {
		return false
	}
	if r.clientMaxWindowBits != windowBitsAbsent {
		if o.clientMaxWindowBits == windowBitsAbsent {
			return false
		}
		if o.clientMaxWindowBits != windowBitsNoValue && r.clientMaxWindowBits > o.clientMaxWindowBits {
			return false
		}
	}
	return true
}

// params returns the negotiated configuration of an accepted response.
func (r deflateElement) params() *domain.WebSocketDeflate {
	params := &domain.WebSocketDeflate{
		ServerNoContextTakeover: r.serverNoContextTakeover,
		ClientNoContextTakeover: r.clientNoContextTakeover,
		ServerMaxWindowBits:     15,
		ClientMaxWindowBits:     15,
	}
	if r.serverMaxWindowBits != windowBitsAbsent {
		params.ServerMaxWindowBits = r.serverMaxWindowBits
	}
	if r.clientMaxWindowBits != windowBitsAbsent {
		params.ClientMaxWindowBits = r.clientMaxWindowBits
	}
	return params
}

// parseDeflateElement parses one permessage-deflate extension element.
// client_max_window_bits may have no value; the caller decides if that is valid.
func parseDeflateElement(element string) (deflateElement, error) {
	parts := splitOutsideQuotes(element, ';')
	if name := strings.TrimSpace(parts[0]); !strings.EqualFold(name, deflateExtension) {
		return deflateElement{}, fmt.Errorf("extension %q is not %s", name, deflateExtension)
	}

	var parsed deflateElement
	seen := make(map[string]bool)
	for _, part := range parts[1:] {
		name, value, hasValue := strings.Cut(part, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		value = unquote(strings.TrimSpace(value))
		if seen[name] {
			return deflateElement{}, fmt.Errorf("duplicate parameter %q", name)
		}
		seen[name] = true

		switch name {
		case "server_no_context_takeover", "client_no_context_takeover":
			if hasValue {
				return deflateElement{}, fmt.Errorf("parameter %q takes no value", name)
			}
			if name == "server_no_context_takeover" {
				parsed.serverNoContextTakeover = true
			} else {
				parsed.clientNoContextTakeover = true
			}
		case "server_max_window_bits":
			if !hasValue {
				return deflateElement{}, fmt.Errorf("parameter %q needs a value", name)
			}
			bits, err := parseWindowBits(value)
			if err != nil {
				return deflateElement{}, fmt.Errorf("parameter %q: %w", name, err)
			}
			parsed.serverMaxWindowBits = bits
		case "client_max_window_bits":
			parsed.clientMaxWindowBits = windowBitsNoValue
			if hasValue {
				bits, err := parseWindowBits(value)
				if err != nil {
					return deflateElement{}, fmt.Errorf("parameter %q: %w", name, err)
				}
				parsed.clientMaxWindowBits = bits
			}
		default:
			return deflateElement{}, fmt.Errorf("unknown parameter %q", name)
		}
	}
	return parsed, nil
}

// parseWindowBits parses a window size of 8 to 15 with no leading zero.
func parseWindowBits(value string) (int, error) {
	bits, err := strconv.Atoi(value)
	if err != nil || bits < 8 || bits > 15 || strconv.Itoa(bits) != value {
		return 0, fmt.Errorf("window bits %q are not 8 to 15", value)
	}
	return bits, nil
}

// unquote returns the content of a quoted-string, or value unchanged.
func unquote(value string) string {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return value
	}
	var unquoted strings.Builder
	for i := 1; i < len(value)-1; i++ {
		if value[i] == '\\' && i+1 < len(value)-1 {
			i++
		}
		unquoted.WriteByte(value[i])
	}
	return unquoted.String()
}

// extensionName returns the extension token of one extension list element.
func extensionName(element string) string {
	name, _, _ := strings.Cut(element, ";")
	return strings.TrimSpace(name)
}

// splitOutsideQuotes splits s on sep, ignoring separators inside quoted strings.
func splitOutsideQuotes(s string, sep byte) []string {
	var parts []string
	start := 0
	quoted := false
	for i := 0; i < len(s); i++ {
		switch {
		case quoted && s[i] == '\\':
			i++
		case s[i] == '"':
			quoted = !quoted
		case !quoted && s[i] == sep:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}
