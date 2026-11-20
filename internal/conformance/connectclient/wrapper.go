package connectclient

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance/shim"
	"google.golang.org/protobuf/encoding/protowire"
)

// Fields of connectrpc.conformance.v1.ClientCompatRequest and
// ClientCompatResponse the wrapper reads or writes.
const (
	fieldTestName protowire.Number = 1 // both messages
	fieldHost     protowire.Number = 6 // request
	fieldPort     protowire.Number = 7 // request
	fieldError    protowire.Number = 3 // response: ClientErrorResult
	fieldMessage  protowire.Number = 1 // ClientErrorResult
)

// Client is the reference client the wrapper runs: its stdin and stdout,
// and Stop, which ends it.
type Client struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stop   func()
}

// Options are the wrapper's settings.
type Options struct {
	// Proxy is the proxy listener, host:port, the shims CONNECT through;
	// empty in the direct run.
	Proxy string
	// Deadline is how long the wrapper waits for the client's result of a
	// case before it answers the case itself with an error, so the runner
	// never times out and kills the client.
	Deadline time.Duration
	// Parallel, when above zero, is how many unanswered cases the client
	// may have at once. A case the wrapper answered at its deadline no
	// longer counts, though the client may still be waiting on it.
	Parallel int
	// Events, when set, gets one JSON Event per line.
	Events io.Writer
}

// EventKind is what happened to a case in the wrapper.
type EventKind string

// Event kinds.
const (
	EventSent     EventKind = "sent"     // the wrapper gave the case to the client
	EventAnswered EventKind = "answered" // the client's result reached the runner
	EventDeadline EventKind = "deadline" // the wrapper answered the case with an error
)

// Event is one line of the wrapper's events file.
type Event struct {
	Case string    `json:"case"`
	Kind EventKind `json:"kind"`
	At   time.Time `json:"at"`
}

// Wrap is the client under test the runner sees. It reads each
// length-prefixed ClientCompatRequest from in, rewrites only its host and
// port to a shim it starts for that server, which sends the connection
// straight on or with CONNECT through Options.Proxy, and gives it to the
// client. It copies each of the client's results to out unchanged. A case
// with no result within Options.Deadline is answered with an error result
// and its late result dropped. After in ends and every case has an
// answer, it stops the client and returns.
func Wrap(ctx context.Context, in io.Reader, out io.Writer, client Client, options Options) error {
	w := &wrapper{out: out, events: options.Events, pending: map[string]*time.Timer{}, shims: map[string]*shim.Shim{}, proxy: options.Proxy}
	w.idle = sync.NewCond(&w.mu)
	defer w.closeShims()
	defer func() {
		if client.Stop != nil {
			client.Stop()
		}
	}()
	go w.copyResults(client.Stdout)
	stop := context.AfterFunc(ctx, func() {
		w.mu.Lock()
		w.cancelled = true
		w.idle.Broadcast()
		w.mu.Unlock()
	})
	defer stop()

	for {
		msg, err := readMessage(in)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name, rewritten, err := w.rewrite(msg)
		if err != nil {
			return err
		}
		w.mu.Lock()
		for options.Parallel > 0 && len(w.pending) >= options.Parallel && !w.cancelled {
			w.idle.Wait()
		}
		if w.cancelled {
			w.mu.Unlock()
			return ctx.Err()
		}
		w.pending[name] = time.AfterFunc(options.Deadline, func() { w.answerLate(name, options.Deadline) })
		w.event(name, EventSent)
		w.mu.Unlock()
		if _, err := client.Stdin.Write(frame(rewritten)); err != nil {
			return fmt.Errorf("writing to the reference client: %w", err)
		}
	}
	client.Stdin.Close()
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.pending) > 0 && !w.cancelled {
		w.idle.Wait()
	}
	if w.cancelled {
		return ctx.Err()
	}
	return w.err
}

type wrapper struct {
	out    io.Writer
	events io.Writer
	proxy  string

	mu        sync.Mutex
	idle      *sync.Cond // broadcast when pending shrinks or the run is cancelled
	pending   map[string]*time.Timer
	cancelled bool
	err       error // the first error writing to out

	shimsMu sync.Mutex
	shims   map[string]*shim.Shim // by the server's host:port
}

