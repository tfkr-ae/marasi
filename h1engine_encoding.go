package marasi

import (
	"bytes"
	"io"
	"net/http"
)

// encodedForClient gives the response view the client leg is written from.
// When CompressedResponseModifier decoded the origin's body and nothing after
// it changed the decoded body or set a Content-Encoding, the client gets the
// origin's encoded body and its Content-Encoding and Content-Length values as
// read; every other edit to the view still applies. Otherwise res itself,
// whose decoded body is sent without Content-Encoding and re-framed
// (docs/adr/2026-11-17-compressed-responses-are-transparent-on-the-wire-and-decoded-in-storage.md).
// Storage always reads res, so it keeps the decoded body.
func encodedForClient(pair *Pair, res *http.Response) (*http.Response, error) {
	decoded := pair.decoded
	if decoded == nil || res.Header.Values("Content-Encoding") != nil {
		return res, nil
	}
	payload, err := readViewBody(&res.Body)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(payload, decoded.decoded) {
		return res, nil
	}
	header := res.Header.Clone()
	for name, values := range decoded.framing {
		if len(values) == 0 {
			delete(header, name)
		} else {
			header[name] = values
		}
	}
	client := *res
	client.Header = header
	client.ContentLength = -1
	client.Body = io.NopCloser(bytes.NewReader(decoded.coded))
	return &client, nil
}
