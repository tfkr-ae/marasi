package conformance

import "testing"

func TestResolverEditBody(t *testing.T) {
	t.Run("should send a stream message's payload back as its edit", func(t *testing.T) {
		body := editBody(heldItem{ID: "x", Type: "stream", Payload: []byte{0x08, 0x96, 0x01}})
		if want := `{"payload":"CJYB"}`; string(body) != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, body)
		}
	})

	t.Run("should send a trailer block back as its raw edit", func(t *testing.T) {
		body := editBody(heldItem{ID: "x", Type: "trailer", Raw: []byte("grpc-status: 0\r\n")})
		if want := `{"raw":"Z3JwYy1zdGF0dXM6IDANCg=="}`; string(body) != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, body)
		}
	})

	t.Run("should hold stream messages with the stream-intercept switch", func(t *testing.T) {
		if flag, ok := CheckpointStream.flag(); !ok || flag != "stream-intercept" {
			t.Fatalf("\nwanted:\nstream-intercept\ngot:\n%q %t", flag, ok)
		}
	})
}
