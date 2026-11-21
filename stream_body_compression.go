package marasi

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/stream"
)

// A server-sent event or NDJSON body with a Content-Encoding is compressed
// as a whole, so each compressed message depends on the sender's earlier
// output. The proxy terminates that compression on each leg, as it does
// permessage-deflate (the 2026-11-20 amendment of
// docs/adr/2026-10-11-proxy-terminates-permessage-deflate-per-leg.md): the
// carry reads the sender's body through an inflatingSource, so the splitter,
// the hooks, Checkpoint and storage see decoded stream messages, and writes
// to the receiver through a compressingSink, Marasi's own compressor,
// flushed after each message. The receiver gets the same messages in
// different compressed bytes, and an edit, a drop or a hold is safe at any
// point. Only gzip and deflate are carried so; any other coding, or more
// than one, leaves the body read whole.

// streamedBodyEncoding is the whole-body coding the engines carry streamed
// for a Content-Encoding value: "gzip" or "deflate", "" for none or
// identity. It reports false for a coding they read whole instead.
func streamedBodyEncoding(contentEncoding string) (string, bool) {
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	switch encoding {
	case "", "identity":
		return "", true
	case "gzip", "deflate":
		return encoding, true
	}
	return "", false
}

// inflateError is a streamed body the proxy could not inflate.
type inflateError struct{ err error }

func (e *inflateError) Error() string { return e.err.Error() }

// inflatingSource is a streamed direction whose body is compressed as a
// whole, read as the bytes it inflates to. It inflates only as its reader
// peeks, so a reader that stops stops the sender as before. It keeps the
// compressed bytes received since the read that completed the last stream
// message, which are what a message that cannot be inflated is stored as:
// compressed bytes do not split at message boundaries, so they may start
// with the end of the message before.
type inflatingSource struct {
	streamSource
	encoding string
	limit    int64

	decoder io.ReadCloser
	out     []byte
	buf     []byte
	// received is the compressed bytes received since the read after the
	// last mark, up to the limit; total counts every compressed byte
	// received.
	received []byte
	marked   bool
	total    int64
	// sourceErr is how the sender's body ended, once it did.
	sourceErr error
	err       error
}

func newInflatingSource(src streamSource, encoding string, limit int64) *inflatingSource {
	return &inflatingSource{streamSource: src, encoding: encoding, limit: limit, buf: make([]byte, 32<<10)}
}

// Read is the compressed body as the decoder reads it.
func (s *inflatingSource) Read(p []byte) (int, error) {
	data, err := s.streamSource.peek()
	if len(data) == 0 {
		s.sourceErr = err
		return 0, err
	}
	n := copy(p, data)
	s.streamSource.discard(n)
	s.total += int64(n)
	if s.marked {
		s.received, s.marked = nil, false
	}
	if room := s.limit - int64(len(s.received)); room > 0 {
		s.received = append(s.received, p[:min(int64(n), room)]...)
	}
	return n, nil
}

// peek is streamSource.peek over the inflated body. A body that fails to
// inflate ends with an *inflateError, once what inflated before the
// failure was read.
func (s *inflatingSource) peek() ([]byte, error) {
	for len(s.out) == 0 && s.err == nil {
		if s.decoder == nil {
			decoder, err := s.newDecoder()
			if err != nil {
				s.fail(err)
				break
			}
			s.decoder = decoder
		}
		n, err := s.decoder.Read(s.buf)
		s.out = append(s.out, s.buf[:n]...)
		if err != nil {
			s.fail(err)
		}
	}
	if len(s.out) > 0 {
		return s.out, nil
	}
	return nil, s.err
}

func (s *inflatingSource) discard(n int) {
	s.out = s.out[n:]
	if len(s.out) == 0 {
		s.out = nil
	}
}

func (s *inflatingSource) newDecoder() (io.ReadCloser, error) {
	if s.encoding == "gzip" {
		return gzip.NewReader(s)
	}
	return zlib.NewReader(s)
}

