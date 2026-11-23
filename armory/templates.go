package armory

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/template"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/protoscope"
)

// payloadMarker delimits fallback values replaced during an Armory run.
const payloadMarker = "@@"

// templateCtx contains the payload values for one rendered request.
type templateCtx struct {
	// payloads maps zero-based template positions to their current values.
	payloads map[int]string
}

// armoryTemplate is a parsed Armory request template.
type armoryTemplate struct {
	// template is the compiled Go template reused for each rendered request.
	template *template.Template
	// positionCount is the number of payload positions in the template.
	positionCount int
	// framing is the request framing of a template with a message-form
	// body, empty for any other template.
	framing string
}

// Payload returns the value assigned to a position or its original fallback.
func (ctx templateCtx) Payload(position int, fallback string) (string, error) {
	if position < 0 {
		return "", errors.New("payload position cannot be negative")
	}

	value, exists := ctx.payloads[position]
	if !exists {
		return fallback, nil
	}
	return value, nil
}

// rawFunction names the position function that, last in a chain, puts a
// payload inside a quoted protoscope string in as written.
const rawFunction = "raw"

// protoscopeStringFunction names the step added to a position inside a
// quoted protoscope string. A marker's chain cannot name it.
const protoscopeStringFunction = "protoscopeString"

// protoscopeString is value escaped for the inside of a protoscope string
// literal, with the quoting the decoder writes.
func protoscopeString(value string) string {
	quoted := protoscope.Quote([]byte(value))
	return quoted[1 : len(quoted)-1]
}

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"upper":      strings.ToUpper,
		"lower":      strings.ToLower,
		"reverse":    reverseString,
		"trim":       strings.TrimSpace,
		"base64":     func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) },
		"hex":        func(value string) string { return hex.EncodeToString([]byte(value)) },
		"sha256":     func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) },
		"urlencode":  url.QueryEscape,
		"pathescape": url.PathEscape,
		"replace":    func(old, new, value string) string { return strings.ReplaceAll(value, old, new) },
		rawFunction:  func(value string) string { return value },
		"uuid": func() (string, error) {
			id, err := uuid.NewV7()
			return id.String(), err
		},
	}
}

func reverseString(value string) string {
	runes := []rune(value)
	for left, right := 0, len(runes)-1; left < right; left, right = left+1, right-1 {
		runes[left], runes[right] = runes[right], runes[left]
	}
	return string(runes)
}

func functionChain(raw string, index int, functions template.FuncMap) ([]string, int, error) {
	var chain []string
	for index < len(raw) && raw[index] == '.' {
		nameStart := index + 1
		nameEnd := nameStart
		for nameEnd < len(raw) && ((raw[nameEnd] >= 'a' && raw[nameEnd] <= 'z') || (raw[nameEnd] >= 'A' && raw[nameEnd] <= 'Z') || (raw[nameEnd] >= '0' && raw[nameEnd] <= '9') || raw[nameEnd] == '_') {
			nameEnd++
		}
		if nameEnd == nameStart || nameEnd >= len(raw) || raw[nameEnd] != '(' {
			break
		}

		name := raw[nameStart:nameEnd]
		if nameEnd+1 >= len(raw) || raw[nameEnd+1] != ')' {
			return nil, index, fmt.Errorf("armory function %s must be called without arguments", name)
		}
		function, exists := functions[name]
		if !exists {
			return nil, index, fmt.Errorf("armory function %s is not defined", name)
		}
		if _, valid := function.(func(string) string); !valid {
			return nil, index, fmt.Errorf("armory function %s cannot be chained", name)
		}

		chain = append(chain, name)
		index = nameEnd + 2
	}
	return chain, index, nil
}

// markedPosition is one payload position written with @@ markers.
type markedPosition struct {
	// fallback is the text between the markers.
	fallback string
	// chain is the functions named after the closing marker, in order.
	chain []string
	// marker is the offset in the raw template of the opening marker.
	marker int
	// start and end bound the fallback in the template's literal text.
	start, end int
}

// preprocessTemplate converts Armory payload markers into Go template actions.
func preprocessTemplate(raw string) (string, int, error) {
	return preprocessFramedTemplate(raw, "")
}

