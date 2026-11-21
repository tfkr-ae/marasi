package marasi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/protoscope"
	"github.com/tfkr-ae/marasi/stream"
)

// A protobuf message with a nested message holding a string field and a
// varint, before and after the string is changed from "alice" to
// "mallory-admin": the nested length and the outer length both grow.
const (
	protoAlice   = "\x0a\x09\x0a\x05alice\x10\x07"
	protoMallory = "\x0a\x11\x0a\x0dmallory-admin\x10\x07"
	// protoPadded is 1:VARINT 150 with its varint padded past its minimal
	// length, which a re-encode from a guessed type would shorten.
	protoPadded = "\x08\x96\x81\x80\x00"
)

func TestProxy_ForwardCheckpointStreamText(t *testing.T) {
	t.Run("should encode an edited text form, so the origin gets a well-formed message with the new string, and forward an unedited message byte for byte", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, false, grpcEnvelope(0, protoAlice))
		held := awaitStreamItems(t, call.proxy, 1)[0]
		if !held.HasText || !strings.Contains(held.Text, `"alice"`) {
			t.Fatalf("the held item's text form\nwanted:\nthe string \"alice\"\ngot:\n%q %t", held.Text, held.HasText)
		}
		text := strings.Replace(held.Text, `"alice"`, `"mallory-admin"`, 1)
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{Text: &text}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoMallory))

		call.client.data(1, false, grpcEnvelope(0, protoPadded))
		unedited := awaitStreamItems(t, call.proxy, 1)[0]
		if err := call.proxy.ForwardCheckpoint(unedited.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoMallory)+grpcEnvelope(0, protoPadded))
	})

	t.Run("should refuse a text form that does not parse with its line and column, and keep the message held", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, false, grpcEnvelope(0, protoAlice))
		held := awaitStreamItems(t, call.proxy, 1)[0]
		text := "1:LEN {\n  1:LEN {\"alice\"\n"
		err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{Text: &text})
		if !errors.Is(err, protoscope.ErrSyntax) || !errors.Is(err, ErrStreamText) || !strings.Contains(err.Error(), "line 3 column 1") {
			t.Fatalf("the refusal\nwanted:\n%v at line 3 column 1\ngot:\n%v", ErrStreamText, err)
		}
		awaitStreamItems(t, call.proxy, 1)
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoAlice))
	})

	t.Run("should refuse an edit that gives both a payload and a text form", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, false, grpcEnvelope(0, protoAlice))
		held := awaitStreamItems(t, call.proxy, 1)[0]
		text, payload := `1:VARINT 1`, []byte("x")
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{Text: &text, Payload: &payload}); !errors.Is(err, ErrStreamText) {
			t.Fatalf("the refusal\nwanted:\n%v\ngot:\n%v", ErrStreamText, err)
		}
		awaitStreamItems(t, call.proxy, 1)
	})
}

func TestProxy_InjectStreamText(t *testing.T) {
	t.Run("should encode an injected text form and send it in an envelope, the same bytes a Checkpoint text edit gives", func(t *testing.T) {
		call := startGRPCCall(t)
		req := callPair(t, call)
		text := "1:LEN {\n  1:LEN {\"mallory-admin\"}\n  2:VARINT 7\n}\n"
		injected := injectTextOnceCarried(t, call.proxy, req.ID, text)
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoMallory))
		if string(injected.Payload) != protoMallory || injected.Metadata["injected"] != true {
			t.Fatalf("the injected message\nwanted:\n%q injected\ngot:\n%q %v", protoMallory, injected.Payload, injected.Metadata)
		}
	})

	t.Run("should refuse an injected text form that does not parse with its line and column, and send nothing", func(t *testing.T) {
		call := startGRPCCall(t)
		req := callPair(t, call)
		call.client.data(1, false, grpcEnvelope(0, protoAlice))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoAlice))
		_, err := call.proxy.InjectStreamText(context.Background(), req.ID, "request", "1:VARINT nope", StreamInjectOptions{})
		if !errors.Is(err, ErrStreamText) || !errors.Is(err, protoscope.ErrSyntax) || !strings.Contains(err.Error(), "line 1 column 10") {
			t.Fatalf("the refusal\nwanted:\n%v at line 1 column 10\ngot:\n%v", ErrStreamText, err)
		}
		call.client.data(1, false, grpcEnvelope(0, protoPadded))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoAlice)+grpcEnvelope(0, protoPadded))
	})
}

// injectTextOnceCarried injects text into the pair's request, retrying
// while the direction's carry has not started.
func injectTextOnceCarried(t *testing.T, proxy *Proxy, pair uuid.UUID, text string) domain.StreamMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		message, err := proxy.InjectStreamText(context.Background(), pair, "request", text, StreamInjectOptions{})
		if err == nil {
			return message
		}
		if !errors.Is(err, ErrStreamDirectionNotStreamed) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestH2Engine_StreamMessageSetText(t *testing.T) {
	t.Run("should send what set_text encodes to, the same bytes a Checkpoint text edit gives, and leave other messages byte for byte", func(t *testing.T) {
		call := startGRPCCall(t, withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				local text = message:text()
				if text and marasi.strings:contains(text, '"alice"') then
					message:set_text(marasi.strings:replace(text, '"alice"', '"mallory-admin"'))
				end
			end
		`))
		call.client.data(1, false, grpcEnvelope(0, protoAlice))
		call.client.data(1, false, grpcEnvelope(0, protoPadded))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoMallory)+grpcEnvelope(0, protoPadded))
	})

	t.Run("should raise for a text form that does not parse, naming its line and column, and send the message unchanged", func(t *testing.T) {
		call := startGRPCCall(t, withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				local ok, err = pcall(function() message:set_text("1:LEN {") end)
				message:set_metadata({ raised = not ok, err = tostring(err) })
			end
		`))
		req := callPair(t, call)
		call.client.data(1, false, grpcEnvelope(0, protoAlice))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, protoAlice))
		messages := storedMessages(t, call.proxy, req.ID)
		own, _ := messages[0].Metadata["rewriter"].(map[string]any)
		if len(messages) != 1 || own["raised"] != true || !strings.Contains(fmt.Sprint(own["err"]), "line 1 column 8") || messages[0].Metadata["edited"] != nil {
			t.Fatalf("the stored message\nwanted:\nset_text raised naming line 1 column 8, not edited\ngot:\n%+v", messages)
		}
	})
	t.Run("should raise for set_text on a message with no text form, leaving its payload", func(t *testing.T) {
		proxy := newTestProxy(t, &domain.Extension{ID: uuid.New(), Name: "rewriter", LuaContent: `
			function processStreamMessage(message)
				local ok, err = pcall(function() message:set_text("data: x") end)
				message:set_metadata({ raised = not ok, err = tostring(err) })
			end
		`})
		message := &stream.LiveMessage{Framing: stream.FramingSSE, ContentType: "text/event-stream", Payload: []byte("data: a\n\n")}

		processStreamMessageExtensions(proxy, uuid.Nil, message)

		own, _ := message.Metadata["rewriter"].(map[string]any)
		if string(message.Payload) != "data: a\n\n" || own["raised"] != true || !strings.Contains(fmt.Sprint(own["err"]), "no text form") {
			t.Fatalf("\nwanted:\nset_text raised for no text form, payload unchanged\ngot:\n%q %v", message.Payload, own)
		}
	})
}
