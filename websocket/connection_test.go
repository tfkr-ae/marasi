package websocket

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type testReadWriteCloser struct {
	bytes.Buffer
	closed     bool
	closeCount int
	closeErr   error
}

func (c *testReadWriteCloser) Close() error {
	c.closed = true
	c.closeCount++
	return c.closeErr
}

type failingReadWriteCloser struct {
	err      error
	closeErr error
}

type errorReader struct {
	err error
}

type closeStartingReader struct {
	reader     io.Reader
	connection *Connection
}

func (r *errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func (r *closeStartingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.connection.closing.Store(true)
	return n, err
}

func (c *failingReadWriteCloser) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (c *failingReadWriteCloser) Write([]byte) (int, error) {
	return 0, c.err
}

func (c *failingReadWriteCloser) Close() error {
	return c.closeErr
}

func encodeTestFrames(t *testing.T, frames ...Frame) []byte {
	t.Helper()

	var output bytes.Buffer
	for _, frame := range frames {
		if err := WriteFrame(&output, frame, false); err != nil {
			t.Fatalf("WriteFrame() returned unexpected error: %v", err)
		}
	}

	return output.Bytes()
}

func TestHijackedConn_ReadsBufferedInput(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	buffered := bufio.NewReadWriter(
		bufio.NewReader(strings.NewReader("buffered client data")),
		bufio.NewWriter(io.Discard),
	)
	hijacked := &HijackedConn{
		Conn:       conn,
		ReadWriter: buffered,
	}
	got := make([]byte, len("buffered client data"))

	n, err := hijacked.Read(got)
	if err != nil {
		t.Fatalf("reading hijacked connection: %v", err)
	}
	if n != len(got) {
		t.Fatalf("\nwanted byte count:\n%d\ngot:\n%d", len(got), n)
	}
	if string(got) != "buffered client data" {
		t.Fatalf("\nwanted data:\n%q\ngot:\n%q", "buffered client data", got)
	}
}

func TestHijackedConn_WritesAndFlushesBufferedOutput(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	var output bytes.Buffer
	buffered := bufio.NewReadWriter(
		bufio.NewReader(bytes.NewReader(nil)),
		bufio.NewWriter(&output),
	)
	hijacked := &HijackedConn{
		Conn:       conn,
		ReadWriter: buffered,
	}
	payload := []byte("websocket frame")

	n, err := hijacked.Write(payload)
	if err != nil {
		t.Fatalf("writing hijacked connection: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("\nwanted byte count:\n%d\ngot:\n%d", len(payload), n)
	}
	if !bytes.Equal(output.Bytes(), payload) {
		t.Fatalf("\nwanted output:\n%q\ngot:\n%q", payload, output.Bytes())
	}
}

func TestConnection_NewConnection(t *testing.T) {
	connectionID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating connection UUID: %v", err)
	}
	requestID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating request UUID: %v", err)
	}

	clientReader := bytes.NewReader([]byte("client"))
	client := &testReadWriteCloser{}
	upstream := &testReadWriteCloser{}
	process := func(*Message) error { return nil }

	got := NewConnection(
		connectionID,
		requestID,
		clientReader,
		client,
		upstream,
		process,
	)

	if got.ID != connectionID {
		t.Fatalf("\nwanted connection ID:\n%s\ngot:\n%s", connectionID, got.ID)
	}
	if got.RequestID != requestID {
		t.Fatalf("\nwanted request ID:\n%s\ngot:\n%s", requestID, got.RequestID)
	}
	if got.clientReader != clientReader {
		t.Fatalf("\nwanted client reader:\n%p\ngot:\n%p", clientReader, got.clientReader)
	}
	if got.client != client {
		t.Fatalf("\nwanted client connection:\n%p\ngot:\n%p", client, got.client)
	}
	if got.upstream != upstream {
		t.Fatalf("\nwanted upstream connection:\n%p\ngot:\n%p", upstream, got.upstream)
	}
	if got.process == nil {
		t.Fatalf("\nwanted process function:\n%v\ngot:\n%v", "initialized", nil)
	}
	if got.done == nil {
		t.Fatalf("\nwanted done channel:\n%v\ngot:\n%v", "initialized", nil)
	}

	select {
	case <-got.done:
		t.Fatalf("\nwanted done channel state:\n%q\ngot:\n%q", "open", "closed")
	default:
	}
}

