package marasi

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// ArmoryService defines the Armory operations exposed by the proxy.
type ArmoryService interface {
	Repo() domain.ArmoryRepository
	ValidateRun(*domain.ArmoryRun) error
	StartRun(uuid.UUID) error
	CancelRun(uuid.UUID) error
	ActiveRunIDs() []uuid.UUID
	Shutdown()
}

const armoryRunIDHeader = "x-armory-run-id"

// SendArmoryRequest sends a rendered Armory request raw through the proxy's
// own listener, on a connection of its own, as a launch is sent. It returns
// once the response has been read. Cancelling ctx abandons the request.
func (proxy *Proxy) SendArmoryRequest(ctx context.Context, raw string, runID uuid.UUID, useHTTPS bool) error {
	if ctx == nil {
		return errors.New("request context is required")
	}
	if runID == uuid.Nil {
		return errors.New("armory run ID is required")
	}
	request, err := readRawRequest(raw, useHTTPS)
	if err != nil {
		return err
	}
	res, err := proxy.sendRaw(ctx, request, [][2]string{{armoryRunIDHeader, runID.String()}})
	if err != nil {
		return fmt.Errorf("sending armory request: %w", err)
	}
	if res.upgrade != nil {
		res.upgrade.Close()
	}
	return nil
}
