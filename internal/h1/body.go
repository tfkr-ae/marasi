package h1

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// ErrBodyCutShort is returned with the body that arrived when the connection
// ends before the body's framing says it is complete.
var ErrBodyCutShort = errors.New("body cut short")

// maxChunkLine caps a chunk size line with its extensions, so a peer cannot
// make the codec buffer an endless line.
const maxChunkLine = 64 << 10

// readStep is how much of a body is read at a time, so a claimed length is
// not allocated before the bytes arrive.
const readStep = 32 << 10

// Chunk is one chunk of a chunked body as it was on the wire. The last chunk,
// of size zero, is kept as a Chunk with no Data and no End.
type Chunk struct {
	// Line is the chunk size line with its extensions and line end.
	Line []byte
	// Size is the size Line declares.
	Size int64
	// Data is the chunk's data. It is shorter than Size only in a body cut
	// short.
	Data []byte
	// End is the line end after the data.
	End []byte
}

// Ext returns the chunk extensions as written, from the first ';' or the
// whitespace before it, without the line end.
func (c Chunk) Ext() string {
	line := trimEOL(c.Line)
	i := 0
	for i < len(line) && isHex(line[i]) {
		i++
	}
	return string(line[i:])
}

// Body is a message body as it was on the wire.
type Body struct {
	Framing Framing
	// Data is the body of a LengthFramed or UntilClose message.
	Data []byte
	// Chunks are the chunks of a Chunked body, the last chunk included.
	Chunks []Chunk
	// Trailer is the trailer section of a Chunked body. Its End is nil until
	// the section's ending empty line arrives.
	Trailer Header
	// Tail holds bytes of a chunked body cut short in the middle of a chunk
	// size line, a chunk's line end or a trailer line.
	Tail []byte
}

// Payload returns the body's content with any chunked coding removed.
func (b *Body) Payload() []byte {
	if b.Framing.Kind != Chunked {
		return b.Data
	}
	n := 0
	for _, c := range b.Chunks {
		n += len(c.Data)
	}
	p := make([]byte, 0, n)
	for _, c := range b.Chunks {
		p = append(p, c.Data...)
	}
	return p
}

// WriteTo writes the body as it was read, or as far as it was read when it
// was cut short.
func (b *Body) WriteTo(w io.Writer) (int64, error) {
	if b.Framing.Kind != Chunked {
		n, err := w.Write(b.Data)
		return int64(n), err
	}
	size := b.Trailer.size() + len(b.Tail)
	for _, c := range b.Chunks {
		size += len(c.Line) + len(c.Data) + len(c.End)
	}
	out := make([]byte, 0, size)
	for _, c := range b.Chunks {
		out = append(out, c.Line...)
		out = append(out, c.Data...)
		out = append(out, c.End...)
	}
	out = b.Trailer.appendTo(out)
	out = append(out, b.Tail...)
	n, err := w.Write(out)
	return int64(n), err
}

// ReadBody reads a body framed as f. When the connection ends early it
// returns the bytes that arrived with ErrBodyCutShort. Any other read error
// also comes with what arrived. A malformed chunked body gives a
// *FramingError.
func ReadBody(br *bufio.Reader, f Framing) (*Body, error) {
	b := &Body{Framing: f}
	var err error
	switch f.Kind {
	case NoBody:
	case LengthFramed:
		b.Data, err = readN(br, nil, f.Length)
	case UntilClose:
		b.Data, err = io.ReadAll(br)
	case Chunked:
		err = readChunked(br, b)
	default:
		return nil, fmt.Errorf("reading body: unknown framing %d", f.Kind)
	}
	if err == io.ErrUnexpectedEOF {
		err = ErrBodyCutShort
	}
	return b, err
}

// readN appends up to n bytes to buf. It returns io.ErrUnexpectedEOF with
// what arrived when the input ends first.
func readN(br *bufio.Reader, buf []byte, n int64) ([]byte, error) {
	for n > 0 {
		step := int(min(n, readStep))
		start := len(buf)
		buf = append(buf, make([]byte, step)...)
		got, err := io.ReadFull(br, buf[start:])
		buf = buf[:start+got]
		n -= int64(got)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return buf, err
		}
	}
	return buf, nil
}

