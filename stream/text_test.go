package stream

import "testing"

func TestTextForm(t *testing.T) {
	proto := []byte{0x08, 0x96, 0x01}
	cases := []struct {
		name        string
		contentType string
		payload     []byte
		want        string
		wantOK      bool
	}{
		{"should decode a gRPC message with no codec suffix", "application/grpc", proto, "1:VARINT 150\n", true},
		{"should decode a gRPC message named +proto", "application/grpc+proto; charset=utf-8", proto, "1:VARINT 150\n", true},
		{"should decode a gRPC-Web message", "application/grpc-web+proto", proto, "1:VARINT 150\n", true},
		{"should decode a gRPC-Web text message", "application/grpc-web-text", proto, "1:VARINT 150\n", true},
		{"should decode a Connect streaming message", "application/connect+proto", proto, "1:VARINT 150\n", true},
		{"should show a JSON codec's message as text", "application/grpc+json", []byte(`{"a":1}`), `{"a":1}`, true},
		{"should show a Connect JSON message as text", "application/connect+json", []byte(`{"a":1}`), `{"a":1}`, true},
		{"should give no text form for JSON that is not UTF-8", "application/grpc+json", []byte{0xff}, "", false},
		{"should give no text form for another codec", "application/grpc+thrift", proto, "", false},
		{"should give no text form for server-sent events", "text/event-stream", []byte("data: x\n\n"), "", false},
		{"should give no text form for an ordinary body", "application/json", []byte(`{}`), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := TextForm(c.contentType, c.payload)
			if got != c.want || ok != c.wantOK {
				t.Fatalf("\nwanted:\n%q %t\ngot:\n%q %t", c.want, c.wantOK, got, ok)
			}
		})
	}
}
