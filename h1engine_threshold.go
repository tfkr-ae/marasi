package marasi

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// An HTTP/1.1 body read up to the body threshold (body_threshold.go). A
// body is read whole, unless its head declares a length over the
// threshold, the bytes read pass it, or it stalls for bodyStallWait after
// some arrived: then it is h1Passing, and the engine forwards what arrived
// and the rest as it comes, with its framing exactly as on the wire.

// h1Passing is a body forwarded as it arrives.
type h1Passing struct {
	// arrived are the wire bytes read before the body passed.
	arrived []byte
	// declared is set when the head declared a length over the threshold:
	// Marasi then sent no 100 Continue, so the origin's goes to the client.
	declared bool
	// rest gives the body's remaining wire bytes as they arrive, then
	// io.EOF, or the error that cut the body short.
	rest *wirePump
	// sent is closed once a request's rest went to the origin, or failed
	// to; sentAll is set before when all of it went. nil until it goes.
	sent    chan struct{}
	sentAll bool
}

// sendRest forwards the rest of a passing request body to the origin on
// its own goroutine, while the origin's answer is read. A body that does
// not all go closes the origin connection.
func (p *h1Passing) sendRest(origin *originConn) {
	p.sent = make(chan struct{})
	go func() {
		defer close(p.sent)
		if p.sentAll = p.forward(origin.bw); !p.sentAll {
			origin.conn.Close()
		}
	}()
}

// unfinished reports whether the rest of the body was not all read, so the
// connection it comes on cannot carry another message. It stops the read.
func (p *h1Passing) unfinished() bool {
	if p == nil || p.rest.ended.Load() {
		return false
	}
	p.rest.close()
	return true
}

// wirePump reads a body's wire bytes on its own goroutine, so the engine
// can notice the body stall while it waits for them.
type wirePump struct {
	chunks chan []byte
	stop   chan struct{}
	once   sync.Once
	// err is why the read ended, set before chunks closes.
	err  error
	left []byte
	// ended is set once the last of the body was read, before it is
	// handed on, so a peer answering it cannot overtake it.
	ended atomic.Bool
}

func startWirePump(r *h1.WireReader) *wirePump {
	p := &wirePump{chunks: make(chan []byte), stop: make(chan struct{})}
	go func() {
		defer close(p.chunks)
		for {
			buf := make([]byte, bodyChunkSize)
			n, err := r.Read(buf)
			if r.Ended() {
				p.ended.Store(true)
			}
			if n > 0 {
				select {
				case p.chunks <- buf[:n]:
				case <-p.stop:
					p.err = errors.New("the body's read was stopped")
					return
				}
			}
			if err != nil {
				p.err = err
				return
			}
		}
	}()
	return p
}

// Read gives the pumped bytes, then io.EOF or the error that ended them.
func (p *wirePump) Read(b []byte) (int, error) {
	if len(p.left) == 0 {
		chunk, ok := <-p.chunks
		if !ok {
			return 0, p.err
		}
		p.left = chunk
	}
	n := copy(b, p.left)
	p.left = p.left[n:]
	return n, nil
}

// close stops the pump. The goroutine ends once its read returns, which
// closing the connection it reads makes happen.
func (p *wirePump) close() { p.once.Do(func() { close(p.stop) }) }

// readBodyOrPass reads a body framed as framing from br whole, or up to the
// point it passes threshold. A passing body comes back with the bytes that
// arrived parsed as the body, cut short, and its h1Passing.
func readBodyOrPass(br *bufio.Reader, framing h1.Framing, threshold int64) (*h1.Body, *h1Passing, error) {
	if framing.Kind == h1.NoBody {
		body, err := h1.ReadBody(br, framing)
		return body, nil, err
	}
	pump := startWirePump(h1.NewWireReader(br, framing))
	if h1DeclaresOver(framing, threshold) {
		return &h1.Body{Framing: framing}, &h1Passing{rest: pump, declared: true}, nil
	}
	var arrived []byte
	var stall bodyStall
	stalled := make(chan struct{}, 1)
	defer stall.stop()
	for {
		select {
		case chunk, ok := <-pump.chunks:
			if !ok {
				body, err := h1.ReadBody(bufio.NewReader(bytes.NewReader(arrived)), framing)
				if err == nil && pump.err != io.EOF {
					err = pump.err
				}
				return body, nil, err
			}
			arrived = append(arrived, chunk...)
			if int64(len(arrived)) > threshold {
				return passedWire(arrived, framing, pump)
			}
			// A wait that ended as these bytes came is not a stall.
			select {
			case <-stalled:
			default:
			}
			stall.arrived(func() {
				select {
				case stalled <- struct{}{}:
				default:
				}
			})
		case <-stalled:
			return passedWire(arrived, framing, pump)
		}
	}
}

// passedWire is the body of the bytes that arrived before it passed, and
// its h1Passing.
func passedWire(arrived []byte, framing h1.Framing, pump *wirePump) (*h1.Body, *h1Passing, error) {
	body, _ := h1.ReadBody(bufio.NewReader(bytes.NewReader(arrived)), framing)
	return body, &h1Passing{arrived: arrived, rest: pump}, nil
}

// forward writes the rest of the passing body to w as it arrives, flushing
// each piece, and reports whether all of it went.
func (p *h1Passing) forward(w *bufio.Writer) bool {
	buf := make([]byte, bodyChunkSize)
	for {
		n, err := p.rest.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil || w.Flush() != nil {
				p.rest.close()
				return false
			}
		}
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// endOriginAfterResponse ends the origin connection after a response that
// does not keep it. While the rest of a passed request body is still being
// sent on it, it stays open as it would going direct: the origin decides
// when it ends, and the client connection stays open until then (linger).
func (c *h1Conn) endOriginAfterResponse(passing *h1Passing) {
	if passing == nil || passing.sent == nil || c.origin == nil {
		c.closeOrigin()
		return
	}
	c.lingering, c.origin = c.origin, nil
}

// linger waits until the origin closes the lingering connection or the
// rest of the request body went, then closes it.
func (p *h1Passing) linger(origin *originConn) {
	if p != nil && p.sent != nil {
		closed := make(chan struct{})
		go func() {
			io.Copy(io.Discard, origin.br)
			close(closed)
		}()
		select {
		case <-p.sent:
		case <-closed:
		}
	}
	origin.conn.Close()
}

// h1DeclaresOver reports whether a head declares a body longer than
// threshold, which goes as it arrives from the start.
func h1DeclaresOver(framing h1.Framing, threshold int64) bool {
	return framing.Kind == h1.LengthFramed && lengthOver(framing.Length, threshold)
}
