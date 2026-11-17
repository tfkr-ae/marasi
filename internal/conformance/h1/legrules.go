package h1

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// Leg rules: what Marasi itself must send on each leg, checked on the
// messages it sent rather than against the direct baseline. See
// docs/adr/2026-11-16-the-proxy-follows-hop-by-hop-rules-but-sends-no-via.md.
var (
	RuleConnection = conformance.LegRule{ID: "leg:connection", RFC: "RFC 9110 §7.6.1", Level: conformance.LevelMust,
		Text: "remove Connection and the fields it names"}
	RuleFraming = conformance.LegRule{ID: "leg:framing", RFC: "RFC 9112 §6", Level: conformance.LevelMust,
		Text: "framing is valid and matches the body"}
	RuleLengthAndEncoding = conformance.LegRule{ID: "leg:cl-and-te", RFC: "RFC 9112 §6.1", Level: conformance.LevelMust,
		Text: "never send both Content-Length and Transfer-Encoding"}
	RuleFieldOrder = conformance.LegRule{ID: "leg:field-order", RFC: "RFC 9110 §5.3", Level: conformance.LevelMust,
		Text: "keep the order of same-name field lines"}
	RuleVia = conformance.LegRule{ID: "leg:via", RFC: "RFC 9110 §7.6.3", Level: conformance.LevelMust,
		Text: "send Via"}
)

// LegRules lists every h1 leg rule in report order.
var LegRules = []conformance.LegRule{RuleConnection, RuleFraming, RuleLengthAndEncoding, RuleFieldOrder, RuleVia}

// ownOptions are connection options a proxy may send for itself with their
// fields, so a message's own Connection naming them is no breach.
var ownOptions = []string{"close", "keep-alive", "te", "upgrade"}

// Forwarded is one message Marasi sent on a leg, with the message it was
// forwarding when the recording holds it.
type Forwarded struct {
	Path     string   // the message's field path, such as origin[0].requests[0]
	Sent     Message  // what Marasi sent
	Received *Message // what Marasi received and forwarded; nil when unknown
}

// Forwards returns every message Marasi sent in a proxied recording: the
// requests the origin received, each paired with the request the client
// sent in the same place, and the responses the client received, each
// paired with the response the origin sent. Messages are paired by their
// order across all connections on a side.
func Forwards(recording Recording) []Forwarded {
	var forwards []Forwarded
	pair := func(side string, sent []Connection, received []Connection, messages func(Connection) []Message) {
		var from []Message
		for _, c := range received {
			from = append(from, messages(c)...)
		}
		n := 0
		for i, c := range sent {
			for j, message := range messages(c) {
				f := Forwarded{Path: fmt.Sprintf("%s[%d].%s[%d]", side, i, kind(side), j), Sent: message}
				if n < len(from) {
					f.Received = &from[n]
				}
				forwards = append(forwards, f)
				n++
			}
		}
	}
	pair("origin", recording.Origin, recording.Client, func(c Connection) []Message { return c.Requests })
	pair("client", recording.Client, recording.Origin, func(c Connection) []Message { return c.Responses })
	return forwards
}

func kind(side string) string {
	if side == "origin" {
		return "requests"
	}
	return "responses"
}

// CheckRecording checks every leg rule on a proxied recording: on the
// origin leg the requests the origin fixture captured, and on the client
// leg the responses the client captured.
func CheckRecording(caseID string, recording Recording) ([]conformance.Difference, []conformance.LegCheck) {
	return CheckLeg(caseID, Forwards(recording))
}

// Check is CheckRecording on a recording as JSON, for conformance.CompareCases.
func Check(caseID string, got json.RawMessage) ([]conformance.Difference, []conformance.LegCheck, error) {
	var recording Recording
	if err := json.Unmarshal(got, &recording); err != nil {
		return nil, nil, fmt.Errorf("decoding recording: %w", err)
	}
	differences, checks := CheckRecording(caseID, recording)
	return differences, checks, nil
}

// CheckLeg checks every leg rule on messages Marasi sent, and returns one
// check per rule it could check and one difference per broken rule, with
// the rule's ID as the field and what was sent in Got. A rule that needs
// the received message, such as field order, is checked only on the
// messages that have it.
func CheckLeg(caseID string, forwards []Forwarded) ([]conformance.Difference, []conformance.LegCheck) {
	var differences []conformance.Difference
	var checks []conformance.LegCheck
	for _, rule := range LegRules {
		checked := false
		var breaches []string
		for _, f := range forwards {
			if rule != RuleFraming && !hasHead(f.Sent) {
				// Bytes that never made a whole head are only a framing breach.
				continue
			}
			breach, ok := check(rule, f)
			checked = checked || ok
			if breach != "" {
				breaches = append(breaches, f.Path+": "+breach)
			}
		}
		if !checked {
			continue
		}
		checks = append(checks, conformance.LegCheck{Case: caseID, Rule: rule, Broken: len(breaches) > 0})
		if len(breaches) > 0 {
			differences = append(differences, conformance.Difference{Case: caseID, Field: rule.ID,
				Want: rule.Text, Got: strings.Join(breaches, "; "), Level: rule.Level})
		}
	}
	return differences, checks
}

