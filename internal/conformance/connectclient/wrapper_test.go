package connectclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// request encodes a ClientCompatRequest with a test name, host, port and
// one more field the wrapper must keep: http_version (2).
func request(name, host string, port uint32) []byte {
	b := protowire.AppendTag(nil, 1, protowire.BytesType)
	b = protowire.AppendString(b, name)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, 2)
	b = protowire.AppendTag(b, 6, protowire.BytesType)
	b = protowire.AppendString(b, host)
	b = protowire.AppendTag(b, 7, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(port))
	return b
}

// response encodes a ClientCompatResponse for name with an empty
// response result (field 2).
func response(name string) []byte {
	b := protowire.AppendTag(nil, 1, protowire.BytesType)
	b = protowire.AppendString(b, name)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	return protowire.AppendBytes(b, nil)
}

func readFrame(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		t.Fatalf("reading a frame: %v", err)
	}
	msg := make([]byte, binary.BigEndian.Uint32(size[:]))
	if _, err := io.ReadFull(r, msg); err != nil {
		t.Fatalf("reading a frame: %v", err)
	}
	return msg
}

// fields decodes a message into its fields by number, keeping each raw value.
func fields(t *testing.T, msg []byte) map[protowire.Number][]byte {
	t.Helper()
	out := map[protowire.Number][]byte{}
	for b := msg; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		m := protowire.ConsumeFieldValue(num, typ, b[n:])
		if n < 0 || m < 0 {
			t.Fatalf("bad message %x", msg)
		}
		out[num] = b[n : n+m]
		b = b[n+m:]
	}
	return out
}

// startFake starts a reference client stand-in that calls answer with
// each decoded request.
func startFake(t *testing.T, answer func(req map[protowire.Number][]byte, write func([]byte))) Client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var mu sync.Mutex
	write := func(msg []byte) {
		mu.Lock()
		defer mu.Unlock()
		outW.Write(frame(msg))
	}
	go func() {
		r := bufio.NewReader(inR)
		for {
			msg, err := readMessage(r)
			if err != nil {
				return
			}
			answer(fields(t, msg), write)
		}
	}()
	t.Cleanup(func() { inR.Close(); outW.Close() })
	return Client{Stdin: inW, Stdout: outR, Stop: func() { outW.Close() }}
}