func TestConnection_WriteFrameToDestination(t *testing.T) {
	tests := []struct {
		name       string
		write      func(*Connection, Frame) error
		output     func(*testReadWriteCloser, *testReadWriteCloser) []byte
		wantMasked bool
	}{
		{
			name: "client output is unmasked",
			write: func(connection *Connection, frame Frame) error {
				return connection.writeToClient(frame)
			},
			output: func(client, _ *testReadWriteCloser) []byte {
				return client.Bytes()
			},
			wantMasked: false,
		},
		{
			name: "upstream output is masked",
			write: func(connection *Connection, frame Frame) error {
				return connection.writeToUpstream(frame)
			},
			output: func(_, upstream *testReadWriteCloser) []byte {
				return upstream.Bytes()
			},
			wantMasked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &testReadWriteCloser{}
			upstream := &testReadWriteCloser{}
			connection := NewConnection(
				uuid.Nil,
				uuid.Nil,
				client,
				client,
				upstream,
				nil,
			)
			frame := Frame{
				Fin:     true,
				Opcode:  OpText,
				Payload: []byte("Hello"),
			}

			if err := tt.write(connection, frame); err != nil {
				t.Fatalf("writing frame returned unexpected error: %v", err)
			}

			got, err := ReadFrame(bytes.NewReader(tt.output(client, upstream)))
			if err != nil {
				t.Fatalf("ReadFrame() returned unexpected error: %v", err)
			}
			if !got.Fin {
				t.Fatalf("\nwanted Fin:\n%v\ngot:\n%v", true, got.Fin)
			}
			if got.Opcode != OpText {
				t.Fatalf("\nwanted Opcode:\n%d\ngot:\n%d", OpText, got.Opcode)
			}
			if got.Masked != tt.wantMasked {
				t.Fatalf("\nwanted Masked:\n%v\ngot:\n%v", tt.wantMasked, got.Masked)
			}
			if !bytes.Equal(got.Payload, frame.Payload) {
				t.Fatalf("\nwanted payload:\n%q\ngot:\n%q", frame.Payload, got.Payload)
			}
		})
	}
}

func TestConnection_WriteFrameError(t *testing.T) {
	writeErr := errors.New("write failed")

	tests := []struct {
		name    string
		connect func() *Connection
		write   func(*Connection, Frame) error
		wantErr string
	}{
		{
			name: "client write fails",
			connect: func() *Connection {
				client := &failingReadWriteCloser{err: writeErr}
				return NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, nil)
			},
			write: func(connection *Connection, frame Frame) error {
				return connection.writeToClient(frame)
			},
			wantErr: "writing websocket frame to client",
		},
		{
			name: "upstream write fails",
			connect: func() *Connection {
				client := &testReadWriteCloser{}
				upstream := &failingReadWriteCloser{err: writeErr}
				return NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)
			},
			write: func(connection *Connection, frame Frame) error {
				return connection.writeToUpstream(frame)
			},
			wantErr: "writing websocket frame upstream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.write(tt.connect(), Frame{Fin: true, Opcode: OpText})
			if err == nil {
				t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", tt.wantErr, err)
			}
			if !errors.Is(err, writeErr) {
				t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", writeErr, err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", tt.wantErr, err)
			}
		})
	}
}

func TestConnection_RelayFrames(t *testing.T) {
	frames := []Frame{
		{
			Fin:     true,
			Opcode:  OpText,
			Payload: []byte("first"),
		},
		{
			Fin:     true,
			Opcode:  OpBinary,
			Payload: []byte{1, 2, 3},
		},
		{
			Fin:    true,
			Opcode: OpClose,
		},
		{
			Fin:     true,
			Opcode:  OpText,
			Payload: []byte("after close"),
		},
	}
	source := bytes.NewReader(encodeTestFrames(t, frames...))
	client := &testReadWriteCloser{}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, nil)

	var got []Frame
	err := connection.relayFrames(source, func(frame Frame) error {
		got = append(got, frame)
		return nil
	}, DirectionFromClient)
	if err != nil {
		t.Fatalf("relayFrames() returned unexpected error: %v", err)
	}

	want := frames[:3]
	if len(got) != len(want) {
		t.Fatalf("\nwanted relayed frame count:\n%d\ngot:\n%d", len(want), len(got))
	}
	for i := range want {
		if got[i].Fin != want[i].Fin {
			t.Fatalf("\nframe:\n%d\nwanted Fin:\n%v\ngot:\n%v", i, want[i].Fin, got[i].Fin)
		}
		if got[i].Opcode != want[i].Opcode {
			t.Fatalf("\nframe:\n%d\nwanted Opcode:\n%d\ngot:\n%d", i, want[i].Opcode, got[i].Opcode)
		}
		if !bytes.Equal(got[i].Payload, want[i].Payload) {
			t.Fatalf("\nframe:\n%d\nwanted payload:\n%q\ngot:\n%q", i, want[i].Payload, got[i].Payload)
		}
	}
}

func TestConnection_RelayFramesProcessesSource(t *testing.T) {
	connectionID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating connection UUID: %v", err)
	}

	tests := []struct {
		name   string
		source string
	}{
		{name: "frame from client", source: DirectionFromClient},
		{name: "frame from server", source: DirectionFromServer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *Message
			process := func(message *Message) error {
				got = message
				return nil
			}
			client := &testReadWriteCloser{}
			connection := NewConnection(
				connectionID,
				uuid.Nil,
				client,
				client,
				&testReadWriteCloser{},
				process,
			)
			source := bytes.NewReader(encodeTestFrames(t, Frame{Fin: true, Opcode: OpClose}))

			err := connection.relayFrames(source, func(Frame) error {
				return nil
			}, tt.source)
			if err != nil {
				t.Fatalf("relayFrames() returned unexpected error: %v", err)
			}
			if got == nil {
				t.Fatalf("\nwanted processed message:\n%v\ngot:\n%v", "initialized", got)
			}
			if got.ConnectionID != connectionID {
				t.Fatalf("\nwanted connection ID:\n%s\ngot:\n%s", connectionID, got.ConnectionID)
			}
			if got.Direction != tt.source {
				t.Fatalf("\nwanted direction:\n%q\ngot:\n%q", tt.source, got.Direction)
			}
			if got.Frame.Opcode != OpClose {
				t.Fatalf("\nwanted Opcode:\n%d\ngot:\n%d", OpClose, got.Frame.Opcode)
			}
		})
	}
}

