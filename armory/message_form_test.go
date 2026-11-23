package armory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strconv"
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
			name:    "a gRPC-Web framing whose content type names gRPC",
			raw:     webHead("application/grpc") + "--- message ---\n1:LEN {\"a\"}\n",
			framing: "grpc-web",
			want:    `content type "application/grpc"`,
		},
		{
			name:    "a Connect framing whose content type names gRPC-Web",
			raw:     webHead("application/grpc-web") + "--- message ---\n1:LEN {\"a\"}\n",
			framing: "connect",
			want:    `content type "application/grpc-web"`,
		},
		{
			name:    "a gRPC framing whose content type is Connect unary",
			raw:     webHead("application/proto") + "--- message ---\n1:LEN {\"a\"}\n",
			framing: "grpc",
			want:    `content type "application/proto"`,
		},
		{
			name:    "a Connect unary body with two separators",
			raw:     webHead("application/proto") + "--- message ---\n1:LEN {\"a\"}\n--- message ---\n1:LEN {\"b\"}\n",
			framing: "connect",
			want:    "exactly one message, not 2",
		},
		{
			name:    "a Connect unary body with no separator",
			raw:     webHead("application/json") + "{\"a\":1}",
			framing: "connect",
			want:    "must start with",
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

func TestManager_MessageFormEscaping(t *testing.T) {
	t.Run("should send payloads with quotes, backslashes, newlines and non-printable bytes as exactly their bytes", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:LEN {\"@@fallback@@\"}\n"
		payloads := []string{`a"b`, `a\b`, "\n", "\x00"}

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": payloads})

		if len(sent) != len(payloads) {
			t.Fatalf("sent requests\nwanted:\n%d\ngot:\n%d", len(payloads), len(sent))
		}
		for i, want := range payloads {
			if got := stringField(t, envelopes(t, bodyOf(t, sent[i]))[0]); got != want {
				t.Fatalf("request %d field 1\nwanted:\n%q\ngot:\n%q", i, want, got)
			}
		}
	})

	t.Run("should run a position's function chain before escaping its value", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:LEN {\"@@fallback@@.upper()\"}\n"

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {`a"b`}})

		if got := stringField(t, envelopes(t, bodyOf(t, sent[0]))[0]); got != `A"B` {
			t.Fatalf("field 1\nwanted:\n%q\ngot:\n%q", `A"B`, got)
		}
	})

	t.Run("should put a payload in as written when its chain ends in raw, so it can add a field", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:LEN {\"@@x@@.raw()\"}\n"

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {`"} 2:LEN {"y`}})

		messages := envelopes(t, bodyOf(t, sent[0]))
		if want := "\x0a\x00\x12\x01y"; string(messages[0]) != want {
			t.Fatalf("the message\nwanted:\n% x\ngot:\n% x", want, messages[0])
		}
	})

	t.Run("should put a payload on a varint field in as written", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n2:VARINT @@1@@\n"

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {"150"}})

		if want := "\x10\x96\x01"; string(envelopes(t, bodyOf(t, sent[0]))[0]) != want {
			t.Fatalf("the message\nwanted:\n% x\ngot:\n% x", want, envelopes(t, bodyOf(t, sent[0]))[0])
		}
	})

	t.Run("should send a fallback written with protoscope escapes as the bytes it names", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:LEN {\"@@c@@\"}\n--- message ---\n1:LEN {\"" + `@@a\\x00\\"b@@` + "\"}\n"

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {"d"}})

		if len(sent) != 2 {
			t.Fatalf("sent requests\nwanted:\n2\ngot:\n%d", len(sent))
		}
		if got := stringField(t, envelopes(t, bodyOf(t, sent[0]))[1]); got != "a\x00\"b" {
			t.Fatalf("the second message's field 1\nwanted:\n%q\ngot:\n%q", "a\x00\"b", got)
		}
	})

	t.Run("should refuse to save a position that opens inside a quoted string and closes outside it", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:LEN {\"a@@b\"} 2:VARINT 1@@\n"

		err := (&Manager{}).ValidateTemplate(&domain.ArmoryTemplate{RawTemplate: raw, Framing: "grpc"})

		want := "payload position 0 at line 7 column 10"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%v", want, err)
		}
	})

	t.Run("should put a payload in a JSON message in as written", func(t *testing.T) {
		raw := strings.Replace(grpcHead, "application/grpc", "application/grpc+json", 1) + "--- message ---\n{\"a\":\"@@x@@\"}\n"

		sent := runMessageForm(t, raw, "grpc", domain.ArmoryAttackHarpoon, map[string][]string{"words": {`q\"`}})

		if got, want := string(envelopes(t, bodyOf(t, sent[0]))[0]), "{\"a\":\"q\\\"\"}"; got != want {
			t.Fatalf("the message\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})
}

// webHead is a gRPC-Web, Connect or Connect unary template head written as
// HTTP/1.1 with contentType and a Content-Length the render must set.
func webHead(contentType string) string {
	return "POST /test.Echo/Say HTTP/1.1\r\nHost: example.com\r\nContent-Type: " + contentType + "\r\nContent-Length: 0\r\n\r\n"
}

// contentLength is the value of raw's Content-Length line, failing unless
// it has exactly one.
func contentLength(t *testing.T, raw string) string {
	t.Helper()
	head, _, _ := strings.Cut(raw, "\r\n\r\n")
	var values []string
	for _, line := range strings.Split(head, "\r\n")[1:] {
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(name, "Content-Length") {
			values = append(values, strings.TrimSpace(value))
		}
	}
	if len(values) != 1 {
		t.Fatalf("Content-Length lines\nwanted:\n1\ngot:\n%q", values)
	}
	return values[0]
}

func TestManager_WebAndConnectFramings(t *testing.T) {
	long := strings.Repeat("A", 300)
	framings := []struct {
		name        string
		contentType string
		framing     string
		message     string
		// messages cuts a sent body into its messages' payloads.
		messages func(t *testing.T, body string) [][]byte
		// field reads the fuzzed field out of a message's payload.
		field func(t *testing.T, message []byte) string
	}{
		{
			name:        "gRPC-Web binary",
			contentType: "application/grpc-web+proto",
			framing:     "grpc-web",
			message:     "1:LEN {\"@@fallback@@\"}\n",
			messages:    envelopes,
			field:       stringField,
		},
		{
			name:        "gRPC-Web text",
			contentType: "application/grpc-web-text",
			framing:     "grpc-web",
			message:     "1:LEN {\"@@fallback@@\"}\n",
			messages: func(t *testing.T, body string) [][]byte {
				decoded, err := base64.StdEncoding.DecodeString(body)
				if err != nil {
					t.Fatalf("decoding the base64 body %q: %v", body, err)
				}
				return envelopes(t, string(decoded))
			},
			field: stringField,
		},
		{
			name:        "Connect streaming proto",
			contentType: "application/connect+proto",
			framing:     "connect",
			message:     "1:LEN {\"@@fallback@@\"}\n",
			messages:    envelopes,
			field:       stringField,
		},
		{
			name:        "Connect streaming JSON",
			contentType: "application/connect+json",
			framing:     "connect",
			message:     "{\"a\":\"@@fallback@@\"}\n",
			messages:    envelopes,
			field:       jsonField,
		},
		{
			name:        "Connect unary proto",
			contentType: "application/proto",
			framing:     "connect",
			message:     "1:LEN {\"@@fallback@@\"}\n",
			messages:    func(_ *testing.T, body string) [][]byte { return [][]byte{[]byte(body)} },
			field:       stringField,
		},
		{
			name:        "Connect unary JSON",
			contentType: "application/json",
			framing:     "connect",
			message:     "{\"a\":\"@@fallback@@\"}\n",
			messages:    func(_ *testing.T, body string) [][]byte { return [][]byte{[]byte(body)} },
			field:       jsonField,
		},
	}
	for _, tt := range framings {
		t.Run("should send "+tt.name+" payloads shorter and longer than the fallback as a well-formed body with its Content-Length", func(t *testing.T) {
			head := webHead(tt.contentType)
			raw := head + "--- message ---\n" + tt.message

			sent := runMessageForm(t, raw, tt.framing, domain.ArmoryAttackHarpoon, map[string][]string{"words": {"x", long}})

			if len(sent) != 2 {
				t.Fatalf("sent requests\nwanted:\n2\ngot:\n%d", len(sent))
			}
			for i, want := range []string{"x", long} {
				body := bodyOf(t, sent[i])
				if got := contentLength(t, sent[i]); got != strconv.Itoa(len(body)) {
					t.Fatalf("request %d Content-Length\nwanted:\n%d\ngot:\n%s", i, len(body), got)
				}
				messages := tt.messages(t, body)
				if len(messages) != 1 {
					t.Fatalf("request %d messages\nwanted:\n1\ngot:\n%d", i, len(messages))
				}
				if got := tt.field(t, messages[0]); got != want {
					t.Fatalf("request %d field\nwanted:\n%q\ngot:\n%q", i, want, got)
				}
			}
		})
	}
}

// jsonField is the value of a JSON message that is exactly {"a":"value"}
// with nothing after it.
func jsonField(t *testing.T, message []byte) string {
	t.Helper()
	var value struct{ A string }
	decoder := json.NewDecoder(bytes.NewReader(message))
	if err := decoder.Decode(&value); err != nil || decoder.InputOffset() != int64(len(message)) {
		t.Fatalf("the message\nwanted:\nexactly one JSON object\ngot:\n%q (%v)", message, err)
	}
	return value.A
}

func TestManager_WebAndConnectEscaping(t *testing.T) {
	t.Run("should put a payload in a Connect unary JSON message in as written, with no protoscope escaping", func(t *testing.T) {
		raw := webHead("application/json") + "--- message ---\n{\"a\":\"@@x@@\"}\n"

		sent := runMessageForm(t, raw, "connect", domain.ArmoryAttackHarpoon, map[string][]string{"words": {`q\"`}})

		if got, want := bodyOf(t, sent[0]), "{\"a\":\"q\\\"\"}"; got != want {
			t.Fatalf("the body\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should escape a payload in a quoted string of a Connect unary proto message", func(t *testing.T) {
		raw := webHead("application/proto") + "--- message ---\n1:LEN {\"@@x@@\"}\n"
		payload := "q\"\\\n\x00"

		sent := runMessageForm(t, raw, "connect", domain.ArmoryAttackHarpoon, map[string][]string{"words": {payload}})

		if got := stringField(t, []byte(bodyOf(t, sent[0]))); got != payload {
			t.Fatalf("field 1\nwanted:\n%q\ngot:\n%q", payload, got)
		}
	})
}
