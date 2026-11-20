package h1

import (
	"bufio"
	"fmt"
	"io"
)

// WireReader reads a body framed as f and gives its bytes exactly as they
// were on the wire, chunk lines and trailers included, ending with io.EOF
// where the framing ends and reading nothing past it. It stops where
// ReadBody would, with the same errors: ErrBodyCutShort when the connection
// ends first, a *FramingError for a malformed chunked body. ReadBody of the
// bytes it gave returns the Body ReadBody would have read.
//
// It is how a body too large to read whole is forwarded as it arrives.
type WireReader struct {
	br      *bufio.Reader
	chunked bool
	// state is what comes next; left is the data still to read of a
	// length-framed body or the current chunk.
	state   wireState
	left    int64
	pending []byte
	err     error
}

type wireState int

const (
	wireData wireState = iota
	wireUntilClose
	wireChunkLine
	wireChunkEnd
	wireTrailers
	wireDone
)

// NewWireReader reads the body framed as f from br.
func NewWireReader(br *bufio.Reader, f Framing) *WireReader {
	r := &WireReader{br: br}
	switch f.Kind {
	case LengthFramed:
		r.state, r.left = wireData, f.Length
	case UntilClose:
		r.state = wireUntilClose
	case Chunked:
		r.state, r.chunked = wireChunkLine, true
	default:
		r.state = wireDone
	}
	if r.state == wireData && r.left == 0 {
		r.state = wireDone
	}
	return r
}

// Ended reports whether every byte of the body was read, so the next Read
// gives what is left of it and then io.EOF.
func (r *WireReader) Ended() bool {
	return r.state == wireDone && r.err != ErrBodyCutShort
}

func (r *WireReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 && r.err == nil {
		if r.state == wireData || r.state == wireUntilClose {
			return r.readData(p)
		}
		r.step()
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	return 0, r.err
}

// readData reads body data: the rest of a length-framed body or chunk, or
// a body that runs until close.
func (r *WireReader) readData(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.state == wireData && int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.br.Read(p)
	if r.state == wireUntilClose {
		if err == io.EOF {
			r.state, r.err = wireDone, io.EOF
		} else if err != nil {
			r.err = err
		}
		return n, nil
	}
	r.left -= int64(n)
	switch {
	case r.left == 0 && r.chunked:
		r.state = wireChunkEnd
	case r.left == 0:
		r.state, r.err = wireDone, io.EOF
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		r.err = ErrBodyCutShort
	case err != nil:
		r.err = err
	}
	return n, nil
}

// step reads the framing bytes that come next into pending.
func (r *WireReader) step() {
	var err error
	switch r.state {
	case wireChunkLine:
		r.pending, err = readLine(r.br, nil, maxChunkLine)
		if err == ErrHeadTooLarge {
			r.pending, r.err = nil, &FramingError{Cause: CauseBadChunkSize, Detail: "chunk size line too long"}
			return
		}
		if err == nil {
			size, ok := parseChunkLine(trimEOL(r.pending))
			switch {
			case !ok:
				r.err = &FramingError{Cause: CauseBadChunkSize, Detail: fmt.Sprintf("%q", trimEOL(r.pending))}
				return
			case size == 0:
				r.state = wireTrailers
			default:
				r.state, r.left = wireData, size
			}
		}
	case wireChunkEnd:
		r.pending, err = readChunkEnd(r.br, nil)
		r.state = wireChunkLine
	case wireTrailers:
		r.pending, _, err = readFields(r.br, nil, MaxHeadBytes)
		r.state = wireDone
	case wireDone:
		r.err = io.EOF
		return
	}
	switch err {
	case nil:
	case io.EOF, io.ErrUnexpectedEOF:
		r.err = ErrBodyCutShort
	default:
		r.err = err
	}
}