func events(t *testing.T, data []byte) []Event {
	t.Helper()
	var out []Event
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("decoding event %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestWrap(t *testing.T) {
	t.Run("should send each case through a shim to its server and copy the response back", func(t *testing.T) {
		server, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		go func() {
			for {
				conn, err := server.Accept()
				if err != nil {
					return
				}
				conn.Write([]byte("hello"))
				conn.Close()
			}
		}()
		host, portText, _ := net.SplitHostPort(server.Addr().String())
		port, _ := strconv.Atoi(portText)

		var got map[protowire.Number][]byte
		var greeting []byte
		client := startFake(t, func(req map[protowire.Number][]byte, write func([]byte)) {
			got = req
			h, _ := protowire.ConsumeString(req[6])
			p, _ := protowire.ConsumeVarint(req[7])
			conn, err := net.Dial("tcp", net.JoinHostPort(h, strconv.Itoa(int(p))))
			if err == nil {
				greeting, _ = io.ReadAll(conn)
				conn.Close()
			}
			name, _ := protowire.ConsumeString(req[1])
			write(response(name))
		})

		in := bytes.NewReader(frame(request("case-a", host, uint32(port))))
		var out, log bytes.Buffer
		if err := Wrap(context.Background(), in, &out, client, Options{Deadline: 5 * time.Second, Events: &log}); err != nil {
			t.Fatalf("Wrap: %v", err)
		}

		if string(greeting) != "hello" {
			t.Fatalf("\nwanted the client to reach the server through the shim\ngot greeting %q", greeting)
		}
		if h, _ := protowire.ConsumeString(got[6]); h != "127.0.0.1" {
			t.Fatalf("\nwanted host 127.0.0.1\ngot %q", h)
		}
		if p, _ := protowire.ConsumeVarint(got[7]); int(p) == port {
			t.Fatalf("\nwanted the port rewritten to a shim\ngot the server's port %d", p)
		}
		if v, _ := protowire.ConsumeVarint(got[2]); v != 2 {
			t.Fatalf("\nwanted http_version kept as 2\ngot %d", v)
		}
		if msg := readFrame(t, &out); !bytes.Equal(msg, response("case-a")) {
			t.Fatalf("\nwanted the response copied unchanged\ngot %x", msg)
		}
		want := []Event{{Case: "case-a", Kind: EventSent}, {Case: "case-a", Kind: EventAnswered}}
		if got := events(t, log.Bytes()); !equalEvents(got, want) {
			t.Fatalf("\nwanted events:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should answer a case the client leaves past the deadline and drop its late result", func(t *testing.T) {
		release := make(chan struct{})
		client := startFake(t, func(req map[protowire.Number][]byte, write func([]byte)) {
			name, _ := protowire.ConsumeString(req[1])
			if name == "slow" {
				go func() { <-release; write(response(name)) }()
				return
			}
			write(response(name))
		})
		in := bytes.NewReader(append(frame(request("slow", "127.0.0.1", 1)), frame(request("fast", "127.0.0.1", 1))...))
		var out, log bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- Wrap(context.Background(), in, &out, client, Options{Deadline: 200 * time.Millisecond, Events: &log})
		}()
		time.Sleep(400 * time.Millisecond)
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("Wrap: %v", err)
		}

		first := fields(t, readFrame(t, &out))
		second := fields(t, readFrame(t, &out))
		if name, _ := protowire.ConsumeString(first[1]); name != "fast" {
			t.Fatalf("\nwanted fast answered first\ngot %q", name)
		}
		if name, _ := protowire.ConsumeString(second[1]); name != "slow" {
			t.Fatalf("\nwanted slow answered by the wrapper\ngot %q", name)
		}
		if _, ok := second[3]; !ok {
			t.Fatalf("\nwanted the wrapper's answer to be an error result (field 3)\ngot fields %v", second)
		}
		if out.Len() != 0 {
			t.Fatalf("\nwanted the late result dropped\ngot %d more bytes", out.Len())
		}
		want := []Event{
			{Case: "slow", Kind: EventSent}, {Case: "fast", Kind: EventSent},
			{Case: "fast", Kind: EventAnswered}, {Case: "slow", Kind: EventDeadline},
		}
		if got := events(t, log.Bytes()); !equalEvents(got, want) {
			t.Fatalf("\nwanted events:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should give the client a new case only while fewer than Parallel are unanswered", func(t *testing.T) {
		var mu sync.Mutex
		var seen []string
		writes := map[string]func([]byte){}
		client := startFake(t, func(req map[protowire.Number][]byte, write func([]byte)) {
			name, _ := protowire.ConsumeString(req[1])
			mu.Lock()
			seen = append(seen, name)
			writes[name] = write
			mu.Unlock()
		})
		in := bytes.NewReader(append(frame(request("a", "127.0.0.1", 1)), frame(request("b", "127.0.0.1", 1))...))
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- Wrap(context.Background(), in, &out, client, Options{Deadline: 5 * time.Second, Parallel: 1})
		}()
		snapshot := func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), seen...)
		}
		time.Sleep(200 * time.Millisecond)
		if got := snapshot(); len(got) != 1 || got[0] != "a" {
			t.Fatalf("\nwanted the client to have only a while a is unanswered\ngot %v", got)
		}
		mu.Lock()
		writes["a"](response("a"))
		mu.Unlock()
		time.Sleep(200 * time.Millisecond)
		if got := snapshot(); len(got) != 2 || got[1] != "b" {
			t.Fatalf("\nwanted b once a was answered\ngot %v", got)
		}
		mu.Lock()
		writes["b"](response("b"))
		mu.Unlock()
		if err := <-done; err != nil {
			t.Fatalf("Wrap: %v", err)
		}
	})
}

func equalEvents(a, b []Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Case != b[i].Case || a[i].Kind != b[i].Kind {
			return false
		}
	}
	return true
}
