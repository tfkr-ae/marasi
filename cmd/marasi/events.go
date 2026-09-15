package main

import (
	"bufio"
	"context"
	"encoding/json"
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
		eventsConnected = false
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return printEvents(ctx, instancePath, instance, jsonOutput, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var eventsConnected bool

// printEvents subscribes to the instance and prints named events until the connection closes.
func printEvents(ctx context.Context, instancePath, instanceName string, asJSON bool, stdout, stderr io.Writer) error {
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
			eventsConnected = true
			if !asJSON {
				if _, err := fmt.Fprintln(stderr, ": connected"); err != nil {
					return err
				}
				if err := flushEventsWriter(stderr); err != nil {
					return err
				}
			}
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		case line == "":
			if connected && eventName != "" {
				if err := writeEvent(stdout, eventName, data, asJSON); err != nil {
					return err
				}
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

func writeEvent(writer io.Writer, name, data string, asJSON bool) error {
	if asJSON {
		encodedName, err := json.Marshal(name)
		if err != nil {
			return err
		}
		line := append([]byte(`{"event":`), encodedName...)
		line = append(line, `,"data":`...)
		line = append(line, data...)
		line = append(line, '}', '\n')
		if _, err := writer.Write(line); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(writer, "%s %s\n", name, data); err != nil {
		return err
	}
	return flushEventsWriter(writer)
}

func flushEventsWriter(writer io.Writer) error {
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}
