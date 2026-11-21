package protoscope

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"
)

// roundTripRuns is how many generated encodings the property checks.
const roundTripRuns = 5000

// The property of the ADR: encoding the text Decode gives of any valid
// encoding is that encoding, byte for byte. The generator writes the wire
// format itself, independent of the codec, with non-minimal varints,
// unordered and split repeated fields, packed fields and groups.
func TestRoundTrip(t *testing.T) {
	t.Run("should give back the exact bytes of an arbitrary valid encoding", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 2))
		for i := range roundTripRuns {
			payload := genMessage(rng, 3)
			assertRoundTrip(t, payload, i)
		}
	})

	t.Run("should give back the exact bytes of arbitrary bytes that are not protobuf", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(3, 4))
		for i := range roundTripRuns {
			payload := make([]byte, rng.IntN(40))
			for j := range payload {
				payload[j] = byte(rng.UintN(256))
			}
			assertRoundTrip(t, payload, i)
		}
	})
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte{0x08, 0x96, 0x01})
	f.Add([]byte{0x0b, 0x12, 0x02, 'h', 'i', 0x0c})
	f.Add([]byte{0x8a, 0x00, 0x81, 0x00, 'x'})
	f.Fuzz(func(t *testing.T, payload []byte) {
		assertRoundTrip(t, payload, 0)
	})
}

func assertRoundTrip(t *testing.T, payload []byte, run int) {
	t.Helper()
	text := Decode(payload)
	got, err := Encode(text)
	if err != nil {
		t.Fatalf("run %d: encoding %x\nwanted:\nno error\ngot:\n%v\ntext:\n%s", run, payload, err, text)
	}
	if !bytes.Equal(got, payload) && !(len(got) == 0 && len(payload) == 0) {
		t.Fatalf("run %d\nwanted:\n%x\ngot:\n%x\ntext:\n%s", run, payload, got, text)
	}
}

// genMessage writes a random valid message, nesting up to depth levels.
func genMessage(rng *rand.Rand, depth int) []byte {
	var b []byte
	for range rng.IntN(6) {
		number := genFieldNumber(rng)
		switch kind := rng.IntN(8); {
		case kind == 0:
			b = genTag(rng, b, number, wireI32)
			b = binary.LittleEndian.AppendUint32(b, rng.Uint32())
		case kind == 1:
			b = genTag(rng, b, number, wireI64)
			b = binary.LittleEndian.AppendUint64(b, rng.Uint64())
		case kind == 2:
			b = genTag(rng, b, number, wireLen)
			b = genLen(rng, b, genBytes(rng))
		case kind == 3 && depth > 0:
			b = genTag(rng, b, number, wireLen)
			b = genLen(rng, b, genMessage(rng, depth-1))
		case kind == 4:
			// A packed repeated field, split across records.
			for range 1 + rng.IntN(3) {
				var packed []byte
				for range rng.IntN(4) {
					packed = genVarint(rng, packed, genValue(rng))
				}
				b = genTag(rng, b, number, wireLen)
				b = genLen(rng, b, packed)
			}
		case kind == 5 && depth > 0:
			b = genTag(rng, b, number, wireSGroup)
			b = append(b, genMessage(rng, depth-1)...)
			b = genTag(rng, b, number, wireEGroup)
		case kind == 6:
			b = genTag(rng, b, number, wireLen)
			b = genLen(rng, b, []byte("text \"quoted\"\n\x00é"))
		default:
			b = genTag(rng, b, number, wireVarint)
			b = genVarint(rng, b, genValue(rng))
		}
	}
	return b
}

// genFieldNumber is a field number, small ones often so fields repeat out
// of order.
func genFieldNumber(rng *rand.Rand) uint64 {
	if rng.IntN(4) == 0 {
		return 1 + rng.Uint64N(maxField)
	}
	return 1 + rng.Uint64N(5)
}

func genValue(rng *rand.Rand) uint64 {
	switch rng.IntN(3) {
	case 0:
		return rng.Uint64N(128)
	case 1:
		return rng.Uint64N(1 << 20)
	}
	return rng.Uint64()
}

func genBytes(rng *rand.Rand) []byte {
	b := make([]byte, rng.IntN(12))
	for i := range b {
		b[i] = byte(rng.UintN(256))
	}
	return b
}

func genTag(rng *rand.Rand, b []byte, number uint64, wire int) []byte {
	return genVarint(rng, b, number<<3|uint64(wire))
}

func genLen(rng *rand.Rand, b, contents []byte) []byte {
	b = genVarint(rng, b, uint64(len(contents)))
	return append(b, contents...)
}

// genVarint appends v as a varint, sometimes padded past its minimal
// length, up to the ten bytes a varint may take.
func genVarint(rng *rand.Rand, b []byte, v uint64) []byte {
	length := varintLength(v)
	if rng.IntN(4) == 0 {
		length += rng.IntN(10 - length + 1)
	}
	for i := range length {
		c := byte(v & 0x7f)
		v >>= 7
		if i < length-1 {
			c |= 0x80
		}
		b = append(b, c)
	}
	return b
}

func varintLength(v uint64) int {
	n := 1
	for ; v >= 0x80; v >>= 7 {
		n++
	}
	return n
}
