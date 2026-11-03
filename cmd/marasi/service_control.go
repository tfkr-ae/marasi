package main

import (
	"fmt"
	"net"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

func init() {
	serviceCmd.AddCommand(serviceControlCmd)
}

var serviceControlCmd = &cobra.Command{
	Use:   "control",
	Short: "Manage the control listener's network address",
	Args:  cobra.NoArgs,
	RunE:  showSubcommandHelp,
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
