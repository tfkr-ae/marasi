package marasi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
)

// ArmoryService defines the Armory operations exposed by the proxy.
type ArmoryService interface {
	Repo() domain.ArmoryRepository
	ValidateRun(*domain.ArmoryRun) error
	StartRun(uuid.UUID) error
	CancelRun(uuid.UUID) error
	ActiveRunIDs() []uuid.UUID
}

const armoryRunIDHeader = "x-armory-run-id"

// SendArmoryRequest sends a rendered Armory request through the proxy client.
func (proxy *Proxy) SendArmoryRequest(ctx context.Context, raw string, runID uuid.UUID, useHTTPS bool) error {
	if ctx == nil {
		return errors.New("request context is required")
	}
	if runID == uuid.Nil {
		return errors.New("armory run ID is required")
	}

	updated, err := rawhttp.RecalculateContentLength([]byte(raw))
	if err != nil {
		return fmt.Errorf("recalculating content length: %w", err)
	}

	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(updated)))
	if err != nil {
		return fmt.Errorf("reading HTTP request: %w", err)
	}
	if req.Host == "" {
		return errors.New("host header not found or is empty")
	}

	scheme := "http"
	if useHTTPS {
		scheme = "https"
	}

	req.RequestURI = ""
	req.URL.Scheme = scheme
	req.URL.Host = req.Host
	req.Header.Set(armoryRunIDHeader, runID.String())
	req = req.WithContext(ctx)

	if _, exists := req.Header["User-Agent"]; !exists {
		req.Header.Set("User-Agent", "")
	}

	res, err := proxy.Client.Do(req)
	if err != nil {
		return fmt.Errorf("sending armory request: %w", err)
	}
	defer res.Body.Close()

	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}
