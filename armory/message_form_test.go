package armory

import (
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// grpcHead is the head of a native gRPC template written as HTTP/2.
const grpcHead = "POST /test.Echo/Say HTTP/2\r\n:authority: example.com\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n"

// runMessageForm runs a harpoon attack with one wordlist on raw and returns
// every request the run sent, in order.
func runMessageForm(t *testing.T, raw, framing string, attack domain.ArmoryAttackType, wordlists map[string][]string) []string {
	t.Helper()
	sent, _ := runMessageFormAttempts(t, raw, framing, attack, wordlists)
	return sent
}

// runMessageFormAttempts runs raw as runMessageForm does and also returns
// every failed attempt the run recorded, and every one it announced.
func runMessageFormAttempts(t *testing.T, raw, framing string, attack domain.ArmoryAttackType, wordlists map[string][]string) ([]string, []*domain.ArmoryFailedAttempt) {
	t.Helper()
	run := newTestArmoryRun(t)
	run.TemplateSnapshot = raw
	run.TemplateFraming = framing
	run.AttackType = attack
	run.Wordlists = nil
	for name := range wordlists {
		run.Wordlists = append(run.Wordlists, name)
	}
	updates := make(chan domain.ArmoryRun, 4)
	var mu sync.Mutex
	var sent []string
	repository := &testExecutionRepository{run: run, updates: updates}
	manager := &Manager{
		repository: repository,
		wordlists:  &testAttackWordlistProvider{entriesByName: wordlists},
		sendFunc: func(_ context.Context, raw string, _ uuid.UUID, _ bool) error {
			mu.Lock()
			sent = append(sent, raw)
			mu.Unlock()
			return nil
		},
		activeRuns: make(map[uuid.UUID]*execution),
	}
	if err := manager.StartRun(run.ID); err != nil {
		t.Fatalf("starting run: %v", err)
	}
	waitForRunUpdate(t, updates)
	if finished := waitForRunUpdate(t, updates); finished.Status != domain.ArmoryRunCompleted {
		t.Fatalf("the run\nwanted:\n%s\ngot:\n%s", domain.ArmoryRunCompleted, finished.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return sent, repository.failedAttempts
}

// envelopes cuts body into gRPC envelopes, failing unless each length
// matches the bytes that follow it exactly.
func envelopes(t *testing.T, body string) [][]byte {
	t.Helper()
	var out [][]byte
	for len(body) > 0 {
		if len(body) < 5 {
			t.Fatalf("a trailing partial envelope header %q", body)
		}
		if body[0] != 0 {
			t.Fatalf("the envelope flags\nwanted:\n0\ngot:\n%d", body[0])
		}
		n := int(binary.BigEndian.Uint32([]byte(body[1:5])))
		if len(body) < 5+n {
			t.Fatalf("the envelope declares %d bytes but %d follow", n, len(body)-5)
		}
		out = append(out, []byte(body[5:5+n]))
		body = body[5+n:]
	}
	return out
}

// stringField is the value of a protobuf message holding one length-delimited
// field, field 1, failing unless the message is exactly that.
func stringField(t *testing.T, message []byte) string {
	t.Helper()
	if len(message) == 0 || message[0] != 0x0a {
		t.Fatalf("the message\nwanted:\nfield 1, wire type LEN\ngot:\n% x", message)
	}
	n, size := binary.Uvarint(message[1:])
	if size <= 0 || 1+size+int(n) != len(message) {
		t.Fatalf("the field length %d does not match the message % x", n, message)
	}
	return string(message[1+size:])
}

// bodyOf is what follows the blank line that ends raw's head.
func bodyOf(t *testing.T, raw string) string {
	t.Helper()
	_, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("the request has no blank line after its head: %q", raw)
	}
	return body
}

func TestManager_MessageFormTemplate(t *testing.T) {
	t.Run("should record a payload that does not encode as a failed attempt, not send it, and go on", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:VARINT @@1@@\n"

		sent, failed := runMessageFormAttempts(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {"1", "abc", "3"}})

		if len(sent) != 2 {
			t.Fatalf("sent requests\nwanted:\n2\ngot:\n%d", len(sent))
		}
		for i, want := range []string{"\x08\x01", "\x08\x03"} {
			messages := envelopes(t, bodyOf(t, sent[i]))
			if len(messages) != 1 || string(messages[0]) != want {
				t.Fatalf("request %d messages\nwanted:\n[% x]\ngot:\n% x", i, want, messages)
			}
		}
		if len(failed) != 1 {
			t.Fatalf("failed attempts\nwanted:\n1\ngot:\n%d", len(failed))
		}
		if want := []domain.ArmoryPayload{{Position: 0, Value: "abc"}}; !reflect.DeepEqual(failed[0].Payloads, want) {
			t.Fatalf("the failed attempt's payloads\nwanted:\n%+v\ngot:\n%+v", want, failed[0].Payloads)
		}
		if !strings.Contains(failed[0].Error, "message 1: line 7 column 10") {
			t.Fatalf("the failed attempt's error\nwanted:\ncontaining %q\ngot:\n%q", "message 1: line 7 column 10", failed[0].Error)
		}
		if failed[0].ID == uuid.Nil || failed[0].RunID == uuid.Nil || failed[0].CreatedAt.IsZero() {
			t.Fatalf("the failed attempt\nwanted:\nan ID, its run's ID and a time\ngot:\n%+v", failed[0])
		}
	})

	t.Run("should send payloads shorter and longer than the fallback in envelopes whose lengths match", func(t *testing.T) {
		long := strings.Repeat("A", 300)
		raw := grpcHead + "--- message ---\n1:LEN {\"@@fallback@@\"}\n"

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {"x", long}})

		if len(sent) != 2 {
			t.Fatalf("sent requests\nwanted:\n2\ngot:\n%d", len(sent))
		}
		for i, want := range []string{"x", long} {
			if head := sent[i][:len(grpcHead)]; head != grpcHead {
				t.Fatalf("request %d head\nwanted:\n%q\ngot:\n%q", i, grpcHead, head)
			}
			messages := envelopes(t, bodyOf(t, sent[i]))
			if len(messages) != 1 {
				t.Fatalf("request %d messages\nwanted:\n1\ngot:\n%d", i, len(messages))
			}
			if got := stringField(t, messages[0]); got != want {
				t.Fatalf("request %d field 1\nwanted:\n%q\ngot:\n%q", i, want, got)
			}
		}
	})

	t.Run("should send a message with no position exactly as the template encodes it", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n2:VARINT 150 # unedited\n--- message ---\n1:LEN {\"@@fallback@@\"}\n"

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {"payload"}})

		if len(sent) != 1 {
			t.Fatalf("sent requests\nwanted:\n1\ngot:\n%d", len(sent))
		}
		messages := envelopes(t, bodyOf(t, sent[0]))
		if len(messages) != 2 {
			t.Fatalf("messages\nwanted:\n2\ngot:\n%d", len(messages))
		}
		if want := "\x10\x96\x01"; string(messages[0]) != want {
			t.Fatalf("the unedited message\nwanted:\n% x\ngot:\n% x", want, messages[0])
		}
		if got := stringField(t, messages[1]); got != "payload" {
			t.Fatalf("the fuzzed message's field 1\nwanted:\n%q\ngot:\n%q", "payload", got)
		}
	})

	t.Run("should render a template with no framing byte for byte, a separator line being ordinary text", func(t *testing.T) {
		raw := "POST /submit HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/grpc\r\n\r\n--- message ---\n1:LEN {\"@@fallback@@\"}\n"

		sent := runMessageForm(t, raw, "", domain.ArmoryAttackHarpoon, map[string][]string{"words": {"x\\y"}})

		want := "POST /submit HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/grpc\r\n\r\n--- message ---\n1:LEN {\"x\\y\"}\n"
		if len(sent) != 1 || sent[0] != want {
			t.Fatalf("sent requests\nwanted:\n%q\ngot:\n%q", want, sent)
		}
	})
}

