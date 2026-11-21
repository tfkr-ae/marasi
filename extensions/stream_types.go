package extensions

import (
	"context"
	"fmt"
	"maps"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"

	"github.com/tfkr-ae/marasi/stream"
)

// streamMessageType is the Lua type of a stream message.
const streamMessageType = "streammsg"

// RegisterStreamMessageType registers the stream message type and its
// methods with the Lua state. It mirrors wsmsg.
func RegisterStreamMessageType(extension *Runtime) {
	check := func(l *lua.State) *stream.LiveMessage {
		return lua.CheckUserData(l, 1, streamMessageType).(*stream.LiveMessage)
	}
	funcs := []APIFunction{
		{Name: "id", Signature: "() -> string", Summary: "Returns the message's UUID.", Function: func(l *lua.State) int {
			l.PushString(check(l).ID.String())
			return 1
		}},
		{Name: "pair_id", Signature: "() -> string", Summary: "Returns the UUID of the request/response pair whose body carries the message.", Function: func(l *lua.State) int {
			l.PushString(check(l).PairID.String())
			return 1
		}},
		{Name: "direction", Signature: "() -> string", Summary: "Returns the direction that carries the message: \"request\" (from the client) or \"response\" (from the origin).", Function: func(l *lua.State) int {
			l.PushString(check(l).Direction)
			return 1
		}},
		{Name: "framing", Signature: "() -> string", Summary: "Returns the direction's message framing, such as \"grpc\".", Function: func(l *lua.State) int {
			l.PushString(string(check(l).Framing))
			return 1
		}},
		{Name: "flags", Signature: "() -> integer", Summary: "Returns the envelope's flags byte.", Function: func(l *lua.State) int {
			l.PushInteger(int(check(l).Flags))
			return 1
		}},
		{Name: "payload", Signature: "() -> string", Summary: "Returns the message payload without its envelope, decompressed when compressed() is true and the encoding is gzip or deflate; as it is on the wire when editable() is false.", Function: func(l *lua.State) int {
			l.PushString(string(check(l).Payload))
			return 1
		}},
		{Name: "text", Signature: "() -> string?", Summary: "Returns the payload in its text form: the protoscope text of a protobuf gRPC, gRPC-Web or Connect message, or the text of a JSON one. Returns nil for a message with no text form, or one editable() is false for.", Function: func(l *lua.State) int {
			if text, ok := check(l).Text(); ok {
				l.PushString(text)
			} else {
				l.PushNil()
			}
			return 1
		}},
		{Name: "set_payload", Signature: "(payload: string)", Summary: "Replaces the message payload; the envelope's length follows it. An edited compressed message goes out uncompressed with its compressed flag cleared. A change is ignored, and a proxy log says why, when editable() is false, or on a direction whose head declares a length when the message would go out at another length.", Function: func(l *lua.State) int {
			message := check(l)
			message.Payload = []byte(lua.CheckString(l, 2))
			return 0
		}},
		{Name: "set_text", Signature: "(text: string)", Summary: "Replaces the message payload with what its text form encodes to, as text() shows it: protoscope text is encoded with every enclosing length following the edit, and a JSON codec's text is the payload. Raises an error, leaving the payload unchanged, for text that does not parse, naming its line and column, or for a message with no text form; pcall it to recover. Otherwise it is set_payload, and is ignored and logged where set_payload would be.", Function: func(l *lua.State) int {
			message := check(l)
			if err := message.SetText(lua.CheckString(l, 2)); err != nil {
				lua.Errorf(l, "set_text: %s", err.Error())
			}
			return 0
		}},
		{Name: "length", Signature: "() -> integer", Summary: "Returns the payload length in bytes.", Function: func(l *lua.State) int {
			l.PushInteger(len(check(l).Payload))
			return 1
		}},
		{Name: "compressed", Signature: "() -> boolean", Summary: "Reports whether the envelope's compressed flag is set: the message is compressed on the wire with encoding().", Function: func(l *lua.State) int {
			l.PushBoolean(check(l).Compressed())
			return 1
		}},
		{Name: "encoding", Signature: "() -> string", Summary: "Returns the direction's per-message encoding from grpc-encoding (gRPC, gRPC-Web) or connect-content-encoding (Connect), such as \"gzip\"; \"\" when the head names none.", Function: func(l *lua.State) int {
			l.PushString(check(l).Encoding)
			return 1
		}},
		{Name: "editable", Signature: "() -> boolean", Summary: "Reports whether set_payload and set_text can change the message: false for a message compressed with an encoding other than gzip or deflate, whose payload() is the compressed bytes.", Function: func(l *lua.State) int {
			l.PushBoolean(!check(l).ReadOnly)
			return 1
		}},
		{Name: "metadata", Signature: "() -> table", Summary: "Returns the message metadata, keyed by extension name.", Function: func(l *lua.State) int {
			util.DeepPush(l, check(l).Metadata)
			return 1
		}},
		{Name: "set_metadata", Signature: "(metadata: table)", Summary: "Merges a key-value table into this extension's metadata on the message.", Function: func(l *lua.State) int {
			message := check(l)
			metadata, ok := ParseTable(l, 2, GoValue).(map[string]any)
			if !ok {
				lua.ArgumentError(l, 2, "metadata must be a key-value table, not an array")
				return 0
			}
			if message.Metadata == nil {
				message.Metadata = make(map[string]any)
			}
			own, _ := message.Metadata[extension.Data.Name].(map[string]any)
			if own == nil {
				own = make(map[string]any)
			}
			maps.Copy(own, metadata)
			message.Metadata[extension.Data.Name] = own
			return 0
		}},
		{Name: "drop", Signature: "()", Summary: "Marks the message to be dropped instead of forwarded. On a direction whose head declares a length, the drop is ignored and a proxy log says why.", Function: func(l *lua.State) int {
			check(l).Dropped = true
			return 0
		}},
		{Name: "skip", Signature: "()", Summary: "Marks the message to skip later extensions while still forwarding it.", Function: func(l *lua.State) int {
			check(l).Skipped = true
			return 0
		}},
	}
	RegisterType(extension, streamMessageType, "A stream message of a streamed pair, such as one gRPC message, passed to processStreamMessage.", funcs, func(l *lua.State) int {
		message := check(l)
		l.PushString(fmt.Sprintf("StreamMessage { ID: %s, PairID: %s, Direction: %s, Framing: %s, Length: %d }",
			message.ID, message.PairID, message.Direction, message.Framing, len(message.Payload)))
		return 1
	})
}

