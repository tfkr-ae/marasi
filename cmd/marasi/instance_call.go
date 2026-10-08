package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/tfkr-ae/marasi/service"
)

type instanceResponse struct {
	StatusCode int
	Status     string
	Body       []byte
}

// callInstance dials this process's instance socket and reads one control response.
func callInstance(ctx context.Context, method, path, contentType string, body io.Reader) (instanceResponse, error) {
	return dialInstance(ctx, instancePath+".sock", instance, method, path, contentType, body)
}

func dialInstance(ctx context.Context, socketPath, instanceName, method, path, contentType string, body io.Reader) (instanceResponse, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, body)
	if err != nil {
		return instanceResponse{}, fmt.Errorf("creating control request: %w", err)
	}
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		request.Header.Set("Content-Type", contentType)
	}
	client := service.NewClient(socketPath)
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return instanceResponse{}, ctx.Err()
		}
		return instanceResponse{}, fmt.Errorf("instance %s is not running", instanceName)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return instanceResponse{}, errors.Join(wrapError("reading control response", readErr), wrapError("closing control response", closeErr))
	}
	return instanceResponse{StatusCode: response.StatusCode, Status: response.Status, Body: responseBody}, nil
}

func rejectInstanceStatus(operation string, response instanceResponse, asJSON bool) error {
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if asJSON {
		return controlAPIError(operation, response.Status, response.Body)
	}
	return fmt.Errorf("%s: %s", operation, response.Status)
}
