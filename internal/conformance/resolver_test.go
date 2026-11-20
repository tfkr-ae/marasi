package conformance

import "testing"

func TestResolverEditBody(t *testing.T) {
	t.Run("should send a stream message's payload back as its edit", func(t *testing.T) {
		body := editBody(heldItem{ID: "x", Type: "stream", Payload: []byte{0x08, 0x96, 0x01}})
		if want := `{"payload":"CJYB"}`; string(body) != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, body)
		}
	})

	t.Run("should hold stream messages with the stream-intercept switch", func(t *testing.T) {
		if flag, ok := CheckpointStream.flag(); !ok || flag != "stream-intercept" {
			t.Fatalf("\nwanted:\nstream-intercept\ngot:\n%q %t", flag, ok)
		}
	})
}
