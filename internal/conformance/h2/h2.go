// Package h2 is the conformance harness's HTTP/2 recording: what one side
// of each connection received, decoded to stream meaning, and the
// comparison of two recordings. A recording keeps each stream's header
// blocks, DATA bytes, trailers, where END_STREAM fell and its RST_STREAM
// code, and each connection's GOAWAY. It leaves out what a correct peer may
// choose freely: SETTINGS values, WINDOW_UPDATE, PING, PRIORITY, how DATA is
// split into frames, padding, HPACK encoding and stream ID numbers.
package h2

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// Recording is one h2 case: the connections the harness client opened and
// the connections the origin accepted, each as its side received it.
type Recording struct {
	Client []Connection `json:"client,omitempty"`
	Origin []Connection `json:"origin,omitempty"`
}

// Connection is what one side of a connection received.
type Connection struct {
	// ALPN is the protocol the TLS handshake negotiated; empty when none
	// was. A side that did not get h2 records no streams.
	ALPN string `json:"alpn"`
	// Preface is false when the peer did not open with the HTTP/2
	// client connection preface; only an origin records it.
	Preface *bool    `json:"preface,omitempty"`
	Streams []Stream `json:"streams"` // in the order they opened
	GoAway  *GoAway  `json:"goaway,omitempty"`
}

// Stream is what one side received on one stream.
type Stream struct {
	// Headers are the header blocks before any trailers, in order, each
	// with its pseudo-header fields: interim (1xx) responses, then the
	// final response, or the request's one block.
	Headers [][]Field `json:"headers"`
	Data    []byte    `json:"data,omitempty"` // the DATA frames' payloads joined, padding left out
	// DataLength and DataSHA256 stand for Data when it is longer than
	// LargeData bytes, so a large body keeps the baseline small.
	DataLength int     `json:"data_length,omitempty"`
	DataSHA256 string  `json:"data_sha256,omitempty"`
	Trailers   []Field `json:"trailers,omitempty"`
	// EndStream is where END_STREAM fell: EndHeaders, EndData,
	// EndTrailers, or empty when it never came.
	EndStream string `json:"end_stream,omitempty"`
	Reset     string `json:"reset,omitempty"` // the RST_STREAM error code received, such as CANCEL

	id    uint32
	final bool // a final header block came, so the next one is trailers
}

// LargeData is the longest body a recording keeps as bytes.
const LargeData = 4096

// Where END_STREAM fell.
const (
	EndHeaders  = "headers"
	EndData     = "data"
	EndTrailers = "trailers"
)

// GoAway is a GOAWAY received.
type GoAway struct {
	Code string `json:"code"` // error code, such as NO_ERROR
	// Processed are the positions in Streams of the streams this side
	// opened that the peer says it processed, at or below its last stream ID.
	Processed []int `json:"processed"`
}

// Field is one decoded header field.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Fields turns name, value pairs into fields.
func Fields(pairs ...string) []Field {
	fields := make([]Field, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		fields = append(fields, Field{Name: pairs[i], Value: pairs[i+1]})
	}
	return fields
}

// Recorder builds a Connection from the frames one side receives. It is
// not safe for concurrent use.
type Recorder struct {
	conn    Connection
	decoder *hpack.Decoder
	opened  map[uint32]bool // streams this side opened

	block       []byte // a header block waiting for its CONTINUATION
	blockStream uint32
	blockEnd    bool

	goAwayLast uint32 // the last stream ID of the GOAWAY received
}

// NewRecorder starts recording a connection that negotiated alpn.
func NewRecorder(alpn string) *Recorder {
	return &Recorder{
		conn:    Connection{ALPN: alpn, Streams: []Stream{}},
		decoder: hpack.NewDecoder(4096, nil),
		opened:  map[uint32]bool{},
	}
}

// Open records that this side opened stream id, so the stream takes its
// place in the opening order before anything arrives on it.
func (r *Recorder) Open(id uint32) {
	r.opened[id] = true
	r.stream(id)
}

// NoPreface records that the peer did not send the connection preface.
func (r *Recorder) NoPreface() {
	r.conn.Preface = new(bool)
}

// StreamID returns the ID of the stream at position i in opening order, or
// 0 when there is none.
func (r *Recorder) StreamID(i int) uint32 {
	if i < 0 || i >= len(r.conn.Streams) {
		return 0
	}
	return r.conn.Streams[i].id
}

// Closed reports whether the stream at position i ended: END_STREAM or
// RST_STREAM came on it.
func (r *Recorder) Closed(i int) bool {
	if i < 0 || i >= len(r.conn.Streams) {
		return false
	}
	s := r.conn.Streams[i]
	return s.EndStream != "" || s.Reset != ""
}

