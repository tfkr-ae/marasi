package websocket

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
)

// maxWindow is the largest LZ77 window permessage-deflate allows (2^15 bytes).
// It is also the dictionary an inflater keeps between messages.
const maxWindow = 1 << 15

// syncTail ends every sync flush. RFC 7692 section 7.2.1 removes it from each
// compressed message, and the receiver appends it again before inflating.
var syncTail = []byte{0x00, 0x00, 0xff, 0xff}

// inflateTail is syncTail followed by an empty final block, so that the flate
// reader ends cleanly at the end of a message.
var inflateTail = []byte{0x00, 0x00, 0xff, 0xff, 0x01, 0x00, 0x00, 0xff, 0xff}

// inflater decompresses the messages one peer sends (RFC 7692 section 7.2.2).
// With context takeover the last 32 KB of output is the dictionary for the
// next message.
type inflater struct {
	noContextTakeover bool
	dict              []byte
	reader            io.ReadCloser
}

func newInflater(noContextTakeover bool) *inflater {
	return &inflater{noContextTakeover: noContextTakeover}
}

// errInflatedTooLarge reports a compressed message that inflates to more than
// the size limit.
var errInflatedTooLarge = errors.New("too large")

// inflate returns the uncompressed payload of one compressed message. It
// stops once the output goes over limit bytes, so it never holds more than
// limit+1 bytes of output, and returns errInflatedTooLarge. A limit of zero
// or less means no limit.
func (f *inflater) inflate(payload []byte, limit int64) ([]byte, error) {
	source := io.MultiReader(bytes.NewReader(payload), bytes.NewReader(inflateTail))
	var dict []byte
	if !f.noContextTakeover {
		dict = f.dict
	}
	if f.reader == nil {
		f.reader = flate.NewReaderDict(source, dict)
	} else if err := f.reader.(flate.Resetter).Reset(source, dict); err != nil {
		return nil, fmt.Errorf("resetting inflater: %w", err)
	}

	var output io.Reader = f.reader
	if limit > 0 {
		output = io.LimitReader(f.reader, limit+1)
	}
	out, err := io.ReadAll(output)
	if err != nil {
		return nil, fmt.Errorf("inflating message: %w", err)
	}
	if limit > 0 && int64(len(out)) > limit {
		return nil, fmt.Errorf("%w: inflated message is over the %d-byte limit", errInflatedTooLarge, limit)
	}
	if !f.noContextTakeover {
		f.dict = lastWindow(f.dict, out)
	}
	return out, nil
}

// lastWindow returns the last maxWindow bytes of dict followed by out.
func lastWindow(dict, out []byte) []byte {
	if len(out) >= maxWindow {
		return append(dict[:0], out[len(out)-maxWindow:]...)
	}
	keep := min(len(dict), maxWindow-len(out))
	next := make([]byte, 0, keep+len(out))
	next = append(next, dict[len(dict)-keep:]...)
	return append(next, out...)
}

// deflater compresses the messages the proxy sends to one peer (RFC 7692
// section 7.2.1). Toward a peer with a full 32 KB window it keeps one stream
// across messages unless context takeover is off. Toward a smaller window it
// compresses Huffman-only, which refers to no earlier bytes, and sends a
// message uncompressed when that is not smaller.
type deflater struct {
	noContextTakeover bool
	huffmanOnly       bool
	buf               bytes.Buffer
	writer            *flate.Writer
}

func newDeflater(noContextTakeover bool, windowBits int) *deflater {
	d := &deflater{noContextTakeover: noContextTakeover, huffmanOnly: windowBits < 15}
	level := flate.DefaultCompression
	if d.huffmanOnly {
		level = flate.HuffmanOnly
	}
	// The level is valid, so NewWriter cannot fail.
	d.writer, _ = flate.NewWriter(&d.buf, level)
	return d
}

// deflate returns the payload to send and whether it is compressed. The
// result is only valid until the next call.
func (d *deflater) deflate(payload []byte) ([]byte, bool) {
	if d.noContextTakeover || d.huffmanOnly {
		d.writer.Reset(&d.buf)
	}
	d.buf.Reset()
	// Writes to a bytes.Buffer cannot fail.
	_, _ = d.writer.Write(payload)
	_ = d.writer.Flush()
	out := bytes.TrimSuffix(d.buf.Bytes(), syncTail)
	if d.huffmanOnly && len(out) >= len(payload) {
		return payload, false
	}
	return out, true
}