// preprocessFramedTemplate converts Armory payload markers into Go template
// actions. When framing is set and the body's messages are protoscope text,
// a position inside a quoted string has its value escaped as a protoscope
// string after its function chain, unless the chain ends in raw.
func preprocessFramedTemplate(raw, framing string) (string, int, error) {
	// literal is the template's text with escapes read and every position
	// at its fallback, the text the quoting is read from.
	var literal strings.Builder
	var positions []markedPosition
	functions := templateFunctions()

	inPosition := false
	for index := 0; index < len(raw); {
		if raw[index] == '\\' && index+1 < len(raw) {
			next := raw[index+1]
			if next == '\\' || next == '@' {
				literal.WriteByte(next)
				index += 2
				continue
			}
		}

		if strings.HasPrefix(raw[index:], payloadMarker) {
			if !inPosition {
				positions = append(positions, markedPosition{marker: index, start: literal.Len()})
				inPosition = true
				index += len(payloadMarker)
				continue
			}
			index += len(payloadMarker)
			chain, nextIndex, err := functionChain(raw, index, functions)
			if err != nil {
				return "", 0, err
			}
			position := &positions[len(positions)-1]
			position.end = literal.Len()
			position.fallback = literal.String()[position.start:position.end]
			position.chain = chain
			inPosition = false
			index = nextIndex
			continue
		}

		literal.WriteByte(raw[index])
		index++
	}

	if inPosition {
		return "", 0, fmt.Errorf(
			"payload position %d is not closed",
			len(positions)-1,
		)
	}

	text := literal.String()
	quoted, err := quotedPositions(raw, text, framing, positions)
	if err != nil {
		return "", 0, err
	}

	var expanded strings.Builder
	previous := 0
	for number, position := range positions {
		expanded.WriteString(text[previous:position.start])
		previous = position.end

		fallback := position.fallback
		escape := quoted[number] && (len(position.chain) == 0 || position.chain[len(position.chain)-1] != rawFunction)
		if escape {
			// The fallback is written as protoscope string text; the action
			// takes its bytes, so escaping it again gives the same message.
			value, err := protoscope.Encode(`"` + fallback + `"`)
			if err != nil {
				return "", 0, fmt.Errorf("payload position %d: %w", number, err)
			}
			fallback = string(value)
		}

		expanded.WriteString("{{.Payload ")
		expanded.WriteString(strconv.Itoa(number))
		expanded.WriteByte(' ')
		expanded.WriteString(strconv.Quote(fallback))
		for _, name := range position.chain {
			expanded.WriteString(" | ")
			expanded.WriteString(name)
		}
		if escape {
			expanded.WriteString(" | ")
			expanded.WriteString(protoscopeStringFunction)
		}
		expanded.WriteString("}}")
	}
	expanded.WriteString(text[previous:])

	return expanded.String(), len(positions), nil
}

// parseFramedTemplate parses an Armory request template whose body is in
// message form when framing is set.
func parseFramedTemplate(raw, framing string) (*armoryTemplate, error) {
	if err := supportedFraming(framing); err != nil {
		return nil, err
	}
	expanded, _, err := preprocessFramedTemplate(raw, framing)
	if err != nil {
		return nil, err
	}
	tmpl, err := parseExpanded(expanded)
	if err != nil {
		return nil, err
	}
	tmpl.framing = framing
	return tmpl, nil
}

// parseTemplate preprocesses and parses an Armory request template.
func parseTemplate(raw string) (*armoryTemplate, error) {
	expanded, _, err := preprocessTemplate(raw)
	if err != nil {
		return nil, err
	}
	return parseExpanded(expanded)
}

// parseExpanded parses a preprocessed Armory request template.
func parseExpanded(expanded string) (*armoryTemplate, error) {
	functions := templateFunctions()
	functions[protoscopeStringFunction] = protoscopeString
	parsed, err := template.New("request").
		Option("missingkey=error").
		Funcs(functions).
		Parse(expanded)
	if err != nil {
		return nil, fmt.Errorf("parsing armory template: %w", err)
	}
	positionCount, err := inspectPayloadPositions(parsed)
	if err != nil {
		return nil, fmt.Errorf("inspecting armory template: %w", err)
	}

	return &armoryTemplate{
		template:      parsed,
		positionCount: positionCount,
	}, nil
}

// render executes the template with the provided payload values.
func (tmpl *armoryTemplate) render(payloads map[int]string) (string, error) {
	var output strings.Builder

	err := tmpl.template.Execute(&output, templateCtx{
		payloads: payloads,
	})
	if err != nil {
		return "", fmt.Errorf("rendering armory template: %w", err)
	}
	if tmpl.framing == "" {
		return output.String(), nil
	}
	raw, err := encodeMessageForm(output.String(), tmpl.framing)
	if err != nil {
		return "", &messageError{err: err}
	}
	return raw, nil
}

// messageError means the rendered request's messages could not be built
// for its framing. During a run, that request is a failed attempt: it is
// recorded and not sent, and the run goes on.
type messageError struct {
	err error
}

func (err *messageError) Error() string { return err.err.Error() }

func (err *messageError) Unwrap() error { return err.err }

// EscapeTemplate escapes raw text so that, as an Armory template, it renders
// back to exactly raw: `\` becomes `\\` and `@` becomes `\@`, the two bytes
// the preprocessor reads specially. The result has no payload positions.
func EscapeTemplate(raw string) string {
	var escaped strings.Builder
	escaped.Grow(len(raw))
	for index := 0; index < len(raw); index++ {
		if raw[index] == '\\' || raw[index] == '@' {
			escaped.WriteByte('\\')
		}
		escaped.WriteByte(raw[index])
	}
	return escaped.String()
}

// Render renders an Armory template with every payload position at its
// fallback value, as the request it sends: a template whose framing is set
// has its message-form body encoded into envelopes.
func Render(raw, framing string) (string, error) {
	tmpl, err := parseFramedTemplate(raw, framing)
	if err != nil {
		return "", err
	}
	return tmpl.render(nil)
}