// hasHead reports whether a message's start line and fields arrived whole.
func hasHead(m Message) bool {
	return m.Framing.Kind != ""
}

// check checks one rule on one message. It returns the breach, empty when
// there is none, and whether the rule could be checked at all.
func check(rule conformance.LegRule, f Forwarded) (string, bool) {
	switch rule {
	case RuleConnection:
		return connectionBreach(f), true
	case RuleFraming:
		return framingBreach(f), true
	case RuleLengthAndEncoding:
		if len(f.Sent.Values("Content-Length")) > 0 && len(f.Sent.Values("Transfer-Encoding")) > 0 {
			return "Content-Length and Transfer-Encoding", true
		}
		return "", true
	case RuleFieldOrder:
		if f.Received == nil {
			return "", false
		}
		return orderBreach(f.Sent, *f.Received), true
	case RuleVia:
		if len(f.Sent.Values("Via")) == 0 {
			return "no Via", true
		}
		return "", true
	}
	return "", false
}

// connectionBreach lists the fields Marasi sent that the received
// message's Connection named. With no received message, it uses the sent
// message's own Connection, less the options a proxy may set for itself.
func connectionBreach(f Forwarded) string {
	var named []string
	if f.Received != nil {
		named = connectionOptions(*f.Received)
	} else {
		named = slices.DeleteFunc(connectionOptions(f.Sent), func(name string) bool {
			return slices.Contains(ownOptions, name)
		})
	}
	var sent []string
	for _, field := range slices.Concat(f.Sent.Fields, f.Sent.Trailers) {
		name := strings.ToLower(strings.TrimSpace(field.Name))
		if name != "connection" && slices.Contains(named, name) {
			sent = append(sent, field.Name+":"+field.Value)
		}
	}
	return strings.Join(sent, ", ")
}

func connectionOptions(m Message) []string {
	var names []string
	for _, value := range m.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// framingBreach reports framing that is invalid or does not delimit the
// body: a message cut short that was whole when received, an invalid
// Content-Length, or a request whose last transfer coding is not chunked.
func framingBreach(f Forwarded) string {
	sent := f.Sent
	if !hasHead(sent) {
		return fmt.Sprintf("bytes that are not a message: %q", sent.StartLine)
	}
	if sent.Truncated && (f.Received == nil || !f.Received.Truncated) {
		return fmt.Sprintf("message cut short after %d body bytes", len(sent.Body))
	}
	if codings := sent.Values("Transfer-Encoding"); len(codings) > 0 {
		all := strings.Split(strings.Join(codings, ","), ",")
		last := strings.TrimSpace(all[len(all)-1])
		if sent.Status() == 0 && !strings.EqualFold(last, "chunked") {
			return "Transfer-Encoding: " + strings.Join(codings, ",") + " does not end in chunked"
		}
		return ""
	}
	if values := sent.Values("Content-Length"); len(values) > 0 {
		if _, ok := contentLength(sent); !ok {
			return "Content-Length:" + strings.Join(values, ",")
		}
	}
	return ""
}

// orderBreach compares the values of each field name sent on several
// lines with the received ones. Lines may be combined or split on commas;
// only the order of the values both sides have counts.
func orderBreach(sent, received Message) string {
	values := func(m Message, name string) []string {
		var out []string
		for _, value := range m.Values(name) {
			for _, v := range strings.Split(value, ",") {
				out = append(out, strings.TrimSpace(v))
			}
		}
		return out
	}
	var breaches []string
	seen := map[string]bool{}
	for _, field := range received.Fields {
		name := strings.ToLower(strings.TrimSpace(field.Name))
		if seen[name] || len(received.Values(name)) < 2 {
			continue
		}
		seen[name] = true
		want, got := values(received, name), values(sent, name)
		common := func(a, b []string) []string {
			rest := slices.Clone(b)
			var out []string
			for _, v := range a {
				if i := slices.Index(rest, v); i >= 0 {
					out = append(out, v)
					rest = slices.Delete(rest, i, i+1)
				}
			}
			return out
		}
		if w, g := common(want, got), common(got, want); !slices.Equal(w, g) {
			breaches = append(breaches, fmt.Sprintf("%s: %s, received %s", field.Name, strings.Join(g, ", "), strings.Join(w, ", ")))
		}
	}
	return strings.Join(breaches, "; ")
}