func TestConnection_CloseDetails(t *testing.T) {
	normalPayload, err := EncodeClosePayload(1000, "Goodbye")
	if err != nil {
		t.Fatalf("EncodeClosePayload() returned unexpected error: %v", err)
	}
	modifiedPayload, err := EncodeClosePayload(1001, "Modified")
	if err != nil {
		t.Fatalf("EncodeClosePayload() returned unexpected error: %v", err)
	}

	tests := []struct {
		name       string
		payload    []byte
		process    ProcessFunc
		wantCode   int
		wantReason string
		wantWrite  bool
		wantErr    string
	}{
		{
			name:       "normal close",
			payload:    normalPayload,
			wantCode:   1000,
			wantReason: "Goodbye",
			wantWrite:  true,
		},
		{
			name:      "close without status",
			wantCode:  CloseNoStatusReceived,
			wantWrite: true,
		},
		{
			name:    "modified close",
			payload: normalPayload,
			process: func(message *Message) error {
				message.Frame.Payload = modifiedPayload
				return nil
			},
			wantCode:   1001,
			wantReason: "Modified",
			wantWrite:  true,
		},
		{
			name:    "dropped close",
			payload: normalPayload,
			process: func(message *Message) error {
				message.Dropped = true
				return nil
			},
			wantCode:   1000,
			wantReason: "Goodbye",
			wantWrite:  false,
		},
		{
			name:      "malformed close",
			payload:   []byte{0x03},
			wantWrite: false,
			wantErr:   "parsing websocket close payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &testReadWriteCloser{}
			connection := NewConnection(
				uuid.Nil,
				uuid.Nil,
				client,
				client,
				&testReadWriteCloser{},
				tt.process,
			)
			source := bytes.NewReader(encodeTestFrames(t, Frame{
				Fin:     true,
				Opcode:  OpClose,
				Payload: tt.payload,
			}))

			writeCalled := false
			relayErr := connection.relayFrames(source, func(Frame) error {
				writeCalled = true
				return nil
			}, DirectionFromClient)

			if tt.wantErr != "" {
				if relayErr == nil {
					t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", tt.wantErr, relayErr)
				}
				if !strings.Contains(relayErr.Error(), tt.wantErr) {
					t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", tt.wantErr, relayErr)
				}
			} else if relayErr != nil {
				t.Fatalf("relayFrames() returned unexpected error: %v", relayErr)
			}

			code, reason := connection.CloseDetails()
			if code != tt.wantCode {
				t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", tt.wantCode, code)
			}
			if reason != tt.wantReason {
				t.Fatalf("\nwanted close reason:\n%q\ngot:\n%q", tt.wantReason, reason)
			}
			if writeCalled != tt.wantWrite {
				t.Fatalf("\nwanted write called:\n%v\ngot:\n%v", tt.wantWrite, writeCalled)
			}
		})
	}
}

func TestConnection_RelayFramesForwardsModification(t *testing.T) {
	process := func(message *Message) error {
		if message.Frame.Opcode == OpText {
			message.Frame.Opcode = OpBinary
			message.Frame.Payload = []byte("modified")
		}
		return nil
	}
	client := &testReadWriteCloser{}
	connection := NewConnection(
		uuid.Nil,
		uuid.Nil,
		client,
		client,
		&testReadWriteCloser{},
		process,
	)
	source := bytes.NewReader(encodeTestFrames(t,
		Frame{Fin: true, Opcode: OpText, Payload: []byte("original")},
		Frame{Fin: true, Opcode: OpClose},
	))

	var got []Frame
	err := connection.relayFrames(source, func(frame Frame) error {
		got = append(got, frame)
		return nil
	}, DirectionFromClient)
	if err != nil {
		t.Fatalf("relayFrames() returned unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("\nwanted relayed frame count:\n%d\ngot:\n%d", 2, len(got))
	}
	if got[0].Opcode != OpBinary {
		t.Fatalf("\nwanted modified Opcode:\n%d\ngot:\n%d", OpBinary, got[0].Opcode)
	}
	if !bytes.Equal(got[0].Payload, []byte("modified")) {
		t.Fatalf("\nwanted modified payload:\n%q\ngot:\n%q", "modified", got[0].Payload)
	}
}

func TestConnection_RelayFramesDropsMessage(t *testing.T) {
	process := func(message *Message) error {
		if message.Frame.Opcode == OpText {
			message.Dropped = true
		}
		return nil
	}
	client := &testReadWriteCloser{}
	connection := NewConnection(
		uuid.Nil,
		uuid.Nil,
		client,
		client,
		&testReadWriteCloser{},
		process,
	)
	source := bytes.NewReader(encodeTestFrames(t,
		Frame{Fin: true, Opcode: OpText, Payload: []byte("drop me")},
		Frame{Fin: true, Opcode: OpClose},
	))

	var got []Frame
	err := connection.relayFrames(source, func(frame Frame) error {
		got = append(got, frame)
		return nil
	}, DirectionFromClient)
	if err != nil {
		t.Fatalf("relayFrames() returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("\nwanted relayed frame count:\n%d\ngot:\n%d", 1, len(got))
	}
	if got[0].Opcode != OpClose {
		t.Fatalf("\nwanted Opcode:\n%d\ngot:\n%d", OpClose, got[0].Opcode)
	}
}

func TestConnection_RelayFramesProcessError(t *testing.T) {
	processErr := errors.New("processing failed")
	process := func(*Message) error {
		return processErr
	}
	client := &testReadWriteCloser{}
	connection := NewConnection(
		uuid.Nil,
		uuid.Nil,
		client,
		client,
		&testReadWriteCloser{},
		process,
	)
	source := bytes.NewReader(encodeTestFrames(t, Frame{
		Fin:     true,
		Opcode:  OpText,
		Payload: []byte("Hello"),
	}))

	writeCalled := false
	err := connection.relayFrames(source, func(Frame) error {
		writeCalled = true
		return nil
	}, DirectionFromClient)
	if err == nil {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "processing websocket message", err)
	}
	if !errors.Is(err, processErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", processErr, err)
	}
	if !strings.Contains(err.Error(), "processing websocket message") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "processing websocket message", err)
	}
	if writeCalled {
		t.Fatalf("\nwanted write called:\n%v\ngot:\n%v", false, writeCalled)
	}
}

