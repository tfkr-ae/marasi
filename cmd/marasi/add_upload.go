package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// runAddRequest adds the file at source through route, the add route of the
// wordlists or the report templates, and returns the response body. The file
// is moved: an instance on this machine moves it itself, and a remote
// instance gets its contents, after which the local file is deleted. A path
// sent to a remote instance would name a file on its instance machine that
// the user never saw (docs/adr/2026-10-24-the-control-listener-network-address-is-guarded-by-a-token-alone.md).
func runAddRequest(cmd *cobra.Command, route, source, operation string) ([]byte, error) {
	if !endpoint.remote() {
		requestBody, err := json.Marshal(struct {
			Path string `json:"path"`
		}{Path: source})
		if err != nil {
			return nil, fmt.Errorf("encoding %s request: %w", operation, err)
		}
		return runControlRequest(cmd, http.MethodPost, route, operation, requestBody)
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s is not a regular file", operation, source)
	}
	query := url.Values{"name": {filepath.Base(source)}}.Encode()
	response, err := sendControlRequest(cmd, http.MethodPost, route+"?"+query, operation, "application/octet-stream", file, http.StatusOK)
	if err != nil {
		return nil, err
	}
	// Delete only after the instance has the file, so a failed upload loses
	// nothing. The HTTP client has already closed file, as a request body.
	if err := os.Remove(source); err != nil {
		return nil, fmt.Errorf("%s: uploaded, but removing the local file: %w", operation, err)
	}
	return response, nil
}
