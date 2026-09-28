package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
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
	listenerStartCmd.Flags().StringVar(&listenerStartAddress, "address", "", "Host or IP, without a port")
	listenerStartCmd.Flags().Var(&listenerStartPort, "port", "TCP port")
	listenerUpdateCmd.Flags().StringVar(&listenerUpdateAddress, "address", "", "Host or IP, without a port")
	listenerUpdateCmd.Flags().Var(&listenerUpdatePort, "port", "TCP port")
	listenerCmd.AddCommand(listenerStartCmd, listenerStopCmd, listenerUpdateCmd, listenerStatusCmd)
	listenerCmd.AddCommand(listenerAddressCmd)
	rootCmd.AddCommand(listenerCmd)
}

var listenerCmd = &cobra.Command{
	Use:   "listener",
	Short: "Start, stop, and update the proxy listener",
}

var listenerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the proxy listener",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		settings := listenerSettings(cmd, listenerStartAddress, listenerStartPort)
		if settings.Address == nil || settings.Port == nil {
			return errors.New("listener start requires --address and --port")
		}
		return runListenerCommand(cmd, http.MethodPost, "/listener/start", "starting proxy listener", "start", settings)
	},
}

var listenerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the proxy listener",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runListenerCommand(cmd, http.MethodPost, "/listener/stop", "stopping proxy listener", "stop", listenerRequest{})
	},
}

var listenerUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the proxy listener",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		settings := listenerSettings(cmd, listenerUpdateAddress, listenerUpdatePort)
		if settings.Address == nil || settings.Port == nil {
			return errors.New("listener update requires --address and --port")
		}
		return runListenerCommand(cmd, http.MethodPost, "/listener/update", "updating proxy listener", "update", settings)
	},
}

var listenerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print the proxy listener status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runListenerCommand(cmd, http.MethodGet, "/listener/status", "getting proxy listener status", "status", listenerRequest{})
	},
}

var listenerAddressCmd = &cobra.Command{
	Use:   "address",
	Short: "Print the active proxy listener address",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runListenerCommand(cmd, http.MethodGet, "/listener/status", "getting proxy listener address", "address", listenerRequest{})
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

func runListenerCommand(cmd *cobra.Command, method, path, operation, action string, settings listenerRequest) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return controlListener(ctx, method, path, operation, action, jsonOutput, settings, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func controlListener(ctx context.Context, method, path, operation, action string, asJSON bool, settings listenerRequest, stdout, stderr io.Writer) error {
	var requestBody io.Reader
	if settings.Address != nil || settings.Port != nil {
		body, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("encoding proxy listener request: %w", err)
		}
		requestBody = bytes.NewReader(body)
	}
	response, err := callInstance(ctx, method, path, "application/json", requestBody)
	if err != nil {
		return err
	}
	if err := rejectInstanceStatus(operation, response, asJSON); err != nil {
		return err
	}
	body := response.Body
	if action == "address" {
		status, proxyListener, decodeErr := decodeListenerStatus(body)
		if decodeErr != nil {
			return decodeErr
		}
		if status != "active" {
			return errors.New("proxy listener is inactive")
		}
		if asJSON {
			if encodeErr := json.NewEncoder(stdout).Encode(struct {
				ProxyListener string `json:"proxy_listener"`
			}{ProxyListener: proxyListener}); encodeErr != nil {
				return fmt.Errorf("writing proxy listener address: %w", encodeErr)
			}
			return nil
		}
		if _, writeErr := fmt.Fprintln(stdout, proxyListener); writeErr != nil {
			return fmt.Errorf("writing proxy listener address: %w", writeErr)
		}
		return nil
	}
	if asJSON {
		if _, writeErr := stdout.Write(body); writeErr != nil {
			return fmt.Errorf("writing proxy listener response: %w", writeErr)
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
		if !validListenerEndpoint(address) {
			return "", "", errors.New("invalid listener status: active proxy_listener must be a valid endpoint")
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

func validListenerEndpoint(endpoint string) bool {
	address, err := netip.ParseAddrPort(endpoint)
	return err == nil && address.Port() != 0
}