func TestConnection_RelayFramesReadError(t *testing.T) {
	client := &testReadWriteCloser{}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, nil)

	err := connection.relayFrames(bytes.NewReader([]byte{0x81}), func(Frame) error {
		return nil
	}, DirectionFromClient)
	if err == nil {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "reading websocket frame", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", io.ErrUnexpectedEOF, err)
	}
	if !strings.Contains(err.Error(), "reading websocket frame") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "reading websocket frame", err)
	}
}

func TestConnection_RelayFramesWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	source := bytes.NewReader(encodeTestFrames(t, Frame{
		Fin:     true,
		Opcode:  OpText,
		Payload: []byte("Hello"),
	}))
	client := &testReadWriteCloser{}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, nil)

	err := connection.relayFrames(source, func(Frame) error {
		return writeErr
	}, DirectionFromClient)
	if err == nil {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "forwarding websocket frame", err)
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", writeErr, err)
	}
	if !strings.Contains(err.Error(), "forwarding websocket frame") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "forwarding websocket frame", err)
	}
}

func TestConnection_RelayFramesStopsWhenDone(t *testing.T) {
	client := &testReadWriteCloser{}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, nil)
	close(connection.done)

	writeCalled := false
	err := connection.relayFrames(bytes.NewReader([]byte{0x81}), func(Frame) error {
		writeCalled = true
		return nil
	}, DirectionFromClient)
	if err != nil {
		t.Fatalf("relayFrames() returned unexpected error: %v", err)
	}
	if writeCalled {
		t.Fatalf("\nwanted write called:\n%v\ngot:\n%v", false, writeCalled)
	}
}

func TestConnection_SendFrame(t *testing.T) {
	tests := []struct {
		name          string
		send          func(*Connection, Frame) error
		output        func(*testReadWriteCloser, *testReadWriteCloser) []byte
		wantDirection string
		wantMasked    bool
	}{
		{
			name: "send to client",
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToClient(frame)
			},
			output: func(client, _ *testReadWriteCloser) []byte {
				return client.Bytes()
			},
			wantDirection: DirectionFromServer,
			wantMasked:    false,
		},
		{
			name: "send upstream",
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToUpstream(frame)
			},
			output: func(_, upstream *testReadWriteCloser) []byte {
				return upstream.Bytes()
			},
			wantDirection: DirectionFromClient,
			wantMasked:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotDirection string
			process := func(message *Message) error {
				gotDirection = message.Direction
				message.Frame.Opcode = OpBinary
				message.Frame.Payload = []byte("modified")
				return nil
			}
			client := &testReadWriteCloser{}
			upstream := &testReadWriteCloser{}
			connection := NewConnection(
				uuid.Nil,
				uuid.Nil,
				client,
				client,
				upstream,
				process,
			)

			err := tt.send(connection, Frame{
				Fin:     true,
				Opcode:  OpText,
				Payload: []byte("original"),
			})
			if err != nil {
				t.Fatalf("sending frame returned unexpected error: %v", err)
			}

			got, err := ReadFrame(bytes.NewReader(tt.output(client, upstream)))
			if err != nil {
				t.Fatalf("ReadFrame() returned unexpected error: %v", err)
			}
			if gotDirection != tt.wantDirection {
				t.Fatalf("\nwanted direction:\n%q\ngot:\n%q", tt.wantDirection, gotDirection)
			}
			if got.Masked != tt.wantMasked {
				t.Fatalf("\nwanted Masked:\n%v\ngot:\n%v", tt.wantMasked, got.Masked)
			}
			if got.Opcode != OpBinary {
				t.Fatalf("\nwanted Opcode:\n%d\ngot:\n%d", OpBinary, got.Opcode)
			}
			if !bytes.Equal(got.Payload, []byte("modified")) {
				t.Fatalf("\nwanted payload:\n%q\ngot:\n%q", "modified", got.Payload)
			}
		})
	}
}

