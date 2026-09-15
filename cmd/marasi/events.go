package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

func init() {
	rootCmd.AddCommand(eventsCmd)
}

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Subscribe to service instance events",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return printEvents(ctx, instancePath, instance, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// printEvents subscribes to the instance and prints named events until the connection closes.
func printEvents(ctx context.Context, instancePath, instanceName string, stdout, stderr io.Writer) error {
	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/events", nil)
	if err != nil {
		return fmt.Errorf("creating events request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("instance %s is not running", instanceName)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("subscribing to events: %s", response.Status)
	}

	scanner := bufio.NewScanner(response.Body)
	connected := false
	eventName := ""
	data := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == ": connected":
			connected = true
			if _, err := fmt.Fprintln(stderr, ": connected"); err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		case line == "" && eventName != "":
			if _, err := fmt.Fprintf(stdout, "%s %s\n", eventName, data); err != nil {
				return err
			}
			eventName, data = "", ""
		}
	}
	if connected && (scanner.Err() == nil || ctx.Err() != nil) {
		return nil
	}
	if scanner.Err() != nil {
		return fmt.Errorf("reading events: %w", scanner.Err())
	}
	return fmt.Errorf("instance %s closed the event subscription before connecting", instanceName)
}