// fail records why the inflated body ended. The compressed stream's end is
// the body's end, when the sender's body ends there too. The sender's
// reset or cut-off is that, not an inflate failure; anything else is one.
func (s *inflatingSource) fail(err error) {
	switch {
	case s.sourceErr != nil && !errors.Is(s.sourceErr, io.EOF):
		s.err = s.sourceErr
	case s.total == 0 && errors.Is(s.sourceErr, io.EOF):
		// An empty body has no compressed stream to end.
		s.err = io.EOF
	case errors.Is(err, io.EOF):
		if data, end := s.streamSource.peek(); len(data) > 0 {
			s.err = &inflateError{fmt.Errorf("%d bytes follow the end of the %s stream", len(data), s.encoding)}
		} else {
			s.sourceErr, s.err = end, end
		}
	default:
		s.err = &inflateError{err}
	}
}

// mark starts the next stream message's compressed bytes with the next
// read, since the last read may hold the start of the next message.
func (s *inflatingSource) mark() { s.marked = true }

// compressed is the compressed bytes of the stream message being read.
func (s *inflatingSource) compressed() []byte { return bytes.Clone(s.received) }

// compressor is the writer side of gzip and zlib.
type compressor interface {
	io.Writer
	Flush() error
	Close() error
}

// compressingSink is a streamed direction going to its receiver compressed
// with Marasi's own compressor, flushed after each stream message.
type compressingSink struct {
	streamSink
	buf bytes.Buffer
	w   compressor
}

func newCompressingSink(sink streamSink, encoding string) *compressingSink {
	s := &compressingSink{streamSink: sink}
	if encoding == "gzip" {
		s.w = gzip.NewWriter(&s.buf)
	} else {
		s.w = zlib.NewWriter(&s.buf)
	}
	return s
}

func (s *compressingSink) forward(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	s.w.Write(raw)
	s.w.Flush()
	return s.send()
}

// end ends the compressed stream, then the direction.
func (s *compressingSink) end(trailers h2Fields) error {
	s.w.Close()
	if err := s.send(); err != nil {
		return err
	}
	return s.streamSink.end(trailers)
}

func (s *compressingSink) send() error {
	if s.buf.Len() == 0 {
		return nil
	}
	out := bytes.Clone(s.buf.Bytes())
	s.buf.Reset()
	return s.streamSink.forward(out)
}

// bodyEncodings records the whole-body codings of d: in, the one its
// sender's head names, and out, the one its head went out with. The engine
// calls it before the direction is carried.
func (sp *streamedPair) bodyEncodings(d *streamedDirection, in, out string) {
	out, _ = streamedBodyEncoding(out)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	d.bodyIn, d.bodyOut = in, out
}

// compressedBody wraps a direction's source and sink in its whole-body
// codings, if it has any. The inflater is nil when the sender's body is
// not compressed.
func (sp *streamedPair) compressedBody(d *streamedDirection, src streamSource, sink streamSink) (streamSource, streamSink, *inflatingSource) {
	sp.mu.Lock()
	in, out := d.bodyIn, d.bodyOut
	sp.mu.Unlock()
	var inflater *inflatingSource
	if in != "" {
		inflater = newInflatingSource(src, in, sp.limit)
		src = inflater
	}
	if out != "" {
		sink = newCompressingSink(sink, out)
	}
	return src, sink, inflater
}

// inflateFailed stores the stream message a body could not be inflated
// at as the compressed bytes received for it, with the error in
// inflate_error, writes a proxy log and records the direction reset. No
// hook sees it, since it cannot be edited or forwarded.
func (sp *streamedPair) inflateFailed(name string, d *streamedDirection, compressed []byte, err error, code http2.ErrCode, extra map[string]any) {
	sp.mu.Lock()
	d.messages++
	encoding := d.bodyIn
	sp.mu.Unlock()
	metadata := map[string]any{"encoding": encoding, "inflate_error": err.Error()}
	for k, v := range extra {
		metadata[k] = v
	}
	id, idErr := uuid.NewV7()
	if idErr != nil {
		id = uuid.New()
	}
	row := sp.message(id, name, d, nil, compressed, metadata)
	row.Compressed = true
	sp.queue(row)
	sp.end(d, streamReset, code)
	entry := fmt.Sprintf("Stream not inflated (%s): the %s body compressed with %q could not be inflated (%v); the stream was reset", sp.requestName, name, encoding, err)
	_ = sp.proxy.WriteLog("WARN", entry, sp.logOptions()...)
}

// inflatedTooLarge is the inflate error of a stream message that inflates
// past the limit.
func inflatedTooLarge(err *stream.TooLargeError) error {
	return fmt.Errorf("too large: the stream message inflates past the %d-byte limit", err.Limit)
}