func TestConnection_SendFrameDropped(t *testing.T) {
	tests := []struct {
		name   string
		send   func(*Connection, Frame) error
		output func(*testReadWriteCloser, *testReadWriteCloser) []byte
	}{
		{
			name: "drop frame to client",
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToClient(frame)
			},
			output: func(client, _ *testReadWriteCloser) []byte {
				return client.Bytes()
			},
		},
		{
			name: "drop frame upstream",
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToUpstream(frame)
			},
			output: func(_, upstream *testReadWriteCloser) []byte {
				return upstream.Bytes()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &testReadWriteCloser{}
			upstream := &testReadWriteCloser{}
			connection := NewConnection(
				uuid.Nil,
				uuid.Nil,
				client,
				client,
				upstream,
				func(message *Message) error {
					message.Dropped = true
					return nil
				},
			)

			if err := tt.send(connection, Frame{Fin: true, Opcode: OpText}); err != nil {
				t.Fatalf("sending frame returned unexpected error: %v", err)
			}
			if got := len(tt.output(client, upstream)); got != 0 {
				t.Fatalf("\nwanted written bytes:\n%d\ngot:\n%d", 0, got)
			}
		})
	}
}

func TestConnection_SendFrameError(t *testing.T) {
	processErr := errors.New("processing failed")
	writeErr := errors.New("write failed")

	tests := []struct {
		name        string
		connect     func() *Connection
		send        func(*Connection, Frame) error
		wantErr     error
		wantMessage string
	}{
		{
			name: "processing frame to client fails",
			connect: func() *Connection {
				client := &testReadWriteCloser{}
				return NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, func(*Message) error {
					return processErr
				})
			},
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToClient(frame)
			},
			wantErr:     processErr,
			wantMessage: "sending websocket frame to client",
		},
		{
			name: "processing frame upstream fails",
			connect: func() *Connection {
				client := &testReadWriteCloser{}
				return NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, func(*Message) error {
					return processErr
				})
			},
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToUpstream(frame)
			},
			wantErr:     processErr,
			wantMessage: "sending websocket frame upstream",
		},
		{
			name: "writing frame to client fails",
			connect: func() *Connection {
				client := &failingReadWriteCloser{err: writeErr}
				return NewConnection(uuid.Nil, uuid.Nil, client, client, &testReadWriteCloser{}, nil)
			},
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToClient(frame)
			},
			wantErr:     writeErr,
			wantMessage: "sending websocket frame to client",
		},
		{
			name: "writing frame upstream fails",
			connect: func() *Connection {
				client := &testReadWriteCloser{}
				upstream := &failingReadWriteCloser{err: writeErr}
				return NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)
			},
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToUpstream(frame)
			},
			wantErr:     writeErr,
			wantMessage: "sending websocket frame upstream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.send(tt.connect(), Frame{Fin: true, Opcode: OpText})
			if err == nil {
				t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", tt.wantMessage, err)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", tt.wantErr, err)
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", tt.wantMessage, err)
			}
		})
	}
}

func TestConnection_SendFrameAfterShutdown(t *testing.T) {
	tests := []struct {
		name string
		send func(*Connection, Frame) error
	}{
		{
			name: "send to client",
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToClient(frame)
			},
		},
		{
			name: "send upstream",
			send: func(connection *Connection, frame Frame) error {
				return connection.SendToUpstream(frame)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &testReadWriteCloser{}
			upstream := &testReadWriteCloser{}
			connection := NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)
			if err := connection.shutdown(); err != nil {
				t.Fatalf("shutdown() returned unexpected error: %v", err)
			}

			err := tt.send(connection, Frame{Fin: true, Opcode: OpText})
			if !errors.Is(err, ErrConnectionClosed) {
				t.Fatalf("\nwanted error:\n%v\ngot:\n%v", ErrConnectionClosed, err)
			}
			if got := client.Len() + upstream.Len(); got != 0 {
				t.Fatalf("\nwanted written bytes:\n%d\ngot:\n%d", 0, got)
			}
		})
	}
}

