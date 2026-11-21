package protoscope

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrSyntax means the text is not in the text form. The error wrapping it
// names the line and column.
var ErrSyntax = errors.New("text form syntax error")

// Encode is the bytes text describes. It accepts what Decode writes and
// the parts of the protoscope language a tester edits it with:
//
//   - a tag, N:VARINT, N:I64, N:LEN, N:SGROUP, N:EGROUP or N:I32;
//   - a number, as a varint: decimal, negative, or 0x hex, or true or false;
//   - a number with an i32 or i64 suffix, little-endian in that width, which
//     may be a float such as 1.5i32 (a bare float is i64);
//   - a "string" with \n, \t, \r, \", \\ and \xNN escapes, as its bytes;
//   - a `hex` literal, as its bytes;
//   - { ... }, its contents prefixed with their length, nested no deeper
//     than maxDepth;
//   - long-form:N before a tag, a varint or {, which pads that varint N
//     bytes past its minimal length;
//   - # to the end of the line, a comment.
func Encode(text string) ([]byte, error) {
	e := &encoder{text: text, line: 1, col: 1}
	out, err := e.sequence(false)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// encoder reads the text form left to right.
type encoder struct {
	text      string
	pos       int
	line, col int
	// depth is how many braces are open.
	depth int
}

func (e *encoder) errorf(format string, args ...any) error {
	return fmt.Errorf("line %d column %d: %s: %w", e.line, e.col, fmt.Sprintf(format, args...), ErrSyntax)
}

// advance moves past n bytes, keeping the line and column.
func (e *encoder) advance(n int) {
	for _, c := range []byte(e.text[e.pos : e.pos+n]) {
		if c == '\n' {
			e.line, e.col = e.line+1, 1
		} else {
			e.col++
		}
	}
	e.pos += n
}

// skipSpace moves past whitespace and comments.
func (e *encoder) skipSpace() {
	for e.pos < len(e.text) {
		switch c := e.text[e.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			e.advance(1)
		case c == '#':
			end := strings.IndexByte(e.text[e.pos:], '\n')
			if end < 0 {
				end = len(e.text) - e.pos
			}
			e.advance(end)
		default:
			return
		}
	}
}

// sequence encodes items until the end of the text, or until the closing
// brace when nested is true.
func (e *encoder) sequence(nested bool) ([]byte, error) {
	var out []byte
	longForm := -1
	for {
		e.skipSpace()
		if e.pos == len(e.text) {
			if nested {
				return nil, e.errorf("missing }")
			}
			break
		}
		c := e.text[e.pos]
		if c == '}' {
			if !nested {
				return nil, e.errorf("unmatched }")
			}
			e.advance(1)
			break
		}
		extra := max(longForm, 0)
		var err error
		switch c {
		case '{':
			// Each brace recurses, so a bound keeps a text of many
			// braces from exhausting the stack.
			if e.depth == maxDepth {
				return nil, e.errorf("braces nested past %d levels", maxDepth)
			}
			e.advance(1)
			e.depth++
			var contents []byte
			contents, err = e.sequence(true)
			if err != nil {
				return nil, err
			}
			e.depth--
			out, err = e.appendVarint(out, uint64(len(contents)), extra)
			out = append(out, contents...)
		case '"':
			if longForm >= 0 {
				return nil, e.errorf("long-form before a string")
			}
			out, err = e.appendString(out)
		case '`':
			if longForm >= 0 {
				return nil, e.errorf("long-form before a hex literal")
			}
			out, err = e.appendHex(out)
		default:
			word := e.word()
			if word == "" {
				return nil, e.errorf("unexpected %q", c)
			}
			if n, ok := strings.CutPrefix(word, "long-form:"); ok {
				if longForm >= 0 {
					return nil, e.errorf("long-form twice")
				}
				longForm, err = strconv.Atoi(n)
				if err != nil || longForm < 0 {
					return nil, e.errorf("bad long-form %q", word)
				}
				e.advance(len(word))
				continue
			}
			out, err = e.appendWord(out, word, longForm)
			if err == nil {
				e.advance(len(word))
			}
		}
		if err != nil {
			return nil, err
		}
		longForm = -1
	}
	if longForm >= 0 {
		return nil, e.errorf("long-form at the end")
	}
	return out, nil
}

// word is the bare token at the current position.
func (e *encoder) word() string {
	end := e.pos
	for end < len(e.text) && !strings.ContainsRune(" \t\r\n{}\"`#", rune(e.text[end])) {
		end++
	}
	return e.text[e.pos:end]
}

// appendWord encodes a tag, a varint or a fixed-width number. longForm is
// -1 when no long-form came before it.
func (e *encoder) appendWord(out []byte, word string, longForm int) ([]byte, error) {
	extra := max(longForm, 0)
	if number, wire, ok := strings.Cut(word, ":"); ok {
		w := -1
		for i, name := range wireNames {
			if wire == name {
				w = i
			}
		}
		n, err := strconv.ParseUint(number, 10, 64)
		if w < 0 || err != nil || n == 0 || n > math.MaxUint64>>3 {
			return nil, e.errorf("bad tag %q", word)
		}
		return e.appendVarint(out, n<<3|uint64(w), extra)
	}
	switch word {
	case "true":
		return e.appendVarint(out, 1, extra)
	case "false":
		return e.appendVarint(out, 0, extra)
	}
	number, width := word, 0
	if n, ok := strings.CutSuffix(word, "i32"); ok {
		number, width = n, 4
	} else if n, ok := strings.CutSuffix(word, "i64"); ok {
		number, width = n, 8
	}
	if width != 0 && longForm >= 0 {
		return nil, e.errorf("long-form before a fixed-width number")
	}
	if v, ok := parseInteger(number); ok {
		switch width {
		case 4:
			if v > math.MaxUint32 && (int64(v) >= 0 || int64(v) < math.MinInt32) {
				return nil, e.errorf("%q does not fit in 32 bits", word)
			}
			return binary.LittleEndian.AppendUint32(out, uint32(v)), nil
		case 8:
			return binary.LittleEndian.AppendUint64(out, v), nil
		}
		return e.appendVarint(out, v, extra)
	}
	f, err := strconv.ParseFloat(number, 64)
	if err != nil || longForm >= 0 {
		return nil, e.errorf("bad number %q", word)
	}
	if width == 4 {
		return binary.LittleEndian.AppendUint32(out, math.Float32bits(float32(f))), nil
	}
	return binary.LittleEndian.AppendUint64(out, math.Float64bits(f)), nil
}

// parseInteger reads a decimal or 0x hex integer, negative as its int64
// two's complement.
func parseInteger(s string) (uint64, bool) {
	negative := strings.HasPrefix(s, "-")
	digits := strings.TrimPrefix(s, "-")
	base := 10
	if h, ok := strings.CutPrefix(digits, "0x"); ok {
		digits, base = h, 16
	}
	v, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return 0, false
	}
	if negative {
		if v > 1<<63 {
			return 0, false
		}
		return -v, true
	}
	return v, true
}