func TestManager_ValidateTemplate(t *testing.T) {
	manager := &Manager{}
	refusals := []struct {
		name    string
		raw     string
		framing string
		want    string
	}{
		{
			name:    "a content type that does not name gRPC",
			raw:     "POST /test.Echo/Say HTTP/2\r\n:authority: example.com\r\ncontent-type: application/json\r\n\r\n--- message ---\n1:LEN {\"a\"}\n",
			framing: "grpc",
			want:    `content type "application/json"`,
		},
		{
			name:    "a body that does not start with a separator",
			raw:     grpcHead + "1:LEN {\"a\"}\n--- message ---\n1:LEN {\"b\"}\n",
			framing: "grpc",
			want:    "line 6: a message-form body must start with",
		},
		{
			name:    "an empty body",
			raw:     grpcHead,
			framing: "grpc",
			want:    "must start with",
		},
		{
			name:    "a message that does not encode with the fallback payloads",
			raw:     grpcHead + "--- message ---\n1:LEN {\"a\"}\n--- message ---\n1:LEN {\n  2:VARINT @@nope@@\n}\n",
			framing: "grpc",
			want:    "message 2: line 10 column 12",
		},
		{
			name:    "a framing Marasi does not know",
			raw:     grpcHead + "--- message ---\n1:LEN {\"a\"}\n",
			framing: "soap",
			want:    `framing "soap" is not supported`,
		},
	}
	for _, tt := range refusals {
		t.Run("should refuse "+tt.name, func(t *testing.T) {
			err := manager.ValidateTemplate(&domain.ArmoryTemplate{RawTemplate: tt.raw, Framing: tt.framing})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%v", tt.want, err)
			}
		})
	}

	t.Run("should accept a gRPC template whose messages encode", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:LEN {\"@@a@@\"}\n--- message ---\n2:VARINT 1\n"
		if err := manager.ValidateTemplate(&domain.ArmoryTemplate{RawTemplate: raw, Framing: "grpc"}); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should accept a template with no framing whose body has a separator line", func(t *testing.T) {
		raw := "POST / HTTP/1.1\r\nHost: example.com\r\n\r\n--- message ---\nnot protoscope {"
		if err := manager.ValidateTemplate(&domain.ArmoryTemplate{RawTemplate: raw}); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}