func TestConnection_Close(t *testing.T) {
	clientConnection, clientPeer := net.Pipe()
	upstreamConnection, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()

	deadline := time.Now().Add(5 * time.Second)
	if err := clientPeer.SetDeadline(deadline); err != nil {
		t.Fatalf("setting client peer deadline: %v", err)
	}
	if err := upstreamPeer.SetDeadline(deadline); err != nil {
		t.Fatalf("setting upstream peer deadline: %v", err)
	}

	processed := make([]*Message, 0, 2)
	connection := NewConnection(
		uuid.Nil,
		uuid.Nil,
		clientConnection,
		clientConnection,
		upstreamConnection,
		func(message *Message) error {
			processed = append(processed, message)
			return nil
		},
	)
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- connection.Close(1000, "Goodbye")
	}()

	clientFrame, err := ReadFrame(clientPeer)
	if err != nil {
		t.Fatalf("reading close frame from client peer: %v", err)
	}
	if clientFrame.Masked {
		t.Fatalf("\nwanted client close frame Masked:\n%v\ngot:\n%v", false, clientFrame.Masked)
	}
	if clientFrame.Opcode != OpClose {
		t.Fatalf("\nwanted client close frame Opcode:\n%d\ngot:\n%d", OpClose, clientFrame.Opcode)
	}
	clientCode, clientReason, err := ParseClosePayload(clientFrame.Payload)
	if err != nil {
		t.Fatalf("ParseClosePayload() returned unexpected error: %v", err)
	}
	if clientCode != 1000 {
		t.Fatalf("\nwanted client close code:\n%d\ngot:\n%d", 1000, clientCode)
	}
	if clientReason != "Goodbye" {
		t.Fatalf("\nwanted client close reason:\n%q\ngot:\n%q", "Goodbye", clientReason)
	}

	upstreamFrame, err := ReadFrame(upstreamPeer)
	if err != nil {
		t.Fatalf("reading close frame from upstream peer: %v", err)
	}
	if !upstreamFrame.Masked {
		t.Fatalf("\nwanted upstream close frame Masked:\n%v\ngot:\n%v", true, upstreamFrame.Masked)
	}
	if upstreamFrame.Opcode != OpClose {
		t.Fatalf("\nwanted upstream close frame Opcode:\n%d\ngot:\n%d", OpClose, upstreamFrame.Opcode)
	}
	upstreamCode, upstreamReason, err := ParseClosePayload(upstreamFrame.Payload)
	if err != nil {
		t.Fatalf("ParseClosePayload() returned unexpected error: %v", err)
	}
	if upstreamCode != 1000 {
		t.Fatalf("\nwanted upstream close code:\n%d\ngot:\n%d", 1000, upstreamCode)
	}
	if upstreamReason != "Goodbye" {
		t.Fatalf("\nwanted upstream close reason:\n%q\ngot:\n%q", "Goodbye", upstreamReason)
	}

	select {
	case closeErr := <-closeResult:
		if closeErr != nil {
			t.Fatalf("Close() returned unexpected error: %v", closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("\nwanted Close state:\n%q\ngot:\n%q", "returned", "blocked")
	}

	code, reason := connection.CloseDetails()
	if code != 1000 {
		t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", 1000, code)
	}
	if reason != "Goodbye" {
		t.Fatalf("\nwanted close reason:\n%q\ngot:\n%q", "Goodbye", reason)
	}
	if len(processed) != 1 {
		t.Fatalf("\nwanted processed close message count:\n%d\ngot:\n%d", 1, len(processed))
	}
	message := processed[0]
	if message.Direction != DirectionFromClient {
		t.Fatalf("\nwanted direction:\n%q\ngot:\n%q", DirectionFromClient, message.Direction)
	}
	if message.Frame.Opcode != OpClose {
		t.Fatalf("\nwanted opcode:\n%d\ngot:\n%d", OpClose, message.Frame.Opcode)
	}
	if generated, _ := message.Metadata["generated"].(bool); !generated {
		t.Fatalf("\nwanted generated metadata:\n%v\ngot:\n%v", true, message.Metadata["generated"])
	}
	select {
	case <-connection.done:
	default:
		t.Fatalf("\nwanted done channel state:\n%q\ngot:\n%q", "closed", "open")
	}
}

func TestConnection_RelayFramesSeparatesLocalCloseHandshakes(t *testing.T) {
	tests := []struct {
		name          string
		direction     string
		wantProcessed bool
	}{
		{
			name:      "downstream acknowledgment is internal",
			direction: DirectionFromClient,
		},
		{
			name:          "upstream acknowledgment is captured",
			direction:     DirectionFromServer,
			wantProcessed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processed := false
			written := false
			client := &testReadWriteCloser{}
			connection := NewConnection(
				uuid.New(),
				uuid.New(),
				client,
				client,
				&testReadWriteCloser{},
				func(*Message) error {
					processed = true
					return nil
				},
			)
			reader := &closeStartingReader{
				reader:     bytes.NewReader(encodeTestFrames(t, Frame{Fin: true, Opcode: OpClose})),
				connection: connection,
			}

			err := connection.relayFrames(reader, func(Frame) error {
				written = true
				return nil
			}, tt.direction)
			if err != nil {
				t.Fatalf("relayFrames() returned unexpected error: %v", err)
			}
			if processed != tt.wantProcessed {
				t.Fatalf("\nwanted close processed:\n%v\ngot:\n%v", tt.wantProcessed, processed)
			}
			if written {
				t.Fatalf("\nwanted close forwarded:\n%v\ngot:\n%v", false, written)
			}
		})
	}
}

