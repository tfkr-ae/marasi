package protoscope

import (
	"bytes"
	"errors"
	"testing"
)

func TestDecode(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    string
	}{
		{
			// The varint example of protobuf's encoding guide.
			name:    "should state a varint field with its wire type",
			payload: []byte{0x08, 0x96, 0x01},
			want:    "1:VARINT 150\n",
		},
		{
			name:    "should show a length-delimited field as a string when it reads as text",
			payload: []byte{0x12, 0x07, 't', 'e', 's', 't', 'i', 'n', 'g'},
			want:    "2:LEN {\"testing\"}\n",
		},
		{
			name:    "should show a length-delimited field as a nested message when it parses as one",
			payload: []byte{0x1a, 0x03, 0x08, 0x96, 0x01},
			want:    "3:LEN {\n  1:VARINT 150\n}\n",
		},
		{
			name:    "should show a length-delimited field as bytes when it is neither text nor a message",
			payload: []byte{0x0a, 0x02, 0xff, 0xfe},
			want:    "1:LEN {`fffe`}\n",
		},
		{
			name:    "should show packed varints as bare numbers",
			payload: []byte{0x22, 0x04, 0x01, 0x02, 0x96, 0x01},
			want:    "4:LEN {1 2 150}\n",
		},
		{
			name:    "should keep a non-minimal varint as long-form",
			payload: []byte{0x08, 0x81, 0x80, 0x00},
			want:    "1:VARINT long-form:2 1\n",
		},
		{
			name:    "should keep a non-minimal tag and length prefix as long-form",
			payload: []byte{0x8a, 0x00, 0x81, 0x00, 'x'},
			want:    "long-form:1 1:LEN long-form:1 {\"x\"}\n",
		},
		{
			name:    "should show fixed-width fields with their width",
			payload: []byte{0x0d, 0x01, 0x00, 0x00, 0x00, 0x11, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
			want:    "1:I32 1i32\n2:I64 18446744073709551615i64\n",
		},
		{
			name:    "should show a negative int64 varint as negative",
			payload: []byte{0x08, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
			want:    "1:VARINT -1\n",
		},
		{
			name:    "should indent a group's contents between its start and end",
			payload: []byte{0x0b, 0x10, 0x01, 0x0c},
			want:    "1:SGROUP\n  2:VARINT 1\n1:EGROUP\n",
		},
		{
			name:    "should keep split repeated fields in the order they came",
			payload: []byte{0x08, 0x01, 0x10, 0x02, 0x08, 0x03},
			want:    "1:VARINT 1\n2:VARINT 2\n1:VARINT 3\n",
		},
		{
			name:    "should show a payload that is not protobuf as a hex literal",
			payload: []byte{0x0a, 0x05, 'a'},
			want:    "`0a0561`\n",
		},
		{
			name:    "should show an empty payload as empty text",
			payload: []byte{},
			want:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decode(c.payload)
			if got != c.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", c.want, got)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []byte
	}{
		{
			name: "should encode hand-written text with comments, escapes and nesting",
			text: "# a comment\n1:LEN { 2:LEN {\"a\\x00\\n\"} 3:VARINT true }\n",
			want: []byte{0x0a, 0x07, 0x12, 0x03, 'a', 0x00, '\n', 0x18, 0x01},
		},
		{
			name: "should encode a negative varint as ten bytes",
			text: "1:VARINT -1",
			want: []byte{0x08, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
		},
		{
			name: "should encode floats in their width",
			text: "1:I32 1.5i32 2:I64 1.5",
			want: []byte{0x0d, 0x00, 0x00, 0xc0, 0x3f, 0x11, 0, 0, 0, 0, 0, 0, 0xf8, 0x3f},
		},
		{
			name: "should encode a negative i32 in 32 bits",
			text: "1:I32 -1i32",
			want: []byte{0x0d, 0xff, 0xff, 0xff, 0xff},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Encode(c.text)
			if err != nil || !bytes.Equal(got, c.want) {
				t.Fatalf("\nwanted:\n%x\ngot:\n%x %v", c.want, got, err)
			}
		})
	}

	for _, text := range []string{"1:LEN {", "}", "1:FOO 1", "\"open", "`0g`", "long-form:1 \"x\"", "long-form:10 1:VARINT 1", "1:I32 4294967296i32"} {
		t.Run("should refuse "+text+" as a syntax error", func(t *testing.T) {
			_, err := Encode(text)
			if !errors.Is(err, ErrSyntax) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrSyntax, err)
			}
		})
	}
}
