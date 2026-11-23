package armory

import (
	"fmt"
	"strings"

	"github.com/tfkr-ae/marasi/stream"
)

// Quoting states of a message-form body, read byte by byte.
const (
	// outsideString is any place outside a string literal: between
	// fields, in a number, a comment or a hex literal.
	outsideString = 0
	// insideEscape is right after a backslash in a string literal.
	insideEscape = -1
)

// quotedPositions reports, for each position, whether it sits inside a
// quoted string in a message-form body whose messages are protoscope text.
// literal is the template's text with every position at its fallback. A
// position that opens and closes in different places, such as inside a
// string and outside it, is refused with its number, line and column.
func quotedPositions(raw, literal string, framing stream.Framing, positions []markedPosition) ([]bool, error) {
	quoted := make([]bool, len(positions))
	if framing == stream.FramingNone || len(positions) == 0 {
		return quoted, nil
	}
	head, blank, _ := splitHead(literal)
	if blank == "" {
		return quoted, nil
	}
	if contentType, _ := headField(head, "content-type"); !stream.HasProtoscopeText(messageContentType(contentType)) {
		return quoted, nil
	}
	states := stringStates(literal, len(head)+len(blank))
	for number, position := range positions {
		opens, closes := states[position.start], states[position.end]
		if opens != closes || opens == insideEscape {
			line := strings.Count(raw[:position.marker], "\n") + 1
			column := position.marker - strings.LastIndex(raw[:position.marker], "\n")
			return nil, fmt.Errorf("payload position %d at line %d column %d must open and close inside the same quoted string, or both outside one", number, line, column)
		}
		quoted[number] = opens != outsideString
	}
	return quoted, nil
}

// stringStates is, for each offset of literal, the state a value put in
// there would be read in: outsideString, insideEscape, or the number of the
// string literal it is inside. Offsets before bodyStart are outside, and
// each separator line starts a new message outside any string.
func stringStates(literal string, bodyStart int) []int {
	states := make([]int, len(literal)+1)
	state, opened := outsideString, 0
	comment, hexLiteral := false, false
	for index := bodyStart; index < len(literal); index++ {
		if index == bodyStart || literal[index-1] == '\n' {
			line, _, _ := strings.Cut(literal[index:], "\n")
			if isSeparator, _ := separatorLine(line); isSeparator {
				state, comment, hexLiteral = outsideString, false, false
			}
		}
		states[index] = state
		c := literal[index]
		switch {
		case state == insideEscape:
			state = opened
		case state != outsideString:
			switch c {
			case '\\':
				state = insideEscape
			case '"':
				state = outsideString
			}
		case comment:
			comment = c != '\n'
		case hexLiteral:
			hexLiteral = c != '`'
		case c == '#':
			comment = true
		case c == '`':
			hexLiteral = true
		case c == '"':
			opened++
			state = opened
		}
	}
	states[len(literal)] = state
	return states
}
