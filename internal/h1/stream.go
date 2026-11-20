package h1

import (
	"bufio"
	"fmt"
	"io"
)

// BodyReader reads a body framed as Framing a piece at a time, as its bytes
// arrive, for a body carried on before it ends. It gives the payload, chunked
// coding removed, and reads no further than the body: what follows stays in
// the reader for the next message.
//
// It has one reader. Peek waits for bytes and Discard consumes them, so a
// reader that stops consuming stops reading the connection.
type BodyReader struct {
	br      *bufio.Reader
	framing Framing
	// left is what remains of the current chunk, or of a length-framed
	// body.
	left int64
	// inChunk is set while a chunk's data or its line end is unread.
	inChunk bool
	trailer Header
	// end is how the body ended, nil while it goes on.
	end error
}

// NewBodyReader reads a body framed as f from br.
func NewBodyReader(br *bufio.Reader, f Framing) *BodyReader {
	r := &BodyReader{br: br, framing: f}
	switch f.Kind {
	case NoBody:
		r.end = io.EOF
	case LengthFramed:
		r.left = f.Length
	}
	return r
}

// Peek waits until payload bytes are buffered or the body ended, and returns
// the buffered bytes without consuming them. Once none remain it returns how
// the body ended: io.EOF when it is complete, ErrBodyCutShort when the
// connection ended first, a *FramingError for a malformed chunked body, or
// the read error.
func (r *BodyReader) Peek() ([]byte, error) {
	for r.end == nil {
		if r.framing.Kind == Chunked && r.left == 0 {
			r.end = r.nextChunk()
			continue
		}
		if r.framing.Kind == LengthFramed && r.left == 0 {
			r.end = io.EOF
			continue
		}
		if r.br.Buffered() == 0 {
			if _, err := r.br.Peek(1); err != nil {
				r.end = r.cut(err)
				continue
			}
		}
		n := r.br.Buffered()
		if r.framing.Kind != UntilClose && int64(n) > r.left {
			n = int(r.left)
		}
		data, _ := r.br.Peek(n)
		return data, nil
	}
	return nil, r.end
}

// Discard consumes n of the bytes Peek returned.
func (r *BodyReader) Discard(n int) {
	r.br.Discard(n)
	if r.framing.Kind != UntilClose {
		r.left -= int64(n)
	}
}

// Trailer is the trailer section of a chunked body, once Peek returned
// io.EOF.
func (r *BodyReader) Trailer() Header { return r.trailer }

// cut is the end of a body whose read failed with err. The end of the
// connection completes a body read until close, and cuts any other short.
func (r *BodyReader) cut(err error) error {
	if err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	if r.framing.Kind == UntilClose && err == io.EOF {
		return io.EOF
	}
	return ErrBodyCutShort
}

// nextChunk reads the line end of the chunk just read and the next chunk's
// size line. It returns nil when a chunk with data follows, io.EOF after the
// last chunk and the trailer section, and the error otherwise.
func (r *BodyReader) nextChunk() error {
	if r.inChunk {
		if _, err := readChunkEnd(r.br, nil); err != nil {
			return r.cut(err)
		}
		r.inChunk = false
	}
	line, err := readLine(r.br, nil, maxChunkLine)
	if err == ErrHeadTooLarge {
		return &FramingError{Cause: CauseBadChunkSize, Detail: "chunk size line too long"}
	}
	if err != nil {
		return r.cut(err)
	}
	size, ok := parseChunkLine(trimEOL(line))
	if !ok {
		return &FramingError{Cause: CauseBadChunkSize, Detail: fmt.Sprintf("%q", trimEOL(line))}
	}
	if size > 0 {
		r.left, r.inChunk = size, true
		return nil
	}
	buf, fields, err := readFields(r.br, nil, MaxHeadBytes)
	if err != nil {
		return r.cut(err)
	}
	r.trailer = fields.header(buf)
	return io.EOF
}
