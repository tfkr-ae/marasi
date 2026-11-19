package marasi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyReadsBodies(t *testing.T) {
	t.Run("should hand both chains whole bodies and record that each ended complete", func(t *testing.T) {
		originBody := make(chan string, 1)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			originBody <- string(body)
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "streamed ")
			flusher.Flush()
			_, _ = io.WriteString(w, "response")
		}))
		defer origin.Close()

		type seen struct {
			body          string
			contentLength int64
			ending        BodyEnding
		}
		requestSeen := make(chan seen, 1)
		responseSeen := make(chan seen, 1)
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(func(_ *Proxy, pair *Pair) error {
				body, err := io.ReadAll(pair.Request.Body)
				if err != nil {
					return err
				}
				pair.Request.Body = io.NopCloser(strings.NewReader(string(body)))
				requestSeen <- seen{string(body), pair.Request.ContentLength, pair.RequestBodyEnding}
				return nil
			})
			proxy.AddResponsePairModifier(func(_ *Proxy, pair *Pair) error {
				body, err := io.ReadAll(pair.Response.Body)
				if err != nil {
					return err
				}
				pair.Response.Body = io.NopCloser(strings.NewReader(string(body)))
				responseSeen <- seen{string(body), pair.Response.ContentLength, pair.ResponseBodyEnding}
				return nil
			})
		})

		// A reader with no length is sent chunked.
		req, err := http.NewRequest(http.MethodPost, origin.URL, io.MultiReader(strings.NewReader("chunked "), strings.NewReader("request")))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("sending request: %v", err)
		}
		defer res.Body.Close()
		clientBody, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("reading response: %v", err)
		}

		if got := <-requestSeen; got != (seen{"chunked request", -1, BodyComplete}) {
			t.Fatalf("\nwanted request chain to see:\n%+v\ngot:\n%+v", seen{"chunked request", -1, BodyComplete}, got)
		}
		// The view keeps the wire framing: a chunked response has no length.
		if got := <-responseSeen; got != (seen{"streamed response", -1, BodyComplete}) {
			t.Fatalf("\nwanted response chain to see:\n%+v\ngot:\n%+v", seen{"streamed response", -1, BodyComplete}, got)
		}
		if got := <-originBody; got != "chunked request" {
			t.Fatalf("\nwanted origin body:\n%q\ngot:\n%q", "chunked request", got)
		}
		if string(clientBody) != "streamed response" {
			t.Fatalf("\nwanted client body:\n%q\ngot:\n%q", "streamed response", clientBody)
		}
	})
}

func TestReadResponseBody(t *testing.T) {
	t.Run("should record a body that fails partway as cut short and keep what arrived before the failure", func(t *testing.T) {
		cut := errors.New("origin went away")
		res := &http.Response{
			Header: make(http.Header),
			Body:   io.NopCloser(io.MultiReader(strings.NewReader("partial"), failingReader{cut})),
		}
		pair := &Pair{Response: res}

		err := readResponseBody(pair)

		if !errors.Is(err, ErrReadBody) || !errors.Is(err, cut) {
			t.Fatalf("\nwanted:\n%v and %v\ngot:\n%v", ErrReadBody, cut, err)
		}
		if pair.ResponseBodyEnding != BodyCutShort {
			t.Fatalf("\nwanted ending:\n%v\ngot:\n%v", BodyCutShort, pair.ResponseBodyEnding)
		}
		got, err := io.ReadAll(res.Body)
		if string(got) != "partial" || !errors.Is(err, cut) {
			t.Fatalf("\nwanted body:\n%q then %v\ngot:\n%q then %v", "partial", cut, got, err)
		}
	})

	t.Run("should leave an upgrade's body, which is the connection, unread", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://marasi.app/socket", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		body := &trackingResponseBody{}
		res := &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Request:    req,
			Header:     http.Header{"Connection": []string{"Upgrade"}, "Upgrade": []string{"websocket"}},
			Body:       body,
		}
		pair := &Pair{Response: res}

		if err := readResponseBody(pair); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if body.read || body.closed {
			t.Fatalf("\nwanted body untouched\ngot read %v, closed %v", body.read, body.closed)
		}
		if pair.ResponseBodyEnding != BodyUnread {
			t.Fatalf("\nwanted ending:\n%v\ngot:\n%v", BodyUnread, pair.ResponseBodyEnding)
		}
	})
}

func TestReadRequestBody(t *testing.T) {
	t.Run("should record a body the client cut short and keep what arrived before the failure", func(t *testing.T) {
		cut := errors.New("client went away")
		req := httptest.NewRequest(http.MethodPost, "http://marasi.app/", nil)
		req.Body = io.NopCloser(io.MultiReader(strings.NewReader("partial"), failingReader{cut}))
		req.ContentLength = 100

		ending := readRequestBody(req)

		if ending != BodyCutShort {
			t.Fatalf("\nwanted ending:\n%v\ngot:\n%v", BodyCutShort, ending)
		}
		got, err := io.ReadAll(req.Body)
		if string(got) != "partial" || !errors.Is(err, cut) {
			t.Fatalf("\nwanted body:\n%q then %v\ngot:\n%q then %v", "partial", cut, got, err)
		}
	})

	t.Run("should record a request with no body as complete and keep it bodiless", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://marasi.app/", nil)
		req.Body = http.NoBody

		if ending := readRequestBody(req); ending != BodyComplete {
			t.Fatalf("\nwanted ending:\n%v\ngot:\n%v", BodyComplete, ending)
		}
		if req.Body != http.NoBody {
			t.Fatalf("\nwanted body:\nhttp.NoBody\ngot:\n%T", req.Body)
		}
	})
}

// failingReader fails every read with err.
type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }
