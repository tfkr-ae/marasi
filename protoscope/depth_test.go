package protoscope

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// deepLevels is how many levels the deep-nesting tests nest: enough that
// work or text in proportion to size times depth would not finish.
const deepLevels = 100_000

// nestedLen is deepLevels LEN fields, field 1, each holding the next, around
// the message 1:VARINT 1. It is written outside in from the lengths, as
// prepending each level would cost size times depth.
func nestedLen(levels int) []byte {
	inner := []byte{0x08, 0x01}
	lengths := make([]int, levels)
	length := len(inner)
	for i := range levels {
		lengths[i] = length
		length += 1 + varintLen(uint64(length))
	}
	out := make([]byte, 0, length)
	for i := levels - 1; i >= 0; i-- {
		out = append(out, 0x0a)
		for v := uint64(lengths[i]); ; v >>= 7 {
			if v < 0x80 {
				out = append(out, byte(v))
				break
			}
			out = append(out, byte(v)|0x80)
		}
	}
	return append(out, inner...)
}

func TestDecodeDepth(t *testing.T) {
	t.Run("should round-trip a message nested 100k levels deep", func(t *testing.T) {
		payload := nestedLen(deepLevels)

		text := Decode(payload)

		if len(text) > 4*len(payload)+maxDepth*maxDepth*len(indentUnit) {
			t.Fatalf("\nwanted:\ntext in proportion to the %d-byte payload\ngot:\n%d bytes", len(payload), len(text))
		}
		got, err := Encode(text)
		if err != nil {
			t.Fatalf("\nwanted:\nno error\ngot:\n%v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("\nwanted:\n%d bytes equal to the payload\ngot:\n%d bytes that differ", len(payload), len(got))
		}
	})

	t.Run("should show a message nested past the cap as a hex literal", func(t *testing.T) {
		text := Decode(nestedLen(maxDepth + 1))

		if !strings.Contains(text, "{`") {
			t.Fatalf("\nwanted:\na hex literal in braces\ngot:\n%s", text)
		}
		if got := strings.Count(text, "{\n"); got != maxDepth-1 {
			t.Fatalf("\nwanted:\n%d nested messages\ngot:\n%d", maxDepth-1, got)
		}
	})

	t.Run("should show a message nested within the cap as fields", func(t *testing.T) {
		text := Decode(nestedLen(maxDepth - 1))

		if strings.Contains(text, "`") {
			t.Fatalf("\nwanted:\nno hex literal\ngot:\n%s", text)
		}
	})

	t.Run("should round-trip groups nested 100k levels deep in text in proportion to their count", func(t *testing.T) {
		var payload []byte
		for range deepLevels {
			payload = append(payload, 0x0b) // 1:SGROUP
		}
		for range deepLevels {
			payload = append(payload, 0x0c) // 1:EGROUP
		}

		text := Decode(payload)

		if len(text) > 2*deepLevels*(len("1:SGROUP\n")+len(maxIndent)) {
			t.Fatalf("\nwanted:\ntext in proportion to %d groups\ngot:\n%d bytes", deepLevels, len(text))
		}
		got, err := Encode(text)
		if err != nil {
			t.Fatalf("\nwanted:\nno error\ngot:\n%v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("\nwanted:\n%d bytes equal to the payload\ngot:\n%d bytes that differ", len(payload), len(got))
		}
	})
}

func TestEncodeDepth(t *testing.T) {
	t.Run("should refuse braces nested 100k levels deep with a syntax error", func(t *testing.T) {
		text := strings.Repeat("1:LEN {", deepLevels) + strings.Repeat("}", deepLevels)

		_, err := Encode(text)

		if !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), "nested past") {
			t.Fatalf("\nwanted:\n%v naming the nesting\ngot:\n%v", ErrSyntax, err)
		}
	})

	t.Run("should encode braces nested to the cap", func(t *testing.T) {
		text := strings.Repeat("1:LEN {", maxDepth) + strings.Repeat("}", maxDepth)

		if _, err := Encode(text); err != nil {
			t.Fatalf("\nwanted:\nno error\ngot:\n%v", err)
		}
	})
}