func TestConnection_CloseInvalidPayload(t *testing.T) {
	client := &testReadWriteCloser{}
	upstream := &testReadWriteCloser{}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)

	err := connection.Close(CloseNoStatusReceived, "")
	if err == nil {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%v", "encoding websocket close payload", err)
	}
	if !strings.Contains(err.Error(), "encoding websocket close payload") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "encoding websocket close payload", err)
	}
	if got := client.Len() + upstream.Len(); got != 0 {
		t.Fatalf("\nwanted written bytes:\n%d\ngot:\n%d", 0, got)
	}
	if client.closed || upstream.closed {
		t.Fatalf("\nwanted connections closed:\n%v\ngot:\n%v", false, client.closed || upstream.closed)
	}
	select {
	case <-connection.done:
		t.Fatalf("\nwanted done channel state:\n%q\ngot:\n%q", "open", "closed")
	default:
	}
}

func TestConnection_CloseAfterShutdown(t *testing.T) {
	client := &testReadWriteCloser{}
	upstream := &testReadWriteCloser{}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)
	if err := connection.shutdown(); err != nil {
		t.Fatalf("shutdown() returned unexpected error: %v", err)
	}

	err := connection.Close(1000, "Goodbye")
	if !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("\nwanted error:\n%v\ngot:\n%v", ErrConnectionClosed, err)
	}
	if got := client.Len() + upstream.Len(); got != 0 {
		t.Fatalf("\nwanted written bytes:\n%d\ngot:\n%d", 0, got)
	}
}

func TestConnection_CloseErrors(t *testing.T) {
	clientWriteErr := errors.New("client write failed")
	upstreamWriteErr := errors.New("upstream write failed")
	clientCloseErr := errors.New("client close failed")
	upstreamCloseErr := errors.New("upstream close failed")
	client := &failingReadWriteCloser{
		err:      clientWriteErr,
		closeErr: clientCloseErr,
	}
	upstream := &failingReadWriteCloser{
		err:      upstreamWriteErr,
		closeErr: upstreamCloseErr,
	}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)

	err := connection.Close(1000, "Goodbye")
	if err == nil {
		t.Fatalf(
			"\nwanted errors:\n%v\ngot:\n%v",
			errors.Join(clientWriteErr, upstreamWriteErr, clientCloseErr, upstreamCloseErr),
			err,
		)
	}
	for _, wantErr := range []error{
		clientWriteErr,
		upstreamWriteErr,
		clientCloseErr,
		upstreamCloseErr,
	} {
		if !errors.Is(err, wantErr) {
			t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", wantErr, err)
		}
	}

	code, reason := connection.CloseDetails()
	if code != 1000 {
		t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", 1000, code)
	}
	if reason != "Goodbye" {
		t.Fatalf("\nwanted close reason:\n%q\ngot:\n%q", "Goodbye", reason)
	}
	select {
	case <-connection.done:
	default:
		t.Fatalf("\nwanted done channel state:\n%q\ngot:\n%q", "closed", "open")
	}
}

func TestConnection_Shutdown(t *testing.T) {
	client := &testReadWriteCloser{}
	upstream := &testReadWriteCloser{}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)

	if err := connection.shutdown(); err != nil {
		t.Fatalf("shutdown() returned unexpected error: %v", err)
	}

	if !client.closed {
		t.Fatalf("\nwanted client closed:\n%v\ngot:\n%v", true, client.closed)
	}
	if !upstream.closed {
		t.Fatalf("\nwanted upstream closed:\n%v\ngot:\n%v", true, upstream.closed)
	}
	if client.closeCount != 1 {
		t.Fatalf("\nwanted client close count:\n%d\ngot:\n%d", 1, client.closeCount)
	}
	if upstream.closeCount != 1 {
		t.Fatalf("\nwanted upstream close count:\n%d\ngot:\n%d", 1, upstream.closeCount)
	}

	select {
	case <-connection.done:
	default:
		t.Fatalf("\nwanted done channel state:\n%q\ngot:\n%q", "closed", "open")
	}

	if err := connection.shutdown(); err != nil {
		t.Fatalf("second shutdown() returned unexpected error: %v", err)
	}
	if client.closeCount != 1 {
		t.Fatalf("\nwanted client close count:\n%d\ngot:\n%d", 1, client.closeCount)
	}
	if upstream.closeCount != 1 {
		t.Fatalf("\nwanted upstream close count:\n%d\ngot:\n%d", 1, upstream.closeCount)
	}
}

func TestConnection_ShutdownErrors(t *testing.T) {
	clientErr := errors.New("client close failed")
	upstreamErr := errors.New("upstream close failed")
	client := &testReadWriteCloser{closeErr: clientErr}
	upstream := &testReadWriteCloser{closeErr: upstreamErr}
	connection := NewConnection(uuid.Nil, uuid.Nil, client, client, upstream, nil)

	err := connection.shutdown()
	if err == nil {
		t.Fatalf("\nwanted close errors:\n%v\ngot:\n%v", errors.Join(clientErr, upstreamErr), err)
	}
	if !errors.Is(err, clientErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", clientErr, err)
	}
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", upstreamErr, err)
	}

	secondErr := connection.shutdown()
	if !errors.Is(secondErr, clientErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", clientErr, secondErr)
	}
	if !errors.Is(secondErr, upstreamErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", upstreamErr, secondErr)
	}
	if client.closeCount != 1 {
		t.Fatalf("\nwanted client close count:\n%d\ngot:\n%d", 1, client.closeCount)
	}
	if upstream.closeCount != 1 {
		t.Fatalf("\nwanted upstream close count:\n%d\ngot:\n%d", 1, upstream.closeCount)
	}
}