// appendVarint appends v as a varint padded extra bytes past its minimal
// length.
func (e *encoder) appendVarint(out []byte, v uint64, extra int) ([]byte, error) {
	length := varintLen(v) + extra
	if length > maxVarintLen {
		return nil, e.errorf("varint of %d bytes is past %d", length, maxVarintLen)
	}
	for i := range length {
		c := byte(v & 0x7f)
		v >>= 7
		if i < length-1 {
			c |= 0x80
		}
		out = append(out, c)
	}
	return out, nil
}

// appendString appends the bytes of the string literal at the current
// position.
func (e *encoder) appendString(out []byte) ([]byte, error) {
	i := e.pos + 1
	for i < len(e.text) {
		c := e.text[i]
		switch c {
		case '"':
			e.advance(i + 1 - e.pos)
			return out, nil
		case '\\':
			if i+1 == len(e.text) {
				return nil, e.errorf("unterminated string")
			}
			switch esc := e.text[i+1]; esc {
			case 'n':
				out = append(out, '\n')
			case 't':
				out = append(out, '\t')
			case 'r':
				out = append(out, '\r')
			case '"', '\\', '\'':
				out = append(out, esc)
			case 'x':
				if i+4 > len(e.text) {
					return nil, e.errorf("bad \\x escape")
				}
				b, err := hex.DecodeString(e.text[i+2 : i+4])
				if err != nil {
					return nil, e.errorf("bad \\x escape")
				}
				out = append(out, b...)
				i += 2
			default:
				return nil, e.errorf("bad escape \\%c", esc)
			}
			i += 2
		default:
			out = append(out, c)
			i++
		}
	}
	return nil, e.errorf("unterminated string")
}

// appendHex appends the bytes of the hex literal at the current position.
func (e *encoder) appendHex(out []byte) ([]byte, error) {
	end := strings.IndexByte(e.text[e.pos+1:], '`')
	if end < 0 {
		return nil, e.errorf("unterminated hex literal")
	}
	digits := strings.Join(strings.Fields(e.text[e.pos+1:e.pos+1+end]), "")
	b, err := hex.DecodeString(digits)
	if err != nil {
		return nil, e.errorf("bad hex literal")
	}
	e.advance(end + 2)
	return append(out, b...), nil
}
