// Package protoscope is Marasi's codec between protobuf's wire format and a
// text form compatible with Google's protoscope language, without a schema.
// Decode never fails: bytes that are not a protobuf message come back as a
// hex literal. Encode turns the text form back into bytes, and encoding the
// text Decode gave is the identity: the text states every field's wire type
// and keeps non-minimal varints, field order and split repeated fields.
//
// See docs/adr/2026-11-20-protobuf-is-decoded-for-display-and-re-encoded-only-when-edited.md.
package protoscope

import (
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Wire types, as protobuf numbers them.
const (
	wireVarint = 0
	wireI64    = 1
	wireLen    = 2
	wireSGroup = 3
	wireEGroup = 4
	wireI32    = 5
)

// wireNames are the wire types as the text form names them.
var wireNames = [...]string{"VARINT", "I64", "LEN", "SGROUP", "EGROUP", "I32"}

// maxField is the largest field number protobuf allows.
const maxField = 1<<29 - 1

// maxVarintLen is the most bytes a 64-bit varint takes.
const maxVarintLen = 10

// indentUnit indents a nested message or group by one level.
const indentUnit = "  "

// maxDepth is how many levels of nested messages and groups the text form
// shows, protobuf's own default recursion limit. Decode writes a LEN field
// that would nest deeper as a hex literal, which still encodes to the same
// bytes, so that a payload of many nested levels costs time and text in
// proportion to its size rather than its size times its depth. Encode
// refuses braces nested deeper than this.
const maxDepth = 100

// maxIndent is the indentation of maxDepth levels. Groups past maxDepth,
// which do not nest by recursion, are not indented further, so the text of
// deeply nested groups stays in proportion to their count.
var maxIndent = strings.Repeat(indentUnit, maxDepth)

// indentAt is the indentation of level, no deeper than maxDepth levels.
func indentAt(level int) string {
	return maxIndent[:min(max(level, 0), maxDepth)*len(indentUnit)]
}

// Decode is payload in the text form: a message's fields one per line,
// nested messages and groups indented, or a hex literal when payload is
// not a protobuf message.
func Decode(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	fields, ok := parseMessage(payload)
	if !ok {
		return hexLiteral(payload) + "\n"
	}
	var b strings.Builder
	writeFields(&b, fields, 0)
	return b.String()
}

// field is one record of a message: a tag and its value. A group's start
// and end are each a field, so a group's contents sit between them.
type field struct {
	number   uint64
	wire     int
	tagExtra int // bytes past the tag's minimal varint
	// value is the varint, or the I32 or I64 value.
	value uint64
	// valueExtra is the bytes past the varint value's, or the LEN
	// prefix's, minimal varint.
	valueExtra int
	// payload is a LEN field's contents.
	payload []byte
}

// parseMessage splits b into fields. It reports false when b is not a
// whole message: a truncated record, a bad wire type, field number 0 or
// past maxField, or groups that do not nest with matching numbers.
func parseMessage(b []byte) ([]field, bool) {
	var fields []field
	var open []uint64
	for len(b) > 0 {
		tag, tagLen, ok := readVarint(b)
		if !ok {
			return nil, false
		}
		b = b[tagLen:]
		f := field{number: tag >> 3, wire: int(tag & 7), tagExtra: tagLen - varintLen(tag)}
		if f.number == 0 || f.number > maxField {
			return nil, false
		}
		switch f.wire {
		case wireVarint:
			v, n, ok := readVarint(b)
			if !ok {
				return nil, false
			}
			f.value, f.valueExtra = v, n-varintLen(v)
			b = b[n:]
		case wireI64, wireI32:
			size := 8
			if f.wire == wireI32 {
				size = 4
			}
			if len(b) < size {
				return nil, false
			}
			for i := size - 1; i >= 0; i-- {
				f.value = f.value<<8 | uint64(b[i])
			}
			b = b[size:]
		case wireLen:
			length, n, ok := readVarint(b)
			if !ok || length > uint64(len(b)-n) {
				return nil, false
			}
			f.valueExtra = n - varintLen(length)
			f.payload = b[n : n+int(length)]
			b = b[n+int(length):]
		case wireSGroup:
			open = append(open, f.number)
		case wireEGroup:
			if len(open) == 0 || open[len(open)-1] != f.number {
				return nil, false
			}
			open = open[:len(open)-1]
		default:
			return nil, false
		}
		fields = append(fields, f)
	}
	return fields, len(open) == 0
}

// readVarint reads the varint at the start of b and its length in bytes.
// It reports false for a truncated varint or one past 64 bits.
func readVarint(b []byte) (uint64, int, bool) {
	var v uint64
	for i := 0; i < len(b) && i < maxVarintLen; i++ {
		c := b[i]
		if i == maxVarintLen-1 && c > 1 {
			return 0, 0, false
		}
		v |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

// varintLen is the length of v's minimal varint.
func varintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// writeFields writes fields one per line at level, a group's contents one
// level deeper than its start and end.
func writeFields(b *strings.Builder, fields []field, level int) {
	for _, f := range fields {
		if f.wire == wireEGroup {
			level--
		}
		b.WriteString(indentAt(level))
		writeLongForm(b, f.tagExtra)
		b.WriteString(strconv.FormatUint(f.number, 10))
		b.WriteByte(':')
		b.WriteString(wireNames[f.wire])
		switch f.wire {
		case wireVarint:
			b.WriteByte(' ')
			writeLongForm(b, f.valueExtra)
			b.WriteString(varintText(f.value))
		case wireI32:
			b.WriteString(" " + strconv.FormatUint(f.value, 10) + "i32")
		case wireI64:
			b.WriteString(" " + strconv.FormatUint(f.value, 10) + "i64")
		case wireLen:
			b.WriteByte(' ')
			writeLongForm(b, f.valueExtra)
			writeLen(b, f.payload, level)
		case wireSGroup:
			level++
		}
		b.WriteByte('\n')
	}
}

// writeLongForm writes the long-form prefix of a varint extra bytes past
// its minimal length, nothing when extra is 0.
func writeLongForm(b *strings.Builder, extra int) {
	if extra > 0 {
		b.WriteString("long-form:" + strconv.Itoa(extra) + " ")
	}
}

// varintText is a varint's value, negative when its top bit is set, as
// protoscope writes a negative int64.
func varintText(v uint64) string {
	if int64(v) < 0 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatUint(v, 10)
}

// writeLen writes the contents of a LEN field at level in braces: as text
// when it reads as text, as a nested message when it parses as one, as
// packed varints when it parses as minimal ones, and as a hex literal
// otherwise. Text comes first because a short string often also parses as a
// message. Contents that would nest past maxDepth are a hex literal.
func writeLen(b *strings.Builder, payload []byte, level int) {
	if len(payload) == 0 {
		b.WriteString("{}")
		return
	}
	if plainText(payload) {
		b.WriteString("{" + Quote(payload) + "}")
		return
	}
	if level+1 >= maxDepth {
		b.WriteString("{" + hexLiteral(payload) + "}")
		return
	}
	if fields, ok := parseMessage(payload); ok {
		b.WriteString("{\n")
		writeFields(b, fields, level+1)
		b.WriteString(indentAt(level) + "}")
		return
	}
	if values, ok := packedVarints(payload); ok {
		b.WriteByte('{')
		for i, v := range values {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(varintText(v))
		}
		b.WriteByte('}')
		return
	}
	if utf8.Valid(payload) {
		b.WriteString("{" + Quote(payload) + "}")
		return
	}
	b.WriteString("{" + hexLiteral(payload) + "}")
}

// plainText reports whether payload is UTF-8 with no control characters
// but tab, so that it reads as a string rather than a message.
func plainText(payload []byte) bool {
	if !utf8.Valid(payload) {
		return false
	}
	for _, r := range string(payload) {
		if !unicode.IsPrint(r) && r != '\t' {
			return false
		}
	}
	return true
}

// packedVarints splits payload into minimal varints. It reports false when
// one is truncated or non-minimal, which a bare number cannot express.
func packedVarints(payload []byte) ([]uint64, bool) {
	var values []uint64
	for len(payload) > 0 {
		v, n, ok := readVarint(payload)
		if !ok || n != varintLen(v) {
			return nil, false
		}
		values = append(values, v)
		payload = payload[n:]
	}
	return values, true
}

// Quote is s as a protoscope string literal, quotes included, as Decode
// writes one: printable runes as they are, `"` and `\` escaped, newline,
// tab and carriage return as \n, \t and \r, and the rest as \xNN a byte at
// a time. Encode reads it back as exactly s.
func Quote(s []byte) string {
	var b strings.Builder
	b.WriteByte('"')
	for len(s) > 0 {
		r, size := utf8.DecodeRune(s)
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r != utf8.RuneError && unicode.IsPrint(r):
			b.Write(s[:size])
		default:
			for _, c := range s[:size] {
				b.WriteString(`\x` + hex.EncodeToString([]byte{c}))
			}
		}
		s = s[size:]
	}
	b.WriteByte('"')
	return b.String()
}

// hexLiteral is b as a protoscope hex literal.
func hexLiteral(b []byte) string {
	return "`" + hex.EncodeToString(b) + "`"
}