func TestConnection_Run(t *testing.T) {
	clientConnection, clientPeer := net.Pipe()
	upstreamConnection, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()

	deadline := time.Now().Add(5 * time.Second)
	if err := clientPeer.SetDeadline(deadline); err != nil {
		t.Fatalf("setting client peer deadline: %v", err)
	}
	if err := upstreamPeer.SetDeadline(deadline); err != nil {
		t.Fatalf("setting upstream peer deadline: %v", err)
	}

	connection := NewConnection(
		uuid.Nil,
		uuid.Nil,
		clientConnection,
		clientConnection,
		upstreamConnection,
		nil,
	)
	runResult := make(chan error, 1)
	go func() {
		runResult <- connection.Run()
	}()

	clientFrame := Frame{
		Fin:     true,
		Opcode:  OpText,
		Payload: []byte("from client"),
	}
	if err := WriteFrame(clientPeer, clientFrame, true); err != nil {
		t.Fatalf("writing client frame: %v", err)
	}

	gotUpstream, err := ReadFrame(upstreamPeer)
	if err != nil {
		t.Fatalf("reading frame from upstream peer: %v", err)
	}
	if !gotUpstream.Masked {
		t.Fatalf("\nwanted upstream frame Masked:\n%v\ngot:\n%v", true, gotUpstream.Masked)
	}
	if gotUpstream.Opcode != clientFrame.Opcode {
		t.Fatalf("\nwanted upstream frame Opcode:\n%d\ngot:\n%d", clientFrame.Opcode, gotUpstream.Opcode)
	}
	if !bytes.Equal(gotUpstream.Payload, clientFrame.Payload) {
		t.Fatalf("\nwanted upstream frame payload:\n%q\ngot:\n%q", clientFrame.Payload, gotUpstream.Payload)
	}

	serverFrame := Frame{
		Fin:     true,
		Opcode:  OpBinary,
		Payload: []byte{1, 2, 3},
	}
	if writeErr := WriteFrame(upstreamPeer, serverFrame, false); writeErr != nil {
		t.Fatalf("writing server frame: %v", writeErr)
	}

	gotClient, err := ReadFrame(clientPeer)
	if err != nil {
		t.Fatalf("reading frame from client peer: %v", err)
	}
	if gotClient.Masked {
		t.Fatalf("\nwanted client frame Masked:\n%v\ngot:\n%v", false, gotClient.Masked)
	}
	if gotClient.Opcode != serverFrame.Opcode {
		t.Fatalf("\nwanted client frame Opcode:\n%d\ngot:\n%d", serverFrame.Opcode, gotClient.Opcode)
	}
	if !bytes.Equal(gotClient.Payload, serverFrame.Payload) {
		t.Fatalf("\nwanted client frame payload:\n%q\ngot:\n%q", serverFrame.Payload, gotClient.Payload)
	}

	closePayload, err := EncodeClosePayload(1000, "")
	if err != nil {
		t.Fatalf("EncodeClosePayload() returned unexpected error: %v", err)
	}
	closeFrame := Frame{
		Fin:     true,
		Opcode:  OpClose,
		Payload: closePayload,
	}
	if writeErr := WriteFrame(clientPeer, closeFrame, true); writeErr != nil {
		t.Fatalf("writing client close frame: %v", writeErr)
	}

	gotClose, err := ReadFrame(upstreamPeer)
	if err != nil {
		t.Fatalf("reading close frame from upstream peer: %v", err)
	}
	if gotClose.Opcode != OpClose {
		t.Fatalf("\nwanted close frame Opcode:\n%d\ngot:\n%d", OpClose, gotClose.Opcode)
	}
	if !gotClose.Masked {
		t.Fatalf("\nwanted close frame Masked:\n%v\ngot:\n%v", true, gotClose.Masked)
	}

	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run() returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("\nwanted Run state:\n%q\ngot:\n%q", "returned", "blocked")
	}

	select {
	case <-connection.done:
	default:
		t.Fatalf("\nwanted done channel state:\n%q\ngot:\n%q", "closed", "open")
	}
}

func TestConnection_RunReturnsFirstAndShutdownErrors(t *testing.T) {
	readErr := errors.New("client read failed")
	closeErr := errors.New("client close failed")
	client := &testReadWriteCloser{closeErr: closeErr}
	upstreamConnection, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()

	connection := NewConnection(
		uuid.Nil,
		uuid.Nil,
		&errorReader{err: readErr},
		client,
		upstreamConnection,
		nil,
	)

	err := connection.Run()
	if err == nil {
		t.Fatalf("\nwanted errors:\n%v\ngot:\n%v", errors.Join(readErr, closeErr), err)
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", readErr, err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("\nwanted wrapped error:\n%v\ngot:\n%v", closeErr, err)
	}
	if client.closeCount != 1 {
		t.Fatalf("\nwanted client close count:\n%d\ngot:\n%d", 1, client.closeCount)
	}

	select {
	case <-connection.done:
	default:
		t.Fatalf("\nwanted done channel state:\n%q\ngot:\n%q", "closed", "open")
	}
}