// CallStreamMessageHandler calls the `processStreamMessage` function in the
// Lua script, passing the live stream message to be processed.
func (extension *Runtime) CallStreamMessageHandler(message *stream.LiveMessage) error {
	extension.Mu.Lock()
	defer extension.Mu.Unlock()
	defer extension.beginExecution(context.Background())()

	extension.LuaState.Global(hookProcessStreamMessage)
	if !extension.LuaState.IsFunction(-1) {
		extension.LuaState.Pop(1)
		return nil
	}

	extension.LuaState.PushUserData(message)
	lua.SetMetaTableNamed(extension.LuaState, streamMessageType)
	if err := extension.LuaState.ProtectedCall(1, 0, 0); err != nil {
		extension.LuaState.Pop(1)
		return fmt.Errorf("calling processStreamMessage: %w", err)
	}
	return nil
}

// ShouldInterceptStreamMessage calls the `interceptStreamMessage` function in
// the Lua script to decide whether Checkpoint holds the stream message.
func (extension *Runtime) ShouldInterceptStreamMessage(message *stream.LiveMessage) (bool, error) {
	extension.Mu.Lock()
	defer extension.Mu.Unlock()
	defer extension.beginExecution(context.Background())()

	extension.LuaState.Global(hookInterceptStreamMessage)
	if !extension.LuaState.IsFunction(-1) {
		extension.LuaState.Pop(1)
		return false, nil
	}

	extension.LuaState.PushUserData(message)
	lua.SetMetaTableNamed(extension.LuaState, streamMessageType)
	if err := extension.LuaState.ProtectedCall(1, 1, 0); err != nil {
		extension.LuaState.Pop(1)
		return false, fmt.Errorf("calling interceptStreamMessage: %w", err)
	}
	intercept := extension.LuaState.ToBoolean(-1)
	extension.LuaState.Pop(1)
	return intercept, nil
}
