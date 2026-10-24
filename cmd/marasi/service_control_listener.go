package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var controlStartAddress string
var controlStartPort decimalPort
var controlUpdateAddress string
var controlUpdatePort decimalPort

func init() {
	serviceControlStartCmd.Flags().StringVar(&controlStartAddress, "address", "", "Host or IP, without a port")
	serviceControlStartCmd.Flags().Var(&controlStartPort, "port", "TCP port; 0 picks a free port")
	serviceControlUpdateCmd.Flags().StringVar(&controlUpdateAddress, "address", "", "Host or IP, without a port")
	serviceControlUpdateCmd.Flags().Var(&controlUpdatePort, "port", "TCP port; 0 picks a free port")
	serviceControlCmd.AddCommand(serviceControlStartCmd, serviceControlUpdateCmd, serviceControlStopCmd, serviceControlStatusCmd)
}

var serviceControlStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Serve the control API at a network address, guarded by the control token",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		settings := listenerSettings(cmd, controlStartAddress, controlStartPort)
		if settings.Address == nil || settings.Port == nil {
			return errors.New("service control start requires --address and --port")
		}
		return runControlListenerCommand(cmd, "/start", "starting control listener network address", &settings)
	},
}

var serviceControlUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Move the control listener's network address",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		settings := listenerSettings(cmd, controlUpdateAddress, controlUpdatePort)
		if settings.Address == nil || settings.Port == nil {
			return errors.New("service control update requires --address and --port")
		}
		return runControlListenerCommand(cmd, "/update", "updating control listener network address", &settings)
	},
}

var serviceControlStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Close the control listener's network address; the socket stays available",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runControlListenerCommand(cmd, "/stop", "stopping control listener network address", nil)
	},
}

var serviceControlStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print the control listener's network address",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runControlListenerCommand(cmd, "/status", "getting control listener status", nil)
	},
}

// runControlListenerCommand sends one control listener request. The status
// route is a GET; the others are POSTs, with settings as the body when given.
func runControlListenerCommand(cmd *cobra.Command, route, operation string, settings *listenerRequest) error {
	method := http.MethodPost
	if route == "/status" {
		method = http.MethodGet
	}
	var body []byte
	if settings != nil {
		encoded, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("encoding control listener request: %w", err)
		}
		body = encoded
	}
	response, err := runControlRequest(cmd, method, "/service/control-listener"+route, operation, body)
	if err != nil {
		return withStartHint(err)
	}
	var status service.ControlListenerStatus
	if err := json.Unmarshal(response, &status); err != nil {
		return fmt.Errorf("decoding control listener status: %w", err)
	}
	if (status.Status == service.ListenerActive) != (status.ControlListener != nil) {
		return fmt.Errorf("decoding control listener status: %s", response)
	}
	if route == "/start" || route == "/update" {
		// The warning goes to stderr in both output modes, as it does when
		// service start binds the address.
		if warning := service.ControlAddressWarning(*status.ControlListener); warning != "" {
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), warning); err != nil {
				return fmt.Errorf("writing control listener warning: %w", err)
			}
		}
	}
	if jsonOutput {
		return writeJSONBody(cmd, response)
	}
	switch route {
	case "/start":
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "control listener network address started on %s\n", *status.ControlListener)
	case "/update":
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "control listener network address updated to %s\n", *status.ControlListener)
	case "/stop":
		_, err = fmt.Fprintln(cmd.ErrOrStderr(), "control listener network address stopped successfully")
	case "/status":
		address := "none"
		if status.ControlListener != nil {
			address = *status.ControlListener
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "status: %s\ncontrol listener network address: %s\n", status.Status, address)
	}
	if err != nil {
		return fmt.Errorf("writing control listener result: %w", err)
	}
	return nil
}