// copyResults copies each result of a pending case to out until the
// client's stdout ends.
func (w *wrapper) copyResults(stdout io.Reader) {
	for {
		msg, err := readMessage(stdout)
		if err != nil {
			return
		}
		name := testName(msg)
		w.mu.Lock()
		if timer, ok := w.pending[name]; ok {
			timer.Stop()
			w.answer(name, msg, EventAnswered)
		}
		w.mu.Unlock()
	}
}

// answerLate answers name with an error result if it is still pending.
func (w *wrapper) answerLate(name string, deadline time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.pending[name]; !ok {
		return
	}
	text := fmt.Sprintf("conformance wrapper: no result from the reference client within %s", deadline)
	result := protowire.AppendTag(nil, fieldMessage, protowire.BytesType)
	result = protowire.AppendString(result, text)
	msg := protowire.AppendTag(nil, fieldTestName, protowire.BytesType)
	msg = protowire.AppendString(msg, name)
	msg = protowire.AppendTag(msg, fieldError, protowire.BytesType)
	msg = protowire.AppendBytes(msg, result)
	w.answer(name, msg, EventDeadline)
}

// answer writes msg to out for pending case name. w.mu is held.
func (w *wrapper) answer(name string, msg []byte, kind EventKind) {
	delete(w.pending, name)
	if _, err := w.out.Write(frame(msg)); err != nil && w.err == nil {
		w.err = fmt.Errorf("writing to the runner: %w", err)
	}
	w.event(name, kind)
	w.idle.Broadcast()
}

// event writes an event. w.mu is held.
func (w *wrapper) event(name string, kind EventKind) {
	if w.events == nil {
		return
	}
	line, _ := json.Marshal(Event{Case: name, Kind: kind, At: time.Now()})
	w.events.Write(append(line, '\n'))
}

// rewrite returns the request's test name and the request with its host
// and port set to the shim for its server, every other field's bytes kept.
func (w *wrapper) rewrite(msg []byte) (string, []byte, error) {
	var name, host string
	var port uint64
	var rest []byte
	for b := msg; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", nil, protowire.ParseError(n)
		}
		m := protowire.ConsumeFieldValue(num, typ, b[n:])
		if m < 0 {
			return "", nil, protowire.ParseError(m)
		}
		switch num {
		case fieldHost:
			host, _ = protowire.ConsumeString(b[n:])
		case fieldPort:
			port, _ = protowire.ConsumeVarint(b[n:])
		default:
			if num == fieldTestName {
				name, _ = protowire.ConsumeString(b[n:])
			}
			rest = append(rest, b[:n+m]...)
		}
		b = b[n+m:]
	}
	shimPort, err := w.shimFor(net.JoinHostPort(host, strconv.FormatUint(port, 10)))
	if err != nil {
		return "", nil, err
	}
	out := protowire.AppendTag(nil, fieldHost, protowire.BytesType)
	out = protowire.AppendString(out, "127.0.0.1")
	out = protowire.AppendTag(out, fieldPort, protowire.VarintType)
	out = protowire.AppendVarint(out, shimPort)
	return name, append(out, rest...), nil
}

// shimFor returns the port of the shim for target, starting it the first time.
func (w *wrapper) shimFor(target string) (uint64, error) {
	w.shimsMu.Lock()
	defer w.shimsMu.Unlock()
	s, ok := w.shims[target]
	if !ok {
		var err error
		if s, err = shim.Start(shim.Options{Target: target, Proxy: w.proxy}); err != nil {
			return 0, err
		}
		w.shims[target] = s
	}
	return strconv.ParseUint(s.Port(), 10, 32)
}

func (w *wrapper) closeShims() {
	w.shimsMu.Lock()
	defer w.shimsMu.Unlock()
	var wg sync.WaitGroup
	for _, s := range w.shims {
		wg.Add(1)
		go func() { defer wg.Done(); s.Close() }()
	}
	wg.Wait()
}

// testName returns a message's field 1, the test name of either message.
func testName(msg []byte) string {
	for b := msg; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return ""
		}
		m := protowire.ConsumeFieldValue(num, typ, b[n:])
		if m < 0 {
			return ""
		}
		if num == fieldTestName {
			name, _ := protowire.ConsumeString(b[n:])
			return name
		}
		b = b[n+m:]
	}
	return ""
}

// readMessage reads one message with a big-endian 32-bit length prefix.
func readMessage(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint32(size[:]))
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, fmt.Errorf("reading a message: %w", err)
	}
	return msg, nil
}

func frame(msg []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(msg))), msg...)
}
