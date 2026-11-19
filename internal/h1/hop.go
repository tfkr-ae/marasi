package h1

import (
	"slices"
	"strings"
)

// RemoveConnectionOptions removes Connection and every field it names, as a
// proxy must before forwarding a message (RFC 9110 §7.6.1). With
// keepUpgrade, an upgrade the message asks for survives: the Upgrade field
// stays, and Connection keeps only its upgrade option, so a protocol switch
// the proxy carries (RFC 9110 §7.8) still reaches the next hop. A Connection
// line that names nothing but upgrade keeps its bytes.
func (h *Header) RemoveConnectionOptions(keepUpgrade bool) {
	var named []string
	upgrade := ""
	for _, f := range h.Fields {
		if !f.named("Connection") {
			continue
		}
		for option := range strings.SplitSeq(f.Value(), ",") {
			option = strings.TrimSpace(option)
			if option == "" {
				continue
			}
			if keepUpgrade && strings.EqualFold(option, "upgrade") {
				upgrade = option
				continue
			}
			named = append(named, strings.ToLower(option))
		}
	}
	if upgrade != "" && !h.Has("Upgrade") {
		named = append(named, "upgrade")
		upgrade = ""
	}
	fields := make([]Field, 0, len(h.Fields))
	keptConnection := false
	for _, f := range h.Fields {
		switch {
		case f.named("Connection"):
			if upgrade == "" || keptConnection {
				continue
			}
			keptConnection = true
			if !strings.EqualFold(f.Value(), "upgrade") {
				lineEnd := string(eolOf(f.Raw))
				if lineEnd == "" {
					lineEnd = h.eol()
				}
				f = Field{Raw: []byte(f.Name() + ": " + upgrade + lineEnd)}
			}
		case len(named) > 0 && slices.ContainsFunc(named, f.named):
			continue
		}
		fields = append(fields, f)
	}
	if len(fields) != len(h.Fields) || keptConnection {
		h.Fields = fields
	}
}
