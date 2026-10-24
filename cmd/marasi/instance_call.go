package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// instanceNotRunningError reports that no instance answers on the named
// instance's control socket.
type instanceNotRunningError struct {
	name string
}

func (e instanceNotRunningError) Error() string {
	return fmt.Sprintf("instance %s is not running", e.name)
}

type instanceResponse struct {
	StatusCode int
	Status     string
	Body       []byte
}

// callInstance calls this process's instance and reads one control response.
func callInstance(ctx context.Context, method, path, contentType string, body io.Reader) (instanceResponse, error) {
	return dialInstance(ctx, endpoint, method, path, contentType, body)
}

// dialInstance calls the instance at target and reads one control response.
func dialInstance(ctx context.Context, target instanceEndpoint, method, path, contentType string, body io.Reader) (instanceResponse, error) {
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
	client := target.client()
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return instanceResponse{}, ctx.Err()
		}
		return instanceResponse{}, instanceNotRunningError{name: target.label}
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
	if asJSON || explainsRefusal(response) {
		return controlAPIError(operation, response.Status, response.Body)
	}
	return fmt.Errorf("%s: %s", operation, response.Status)
}

// rejectLaunchpadStatus is rejectInstanceStatus for the launchpad routes,
// whose refusals carry an error code such as invalid_placement,
// already_linked or not_found. Human mode shows that code too, as it does for
// launch_pending, rather than the bare HTTP status.
func rejectLaunchpadStatus(operation string, response instanceResponse, asJSON bool) error {
	if code, _ := controlRefusal(response); code != "" && response.StatusCode >= 400 && response.StatusCode < 500 {
		return controlAPIError(operation, response.Status, response.Body)
	}
	return rejectInstanceStatus(operation, response, asJSON)
}
