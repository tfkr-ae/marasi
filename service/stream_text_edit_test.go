package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// assertRefusal checks answer is a refusal with error code and a message
// containing want.
func assertRefusal(t *testing.T, status int, answer, code, want string) {
	t.Helper()
	var refusal struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if status != http.StatusBadRequest || json.Unmarshal([]byte(answer), &refusal) != nil || refusal.Error != code || !strings.Contains(refusal.Message, want) {
		t.Fatalf("the refusal\nwanted:\n400 %s naming %q\ngot:\n%d %s", code, want, status, answer)
	}
}

func TestStreamTextEdits(t *testing.T) {
	t.Run("should forward a held message edited in its text form, encoded, and refuse text that does not parse with its position or that comes with a payload", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		if status, body := f.post(t, "/checkpoint/stream-intercept", `{"stream_intercept":true}`); status != http.StatusOK {
			t.Fatalf("turning the switch on\nwanted:\n200\ngot:\n%d %s", status, body)
		}
		body, sender := io.Pipe()
		defer sender.Close()
		f.call(t, body, -1)
		go func() { _, _ = sender.Write(grpcFrame([]byte{0x08, 0x96, 0x01})) }()
		item := f.held(t, 1)[0]
		path := "/checkpoint/" + item.ID.String() + "/forward"

		status, answer := f.post(t, path, `{"text":"1:VARINT nope"}`)
		assertRefusal(t, status, answer, "invalid_checkpoint_request", "line 1 column 10")
		status, answer = f.post(t, path, fmt.Sprintf(`{"text":"1:VARINT 1","payload":%q}`, base64.StdEncoding.EncodeToString([]byte("x"))))
		if status != http.StatusBadRequest {
			t.Fatalf("forwarding with a payload and a text form\nwanted:\n400\ngot:\n%d %s", status, answer)
		}
		if status, answer := f.post(t, path, `{"text":"1:LEN {\"mallory\"}"}`); status != http.StatusOK {
			t.Fatalf("forwarding with a text form\nwanted:\n200\ngot:\n%d %s", status, answer)
		}
		if got, want := f.receive(t), []byte("\x0a\x07mallory"); !bytes.Equal(got, want) {
			t.Fatalf("the origin's message\nwanted:\n%x\ngot:\n%x", want, got)
		}
	})

	t.Run("should inject a message given in its text form, encoded, and refuse text that does not parse with its position or that comes with a payload", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		body, sender := io.Pipe()
		defer sender.Close()
		f.call(t, body, -1)
		go func() { _, _ = sender.Write(grpcFrame([]byte("one"))) }()
		f.receive(t)
		path := "/stream/" + f.pairOf(t).String() + "/inject"

		status, answer := f.post(t, path, `{"direction":"request","text":"1:LEN {"}`)
		assertRefusal(t, status, answer, "invalid_stream_request", "line 1 column 8")
		status, answer = f.post(t, path, `{"direction":"request","text":"1:VARINT 1","payload":""}`)
		if status != http.StatusBadRequest {
			t.Fatalf("injecting a payload and a text form\nwanted:\n400\ngot:\n%d %s", status, answer)
		}
		status, answer = f.post(t, path, `{"direction":"request","text":"1:LEN {\"mallory\"}"}`)
		if status != http.StatusOK {
			t.Fatalf("injecting a text form\nwanted:\n200\ngot:\n%d %s", status, answer)
		}
		if got, want := f.receive(t), []byte("\x0a\x07mallory"); !bytes.Equal(got, want) {
			t.Fatalf("the origin's message\nwanted:\n%x\ngot:\n%x", want, got)
		}
	})
}
