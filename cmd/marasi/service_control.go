package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

func init() {
	serviceControlTokenCmd.Flags().Bool("rotate", false, "replace the control token, drop connections that used the old one, and print the new one")
	serviceControlCmd.AddCommand(serviceControlTokenCmd)
	serviceCmd.AddCommand(serviceControlCmd)
}

var serviceControlCmd = &cobra.Command{
	Use:   "control",
	Short: "Manage the control listener's network address and control token",
}

var serviceControlTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print the control token",
	Long:  "Print the control token that every request to the control listener's network address must send as Authorization: Bearer. Only the socket returns it, and Marasi generates it on first use. With --rotate, replace it first: the old token stops working at once, and connections and event streams that used it are dropped.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		rotate, err := cmd.Flags().GetBool("rotate")
		if err != nil {
			return err
		}
		method, path, operation := http.MethodGet, "/service/control-listener/token", "reading control token"
		if rotate {
			method, path, operation = http.MethodPost, "/service/control-listener/token/rotate", "rotating control token"
		}
		body, err := runControlRequestStatus(cmd, method, path, operation, nil, http.StatusOK)
		if err != nil {
			return withStartHint(err)
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var response struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &response); err != nil || response.Token == "" {
			return fmt.Errorf("decoding control token: %s", body)
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), response.Token); err != nil {
			return fmt.Errorf("writing control token: %w", err)
		}
		return nil
	},
}

// parseControlAddress reads a control listener network address given as
// HOST:PORT.
func parseControlAddress(value string) (service.ListenerSettings, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return service.ListenerSettings{}, fmt.Errorf("invalid --control-address %q: want HOST:PORT: %w", value, err)
	}
	if host == "" {
		return service.ListenerSettings{}, fmt.Errorf("invalid --control-address %q: want HOST:PORT with a host", value)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return service.ListenerSettings{}, fmt.Errorf("invalid --control-address %q: port must be 0 to 65535", value)
	}
	portValue := uint16(port)
	return service.ListenerSettings{Address: &host, Port: &portValue}, nil
}
