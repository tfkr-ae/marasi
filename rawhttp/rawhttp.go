package rawhttp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/beevik/etree"
	"github.com/gabriel-vasile/mimetype"
	"github.com/yosssi/gohtml"
)

// Prettify will atempt to prettify the body or return an empty byte slice if it fails
// JSON, XML, HTML can be prettified if it cannot prettify the body it will return an empty string
func Prettify(bodyBytes []byte) ([]byte, error) {
	if len(bodyBytes) == 0 {
		return []byte{}, nil
	}

	trimmedBody := bytes.TrimSpace(bodyBytes)

	// Check JSON
	var jsonData any

	err := json.Unmarshal(trimmedBody, &jsonData)
	// Contiue if there are no errors
	if err == nil {
		output, err := json.MarshalIndent(jsonData, "", "  ")
		if err != nil {
			return []byte{}, fmt.Errorf("remarshalling JSON: %w", err)
		}
		return output, nil
	}

	// Check XML
	doc := etree.NewDocument()
	err = doc.ReadFromBytes(trimmedBody)
	if err == nil && doc.Root() != nil {
		doc.Indent(1)
		var output bytes.Buffer
		_, err := doc.WriteTo(&output)
		if err != nil {
			return []byte{}, fmt.Errorf("writing indented XML : %w", err)
		}
		// etree adds a newline at the end, which you might not want
		return output.Bytes(), nil
	}

	// Check HTML (mimetype OR prefix)
	contentType := mimetype.Detect(trimmedBody).String()
	if strings.Contains(contentType, "text/html") ||
		(bytes.HasPrefix(trimmedBody, []byte("<")) && !bytes.HasPrefix(trimmedBody, []byte("<?xml"))) {
		output := gohtml.FormatBytes(trimmedBody)

		// Check if gohtml formatted anything
		if !bytes.Equal(output, trimmedBody) && len(output) > 0 {
			return output, nil
		}
	}

	return []byte{}, nil
}

// DumpResponse will take a *http.Response, dumps the raw response and reset the body so it can be consumed
// Returns the full dump, prettified dump, and and error
func DumpResponse(res *http.Response) (rawDump []byte, prettyDump string, error error) {
	responseDump, err := httputil.DumpResponse(res, false)
	if err != nil {
		return []byte{}, "", fmt.Errorf("dumping response : %w", err)
	}

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return []byte{}, "", fmt.Errorf("reading response body: %w", err)
	}
	res.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	fullDump := append(responseDump, bodyBytes...)

	prettified, err := Prettify(bodyBytes)
	if err != nil || len(prettified) == 0 {
		return fullDump, "", nil
	}

	// appending twice with requestDump will lead to truncating fullDump
	prettyHeaders := make([]byte, len(responseDump))
	copy(prettyHeaders, responseDump)

	prettifiedDump := append(prettyHeaders, prettified...)
	return fullDump, string(prettifiedDump), nil
}

// DumpRequest takes a *http.Request, dumps the raw request and resets the body so it can be consumed
// Returns the full dump, prettified dump and an error
func DumpRequest(req *http.Request) (rawDump []byte, prettyDump string, err error) {
	requestDump, err := httputil.DumpRequest(req, false)
	if err != nil {
		return []byte{}, "", fmt.Errorf("dumping request : %w", err)
	}
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return []byte{}, "", fmt.Errorf("reading request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	fullDump := append(requestDump, bodyBytes...)
	prettified, err := Prettify(bodyBytes)

	if err != nil || len(prettified) == 0 {
		return fullDump, "", nil
	}

	// appending twice with requestDump will lead to truncating fullDump
	prettyHeaders := make([]byte, len(requestDump))
	copy(prettyHeaders, requestDump)

	prettifiedDump := append(prettyHeaders, prettified...)
	return fullDump, string(prettifiedDump), nil
}

// Takes a raw request / response and updates the content-length to match the body length
func RecalculateContentLength(raw []byte) (updated []byte, err error) {
	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	parts := bytes.SplitN(normalized, []byte("\n\n"), 2)
	if len(parts) == 2 {
		headers := parts[0]
		body := parts[1]

		headerLines := bytes.Split(headers, []byte("\n"))
		newHeaders := make([][]byte, 0, len(headerLines)+1)
		for _, line := range headerLines {
			if !bytes.HasPrefix(bytes.ToLower(line), []byte("content-length:")) {
				newHeaders = append(newHeaders, line)
			}
		}
		newContentLength := fmt.Sprintf("Content-Length: %d", len(body))
		if len(body) > 0 {
			newHeaders = append(newHeaders, []byte(newContentLength))
		}

		updatedHeaders := bytes.Join(newHeaders, []byte("\r\n"))
		// Reconstruct request with correct Content-Length
		updated := append(updatedHeaders, []byte("\r\n\r\n")...)
		updated = append(updated, body...)
		return updated, nil
	}
	return []byte{}, fmt.Errorf("malformed string : %s", normalized)
}

// RebuildRequest creates a new *http.Request from a raw request slice, it takes the original request context and scheme
func RebuildRequest(raw []byte, originalRequest *http.Request) (req *http.Request, err error) {
	updated, err := RecalculateContentLength(raw)
	if err != nil {
		return nil, fmt.Errorf("recalculating content length : %w", err)
	}
	req, err = http.ReadRequest(bufio.NewReader(bytes.NewReader(updated)))
	if err != nil {
		return nil, fmt.Errorf("reading raw request %s : %w", raw, err)
	}
	req = req.WithContext(originalRequest.Context())
	req.URL.Host = req.Host
	req.URL.Scheme = originalRequest.URL.Scheme
	return req, nil
}

// RebuildResponse creates a new *http.response from a raw response slice
func RebuildResponse(raw []byte, req *http.Request) (res *http.Response, err error) {
	updated, err := RecalculateContentLength(raw)
	if err != nil {
		return nil, fmt.Errorf("recalculating content length : %w", err)
	}
	res, err = http.ReadResponse(bufio.NewReader(bytes.NewReader(updated)), req)
	if err != nil {
		return nil, fmt.Errorf("reading raw response %s : %w", raw, err)
	}
	return res, nil
}
