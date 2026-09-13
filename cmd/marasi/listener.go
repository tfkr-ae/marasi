package main

import (
	"bytes"
	"context"
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

var listenerStartAddress string
var listenerStartPort decimalPort
var listenerUpdateAddress string
var listenerUpdatePort decimalPort

type listenerRequest struct {
	Address *string `json:"address,omitempty"`
	Port    *uint16 `json:"port,omitempty"`
}

func init() {
	listenerStartCmd.Flags().StringVar(&listenerStartAddress, "address", "", "Proxy listener address")
	listenerStartCmd.Flags().Var(&listenerStartPort, "port", "Proxy listener port")
	listenerUpdateCmd.Flags().StringVar(&listenerUpdateAddress, "address", "", "Proxy listener address")
	listenerUpdateCmd.Flags().Var(&listenerUpdatePort, "port", "Proxy listener port")
	listenerCmd.AddCommand(listenerStartCmd, listenerStopCmd, listenerUpdateCmd, listenerStatusCmd)
	rootCmd.AddCommand(listenerCmd)
}

var listenerCmd = &cobra.Command{
	Use:   "listener",
	Short: "Manage a service instance's proxy listener",
}

var listenerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the proxy listener",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		settings := listenerSettings(cmd, listenerStartAddress, listenerStartPort)
		return runListenerCommand(cmd, settings)
	},
}

var listenerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the proxy listener",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runListenerCommand(cmd, listenerRequest{})
	},
}

var listenerUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the proxy listener",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		settings := listenerSettings(cmd, listenerUpdateAddress, listenerUpdatePort)
		if settings.Address == nil && settings.Port == nil {
			return errors.New("listener update requires --address or --port")
		}
		return runListenerCommand(cmd, settings)
	},
}

var listenerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report the proxy listener's status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runListenerCommand(cmd, listenerRequest{})
	},
}

func listenerSettings(cmd *cobra.Command, address string, port decimalPort) listenerRequest {
	var settings listenerRequest
	if cmd.Flags().Changed("address") {
		settings.Address = &address
	}
	if cmd.Flags().Changed("port") {
		value := uint16(port)
		settings.Port = &value
	}
	return settings
}

func runListenerCommand(cmd *cobra.Command, settings listenerRequest) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return controlListener(ctx, instancePath, instance, jsonOutput, cmd.Name(), settings, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func controlListener(ctx context.Context, instancePath, instanceName string, asJSON bool, action string, settings listenerRequest, stdout, stderr io.Writer) error {
	method := http.MethodPost
	operation := "starting proxy listener"
	if action == "update" {
		operation = "updating proxy listener"
	} else if action == "status" {
		method = http.MethodGet
		operation = "getting proxy listener status"
	} else if action == "stop" {
		operation = "stopping proxy listener"
	}
	path := "/listener/" + action

	var requestBody io.Reader
	if settings.Address != nil || settings.Port != nil {
		body, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("encoding proxy listener request: %w", err)
		}
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return fmt.Errorf("creating proxy listener request: %w", err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("instance %s is not running", instanceName)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(
			wrapError("reading proxy listener response", readErr),
			wrapError("closing proxy listener response", closeErr),
		)
	}
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return controlAPIError(operation, response.Status, body)
		}
		return fmt.Errorf("%s: %s", operation, response.Status)
	}
	if asJSON {
		if _, err := stdout.Write(body); err != nil {
			return fmt.Errorf("writing proxy listener response: %w", err)
		}
		return nil
	}

	status, proxyListener, err := decodeListenerStatus(body)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		if status != "active" {
			return errors.New("invalid listener status: start did not return active status")
		}
		_, err = fmt.Fprintf(stderr, "proxy listener started on %s\n", proxyListener)
	case "update":
		if status != "active" {
			return errors.New("invalid listener status: update did not return active status")
		}
		_, err = fmt.Fprintf(stderr, "proxy listener updated to %s\n", proxyListener)
	case "stop":
		if status != "inactive" {
			return errors.New("invalid listener status: stop did not return inactive status")
		}
		_, err = fmt.Fprintln(stderr, "proxy listener stopped successfully")
	case "status":
		_, err = fmt.Fprintf(stdout, "status: %s\nproxy listener: %s\n", status, proxyListener)
	}
	if err != nil {
		return fmt.Errorf("writing proxy listener result: %w", err)
	}
	return nil
}

func decodeListenerStatus(body []byte) (string, string, error) {
	var response struct {
		Status        *string         `json:"status"`
		ProxyListener json.RawMessage `json:"proxy_listener"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", "", fmt.Errorf("decoding listener status: %w", err)
	}
	if response.Status == nil {
		return "", "", errors.New("invalid listener status: status is missing")
	}
	switch *response.Status {
	case "active":
		var address string
		if err := json.Unmarshal(response.ProxyListener, &address); err != nil || address == "" {
			return "", "", errors.New("invalid listener status: active proxy_listener must be a non-empty string")
		}
		return "active", address, nil
	case "inactive":
		if string(response.ProxyListener) != "null" {
			return "", "", errors.New("invalid listener status: inactive proxy_listener must be null")
		}
		return "inactive", "inactive", nil
	default:
		return "", "", errors.New("invalid listener status: status must be active or inactive")
	}
}