// chunkSpan locates one chunk while its buffers still grow: line, end and
// stop are offsets in the wire buffer, data and dataStop in the data buffer.
type chunkSpan struct {
	line, end, stop int
	data, dataStop  int
	size            int64
}

// readChunked reads chunks into two buffers, one for framing bytes and one
// for data, and slices them once both stop growing, so a body of many chunks
// costs a few allocations rather than several per chunk.
func readChunked(br *bufio.Reader, b *Body) error {
	var wire, data []byte
	var spans []chunkSpan
	tail := -1
	var fields fieldSpans
	trailers := false

	err := func() error {
		for {
			sp := chunkSpan{line: len(wire)}
			var err error
			wire, err = readLine(br, wire, len(wire)+maxChunkLine)
			if err == ErrHeadTooLarge {
				return &FramingError{Cause: CauseBadChunkSize, Detail: "chunk size line too long"}
			}
			if err != nil {
				tail = sp.line
				return err
			}
			size, ok := parseChunkLine(trimEOL(wire[sp.line:]))
			if !ok {
				return &FramingError{Cause: CauseBadChunkSize, Detail: fmt.Sprintf("%q", trimEOL(wire[sp.line:]))}
			}
			sp.size = size
			sp.data = len(data)
			sp.end = len(wire)
			if size == 0 {
				sp.dataStop, sp.stop = sp.data, sp.end
				spans = append(spans, sp)
				break
			}
			data, err = readN(br, data, size)
			sp.dataStop = len(data)
			if err != nil {
				sp.stop = sp.end
				spans = append(spans, sp)
				return err
			}
			wire, err = readChunkEnd(br, wire)
			sp.stop = len(wire)
			spans = append(spans, sp)
			if err != nil {
				return err
			}
		}
		trailers = true
		start := len(wire)
		var err error
		wire, fields, err = readFields(br, wire, start+MaxHeadBytes)
		if err == io.ErrUnexpectedEOF {
			tail = fields.stop
		}
		return err
	}()

	if len(spans) > 0 {
		b.Chunks = make([]Chunk, len(spans))
		for i, sp := range spans {
			c := Chunk{
				Line: wire[sp.line:sp.end:sp.end],
				Size: sp.size,
			}
			if sp.dataStop > sp.data {
				c.Data = data[sp.data:sp.dataStop:sp.dataStop]
			}
			if sp.stop > sp.end {
				c.End = wire[sp.end:sp.stop:sp.stop]
			}
			b.Chunks[i] = c
		}
	}
	if trailers {
		b.Trailer = fields.header(wire)
	}
	if tail >= 0 && tail < len(wire) {
		b.Tail = wire[tail:len(wire):len(wire)]
	}
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// readChunkEnd appends the line end that must follow chunk data.
func readChunkEnd(br *bufio.Reader, wire []byte) ([]byte, error) {
	c, err := br.ReadByte()
	if err != nil {
		return wire, err
	}
	wire = append(wire, c)
	if c == '\n' {
		return wire, nil
	}
	if c != '\r' {
		return wire, &FramingError{Cause: CauseBadChunkEnd, Detail: fmt.Sprintf("%q", c)}
	}
	c, err = br.ReadByte()
	if err != nil {
		return wire, err
	}
	wire = append(wire, c)
	if c != '\n' {
		return wire, &FramingError{Cause: CauseBadChunkEnd, Detail: fmt.Sprintf("%q", c)}
	}
	return wire, nil
}

// parseChunkLine parses a chunk size line without its line end: hex digits,
// then nothing or optional whitespace and ';' extensions (RFC 9112 §7.1.1).
func parseChunkLine(line []byte) (int64, bool) {
	var n int64
	i := 0
	for ; i < len(line) && isHex(line[i]); i++ {
		if n > (1<<63-1)>>4 {
			return 0, false
		}
		n = n<<4 | int64(unhex(line[i]))
	}
	if i == 0 {
		return 0, false
	}
	ext := bytes.TrimLeft(line[i:], " \t")
	if len(ext) > 0 && ext[0] != ';' {
		return 0, false
	}
	if len(ext) == 0 && len(line) > i {
		// Whitespace after the size with no extension after it.
		return 0, false
	}
	return n, validValue(ext)
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}
