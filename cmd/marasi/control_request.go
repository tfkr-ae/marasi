package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// runControlRequest sends one request to the instance's control socket and
// returns the response body of a 200. Any other status is an error naming
// operation. SIGINT or SIGTERM cancels the request.
func runControlRequest(cmd *cobra.Command, method, path, operation string, body []byte) ([]byte, error) {
	return runControlRequestStatus(cmd, method, path, operation, body, http.StatusOK)
}

// runControlRequestStatus is runControlRequest for a route whose success
// status is wantStatus rather than 200.
func runControlRequestStatus(cmd *cobra.Command, method, path, operation string, body []byte, wantStatus int) ([]byte, error) {
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	return sendControlRequest(cmd, method, path, operation, "application/json", requestBody, wantStatus)
}

// sendControlRequest is runControlRequestStatus for a body of any content
// type, streamed from body rather than held in memory.
func sendControlRequest(cmd *cobra.Command, method, path, operation, contentType string, body io.Reader, wantStatus int) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	response, err := callInstance(ctx, method, path, contentType, body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == wantStatus {
		return response.Body, nil
	}
	if response.StatusCode == http.StatusOK {
		return nil, fmt.Errorf("%s: unexpected status %s", operation, response.Status)
	}
	return nil, rejectInstanceStatus(operation, response, true)
}