// Streams counts the streams so far.
func (r *Recorder) Streams() int { return len(r.conn.Streams) }

// GoneAway reports whether a GOAWAY came, and its last stream ID.
func (r *Recorder) GoneAway() (bool, uint32) {
	if r.conn.GoAway == nil {
		return false, 0
	}
	return true, r.goAwayLast
}

// Frame records one frame received. Frames that carry no stream meaning,
// such as SETTINGS, PING, WINDOW_UPDATE and PRIORITY, are ignored.
func (r *Recorder) Frame(frame http2.Frame) error {
	switch f := frame.(type) {
	case *http2.HeadersFrame:
		r.block = append(r.block[:0], f.HeaderBlockFragment()...)
		r.blockStream, r.blockEnd = f.StreamID, f.StreamEnded()
		if f.HeadersEnded() {
			return r.headers()
		}
	case *http2.ContinuationFrame:
		if f.StreamID != r.blockStream {
			return fmt.Errorf("CONTINUATION on stream %d inside a header block on stream %d", f.StreamID, r.blockStream)
		}
		r.block = append(r.block, f.HeaderBlockFragment()...)
		if f.HeadersEnded() {
			return r.headers()
		}
	case *http2.DataFrame:
		s := r.stream(f.StreamID)
		s.Data = append(s.Data, f.Data()...)
		if f.StreamEnded() {
			s.EndStream = EndData
		}
	case *http2.RSTStreamFrame:
		r.stream(f.StreamID).Reset = f.ErrCode.String()
	case *http2.GoAwayFrame:
		goAway := &GoAway{Code: f.ErrCode.String(), Processed: []int{}}
		for i, s := range r.conn.Streams {
			if r.opened[s.id] && s.id <= f.LastStreamID {
				goAway.Processed = append(goAway.Processed, i)
			}
		}
		r.conn.GoAway, r.goAwayLast = goAway, f.LastStreamID
	}
	return nil
}

// headers decodes a whole header block and files it on its stream.
func (r *Recorder) headers() error {
	decoded, err := r.decoder.DecodeFull(r.block)
	if err != nil {
		return fmt.Errorf("decoding a header block on stream %d: %w", r.blockStream, err)
	}
	fields := make([]Field, len(decoded))
	for i, f := range decoded {
		fields[i] = Field{Name: f.Name, Value: f.Value}
	}
	s := r.stream(r.blockStream)
	if s.final {
		s.Trailers = fields
		if r.blockEnd {
			s.EndStream = EndTrailers
		}
		return nil
	}
	s.Headers = append(s.Headers, fields)
	s.final = !informational(fields)
	if r.blockEnd {
		s.EndStream = EndHeaders
	}
	return nil
}

// informational reports whether a header block is an interim 1xx response.
func informational(fields []Field) bool {
	for _, f := range fields {
		if f.Name == ":status" {
			return strings.HasPrefix(f.Value, "1")
		}
	}
	return false
}

// stream returns the stream with id, adding it in opening order when new.
func (r *Recorder) stream(id uint32) *Stream {
	for i := range r.conn.Streams {
		if r.conn.Streams[i].id == id {
			return &r.conn.Streams[i]
		}
	}
	r.conn.Streams = append(r.conn.Streams, Stream{Headers: [][]Field{}, id: id})
	return &r.conn.Streams[len(r.conn.Streams)-1]
}

// Connection returns a copy of what was recorded so far, with every field
// value passed through normalise.
func (r *Recorder) Connection(normalise func(string) string) Connection {
	conn := r.conn
	conn.Streams = make([]Stream, len(r.conn.Streams))
	for i, s := range r.conn.Streams {
		s.Headers = make([][]Field, len(r.conn.Streams[i].Headers))
		for j, block := range r.conn.Streams[i].Headers {
			s.Headers[j] = normaliseFields(block, normalise)
		}
		s.Trailers = normaliseFields(s.Trailers, normalise)
		if len(s.Data) > LargeData {
			sum := sha256.Sum256(s.Data)
			s.DataLength, s.DataSHA256, s.Data = len(s.Data), hex.EncodeToString(sum[:]), nil
		} else {
			s.Data = slices.Clone(s.Data)
		}
		conn.Streams[i] = s
	}
	if r.conn.GoAway != nil {
		goAway := *r.conn.GoAway
		goAway.Processed = slices.Clone(goAway.Processed)
		conn.GoAway = &goAway
	}
	return conn
}

func normaliseFields(fields []Field, normalise func(string) string) []Field {
	if fields == nil {
		return nil
	}
	out := make([]Field, len(fields))
	for i, f := range fields {
		out[i] = Field{Name: f.Name, Value: normalise(f.Value)}
	}
	return out
}
