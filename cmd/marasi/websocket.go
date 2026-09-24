package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

func init() {
	websocketCmd.AddCommand(websocketGetCmd)
	rootCmd.AddCommand(websocketCmd)
	trafficCmd.AddCommand(trafficWebSocketCmd)
}

var websocketCmd = &cobra.Command{
	Use:   "websocket",
	Short: "Inspect WebSocket connections",
}

var websocketGetCmd = &cobra.Command{
	Use:   "get CONNECTION_ID",
	Short: "Get one WebSocket connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return getWebSocketConnection(cmd, "/websocket/"+args[0], "getting websocket connection")
	},
}

var trafficWebSocketCmd = &cobra.Command{
	Use:   "websocket REQUEST_ID",
	Short: "Get the WebSocket connection opened by a traffic pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return getWebSocketConnection(cmd, "/traffic/"+args[0]+"/websocket", "getting traffic websocket")
	},
}

func getWebSocketConnection(cmd *cobra.Command, path, operation string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi"+path, nil)
	if err != nil {
		return fmt.Errorf("creating websocket request: %w", err)
	}
	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("instance %s is not running", instance)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(wrapError("reading websocket response", readErr), wrapError("closing websocket response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return controlAPIError(operation, response.Status, body)
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}
	return writeWebSocketConnectionHuman(body, cmd.OutOrStdout())
}

func writeWebSocketConnectionHuman(body []byte, stdout io.Writer) error {
	var connection struct {
		ID          string  `json:"id"`
		RequestID   string  `json:"request_id"`
		State       string  `json:"state"`
		Transport   string  `json:"transport"`
		Host        string  `json:"host"`
		Path        string  `json:"path"`
		StartedAt   string  `json:"started_at"`
		ClosedAt    *string `json:"closed_at"`
		CloseCode   int     `json:"close_code"`
		CloseReason string  `json:"close_reason"`
	}
	if err := json.Unmarshal(body, &connection); err != nil {
		return fmt.Errorf("decoding websocket connection: %w", err)
	}
	closedAt := "null"
	if connection.ClosedAt != nil {
		closedAt = *connection.ClosedAt
	}
	_, err := fmt.Fprintf(stdout,
		"id: %s\nrequest_id: %s\nstate: %s\ntransport: %s\nhost: %s\npath: %s\nstarted_at: %s\nclosed_at: %s\nclose_code: %d\nclose_reason: %s\n",
		connection.ID,
		connection.RequestID,
		connection.State,
		connection.Transport,
		connection.Host,
		connection.Path,
		connection.StartedAt,
		closedAt,
		connection.CloseCode,
		connection.CloseReason,
	)
	return err
}
