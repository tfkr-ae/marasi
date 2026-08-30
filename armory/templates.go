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

// preprocessTemplate converts Armory payload markers into Go template actions.
func preprocessTemplate(raw string) (string, int, error) {
	var expanded strings.Builder
	var fallback strings.Builder
	functions := templateFunctions()

	position := 0
	inPosition := false

	writeByte := func(value byte) {
		if inPosition {
			fallback.WriteByte(value)
			return
		}

		expanded.WriteByte(value)
	}

	for index := 0; index < len(raw); {
		if raw[index] == '\\' && index+1 < len(raw) {
			next := raw[index+1]
			if next == '\\' || next == '@' {
				writeByte(next)
				index += 2
				continue
			}
		}

		if strings.HasPrefix(raw[index:], payloadMarker) {
			index += len(payloadMarker)

			if !inPosition {
				fallback.Reset()
				inPosition = true
				continue
			}
			chain, nextIndex, err := functionChain(raw, index, functions)
			if err != nil {
				return "", 0, err
			}

			expanded.WriteString("{{.Payload ")
			expanded.WriteString(strconv.Itoa(position))
			expanded.WriteByte(' ')
			expanded.WriteString(strconv.Quote(fallback.String()))
			for _, name := range chain {
				expanded.WriteString(" | ")
				expanded.WriteString(name)
			}
			expanded.WriteString("}}")

			position++
			inPosition = false
			index = nextIndex
			continue
		}

		writeByte(raw[index])
		index++
	}

	if inPosition {
		return "", 0, fmt.Errorf(
			"payload position %d is not closed",
			position,
		)
	}

	return expanded.String(), position, nil
}

// parseTemplate preprocesses and parses an Armory request template.
func parseTemplate(raw string) (*armoryTemplate, error) {
	expanded, _, err := preprocessTemplate(raw)
	if err != nil {
		return nil, err
	}

	parsed, err := template.New("request").
		Option("missingkey=error").
		Funcs(templateFunctions()).
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

	return output.String(), nil
}
