package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/tfkr-ae/marasi/service"
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
		return instanceResponse{}, instanceNotRunningError{name: instanceName}
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
	if asJSON || isInvalidRequest(response.StatusCode, response.Body) || isLaunchPending(response) {
		return controlAPIError(operation, response.Status, response.Body)
	}
	return fmt.Errorf("%s: %s", operation, response.Status)
}

// isLaunchPending reports whether the control API refused the request
// because it would take a pending launch's pair out of its launchpad
// (ADR-0053). Its message names the pending pairs, so it is shown in human
// mode too.
func isLaunchPending(response instanceResponse) bool {
	var payload struct {
		Error string `json:"error"`
	}
	return response.StatusCode == http.StatusConflict && json.Unmarshal(response.Body, &payload) == nil && payload.Error == "launch_pending"
}
