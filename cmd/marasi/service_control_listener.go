package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

// controlListenerCommand is one service control subcommand: the route it
// calls and how it reports the result.
type controlListenerCommand struct {
	name      string // subcommand name, as in service control NAME
	short     string
	method    string
	route     string // under /service/control-listener
	operation string // gerund phrase that wraps a failure
	// binds is true for a command that binds an address from --address and
	// --port, and so may warn that the address is beyond loopback.
	binds bool
	// report writes the human result; status is the decoded response.
	report func(stdout, stderr io.Writer, status service.ControlListenerStatus) error
}

var (
	controlStartCommand = controlListenerCommand{
		name:      "start",
		short:     "Serve the control API at a network address, guarded by the control password when one is set",
		method:    http.MethodPost,
		route:     "/start",
		operation: "starting control listener network address",
		binds:     true,
		report: func(_, stderr io.Writer, status service.ControlListenerStatus) error {
			_, err := fmt.Fprintf(stderr, "control listener network address started on %s\n", *status.ControlListener)
			return err
		},
	}
	controlUpdateCommand = controlListenerCommand{
		name:      "update",
		short:     "Move the control listener's network address or change its control password",
		method:    http.MethodPost,
		route:     "/update",
		operation: "updating control listener network address",
		binds:     true,
		report: func(_, stderr io.Writer, status service.ControlListenerStatus) error {
			_, err := fmt.Fprintf(stderr, "control listener network address updated to %s\n", *status.ControlListener)
			return err
		},
	}
	controlStopCommand = controlListenerCommand{
		name:      "stop",
		short:     "Close the control listener's network address; the socket stays available",
		method:    http.MethodPost,
		route:     "/stop",
		operation: "stopping control listener network address",
		report: func(_, stderr io.Writer, _ service.ControlListenerStatus) error {
			_, err := fmt.Fprintln(stderr, "control listener network address stopped successfully")
			return err
		},
	}
	controlStatusCommand = controlListenerCommand{
		name:      "status",
		short:     "Print the control listener's network address and whether a password guards it",
		method:    http.MethodGet,
		route:     "/status",
		operation: "getting control listener status",
		report: func(stdout, _ io.Writer, status service.ControlListenerStatus) error {
			address := "none"
			if status.ControlListener != nil {
				address = *status.ControlListener
			}
			password := "none"
			if status.PasswordSet {
				password = "set"
			}
			_, err := fmt.Fprintf(stdout, "status: %s\ncontrol listener network address: %s\npassword: %s\n", status.Status, address, password)
			return err
		},
	}
)

func init() {
	for _, command := range []controlListenerCommand{controlStartCommand, controlUpdateCommand, controlStopCommand, controlStatusCommand} {
		serviceControlCmd.AddCommand(command.cobraCommand())
	}
}

// controlListenerRequest is the body of service control start and update:
// the address to bind, and the control password to set. Password nil and
// NoPassword false keep the current password.
type controlListenerRequest struct {
	listenerRequest
	Password   *string `json:"password,omitempty"`
	NoPassword bool    `json:"no_password,omitempty"`
}

// cobraCommand builds the subcommand. A command that binds sends --address
// and --port, which start requires and update may leave out to keep the
// address, plus the control password to set.
func (c controlListenerCommand) cobraCommand() *cobra.Command {
	var address string
	var port decimalPort
	var password string
	var noPassword bool
	command := &cobra.Command{
		Use:   c.name,
		Short: c.short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !c.binds {
				return c.run(cmd, nil)
			}
			request := controlListenerRequest{listenerRequest: listenerSettings(cmd, address, port)}
			newPassword, err := passwordToSet(cmd, password, noPassword)
			if err != nil {
				return err
			}
			if newPassword != nil && *newPassword == "" {
				request.NoPassword = true
			} else {
				request.Password = newPassword
			}
			bindsAddress := request.Address != nil && request.Port != nil
			switch {
			case c.name == controlStartCommand.name && !bindsAddress:
				return fmt.Errorf("service control %s requires --address and --port", c.name)
			case (request.Address == nil) != (request.Port == nil):
				return fmt.Errorf("service control %s requires --address and --port together", c.name)
			case !bindsAddress && newPassword == nil:
				return fmt.Errorf("service control %s requires --address and --port, --password, or --no-password", c.name)
			}
			return c.run(cmd, &request)
		},
	}
	if c.binds {
		command.Flags().StringVar(&address, "address", "", "Host or IP, without a port")
		command.Flags().Var(&port, "port", "TCP port; 0 picks a free port")
		// This --password shadows the root one: it is the password to set,
		// and with --remote the password to authenticate comes from
		// MARASI_PASSWORD alone.
		command.Flags().StringVar(&password, "password", "", "Control password to set on the instance, replacing the current one (env "+passwordVariable+" without --remote; with --remote, "+passwordVariable+" authenticates and only this flag sets a new one)")
		command.Flags().BoolVar(&noPassword, "no-password", false, "Remove the control password, so the network address needs none")
		command.MarkFlagsMutuallyExclusive("password", "no-password")
	}
	return command
}

// passwordToSet returns the control password a start or update sets:
// --password, or MARASI_PASSWORD when the command talks to the socket. It
// returns "" for --no-password, and nil to keep the current password.
func passwordToSet(cmd *cobra.Command, password string, noPassword bool) (*string, error) {
	source := "--password"
	switch {
	case noPassword:
		return new(string), nil
	case cmd.Flags().Changed("password"):
	case remoteURL == "" && os.Getenv(passwordVariable) != "":
		password, source = os.Getenv(passwordVariable), passwordVariable
	default:
		return nil, nil
	}
	if err := service.CheckControlPassword(password); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", source, err)
	}
	return &password, nil
}

// run sends the command's request, with settings as the body when given.
func (c controlListenerCommand) run(cmd *cobra.Command, settings *controlListenerRequest) error {
	var body []byte
	if settings != nil {
		encoded, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("encoding control listener request: %w", err)
		}
		body = encoded
	}
	response, err := runControlRequest(cmd, c.method, "/service/control-listener"+c.route, c.operation, body)
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
	if c.binds && status.ControlListener == nil {
		return errors.New("decoding control listener status: no control_listener")
	}
	if c.binds && writesToTerminal(cmd.ErrOrStderr()) {
		// The warning goes to a terminal's stderr in both output modes, as it
		// does when service start binds the address. The instance log
		// carries it either way.
		if warning := service.ControlAddressWarning(*status.ControlListener); warning != "" {
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), warning); err != nil {
				return fmt.Errorf("writing control listener warning: %w", err)
			}
		}
	}
	if jsonOutput {
		return writeJSONBody(cmd, response)
	}
	if err := c.report(cmd.OutOrStdout(), cmd.ErrOrStderr(), status); err != nil {
		return fmt.Errorf("writing control listener result: %w", err)
	}
	return nil
}
