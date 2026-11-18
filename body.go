package marasi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"

	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// BodyEnding is how the proxy's read of a body ended. The proxy reads each
// body whole before the chains run, so a modifier always sees what arrived.
type BodyEnding int

const (
	// BodyUnread means the proxy did not read the body: there is no
	// response yet, it is the proxy's own answer, or it is an upgrade's
	// connection.
	BodyUnread BodyEnding = iota
	// BodyComplete means the body arrived whole, or there was none.
	BodyComplete
	// BodyCutShort means the peer stopped sending, or the read failed,
	// before the body was whole.
	BodyCutShort
	// BodyTooLarge means the body passed a size limit. No limit applies to
	// the bodies the proxy reads today, so nothing sets it yet.
	BodyTooLarge
)

// String returns the ending in words.
func (ending BodyEnding) String() string {
	switch ending {
	case BodyUnread:
		return "unread"
	case BodyComplete:
		return "complete"
	case BodyCutShort:
		return "cut short"
	case BodyTooLarge:
		return "too large"
	}
	return "BodyEnding(" + strconv.Itoa(int(ending)) + ")"
}

// readBody reads body whole and closes it. On a cut-short body it returns the
// bytes that arrived and why the read stopped.
func readBody(body io.ReadCloser) ([]byte, BodyEnding, error) {
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return data, BodyCutShort, err
	}
	return data, BodyComplete, nil
}

// cutShortBody gives back the bytes that arrived and then the error that cut
// the body short, so whoever reads it next fails as the original read did.
func cutShortBody(data []byte, err error) io.ReadCloser {
	return io.NopCloser(io.MultiReader(bytes.NewReader(data), errorReader{err}))
}

// errorReader fails every read with err.
type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

// readRequestBody reads req's body whole before the request chain runs and
// returns how it ended. The request keeps its framing, so it goes upstream as
// it arrived.
func readRequestBody(req *http.Request) BodyEnding {
	if req.Body == nil || req.Body == http.NoBody {
		return BodyComplete
	}
	data, ending, err := readBody(req.Body)
	if err != nil {
		req.Body = cutShortBody(data, err)
		return ending
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	return ending
}

// readResponseBody reads the pair's response body whole before the response
// chain runs and records how it ended. On Martian a whole body is sent with
// its length, not chunked; an engine that keeps the wire form writes the body
// as it was framed, so its view is left alone. An upgrade's body is the connection and is left unread, and so
// is a proxy error response, which Marasi made whole.
func readResponseBody(pair *Pair) error {
	res := pair.Response
	if marasiws.IsUpgradeResponse(res) || pair.FromMarasi() {
		return nil
	}
	data, ending, err := readBody(res.Body)
	pair.ResponseBodyEnding = ending
	if err != nil {
		res.Body = cutShortBody(data, err)
		return fmt.Errorf("%w: %w", ErrReadBody, err)
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	if pair.responseWire != nil {
		return nil
	}
	res.ContentLength = int64(len(data))
	res.Header.Set("Content-Length", strconv.Itoa(len(data)))
	res.TransferEncoding = nil
	return nil
}
