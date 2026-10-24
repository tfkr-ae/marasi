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
)

func init() {
	rootCmd.AddCommand(eventsCmd)
}

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Subscribe to service instance events",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		streamConnected = false
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return printEvents(ctx, endpoint, jsonOutput, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var streamConnected bool

// printEvents subscribes to the instance and prints named events until the connection closes.
func printEvents(ctx context.Context, target instanceEndpoint, asJSON bool, stdout, stderr io.Writer) error {
	client := target.client()
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
		return target.unreachable(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("subscribing to events: %s", response.Status)
	}

	connected := false
	var writeErr error
	err = readEventFrames(bufio.NewReader(response.Body), func(line string) error {
		if line != ": connected" {
			return nil
		}
		connected = true
		streamConnected = true
		if asJSON {
			return nil
		}
		if _, writeErr = fmt.Fprintln(stderr, ": connected"); writeErr != nil {
			return writeErr
		}
		writeErr = flushEventsWriter(stderr)
		return writeErr
	}, func(frame eventFrame) error {
		if !connected {
			return nil
		}
		writeErr = writeEvent(stdout, frame.name, frame.data, asJSON)
		return writeErr
	})
	if writeErr != nil {
		return writeErr
	}
	if connected && (err == io.EOF || ctx.Err() != nil) {
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("reading events: %w", err)
	}
	return fmt.Errorf("instance %s closed the event subscription before connecting", target.label)
}

// eventFrame is one named event from GET /events.
type eventFrame struct {
	name string
	data string
}

// readEventFrames reads a GET /events stream, calling comment with each
// comment line and frame with each complete named event, until the stream
// ends or a callback fails. It returns what ended it: the read error
// (io.EOF at the end of the stream) or the callback's error.
func readEventFrames(reader *bufio.Reader, comment func(string) error, frame func(eventFrame) error) error {
	name, data := "", ""
	for {
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			return err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case strings.HasPrefix(line, ":"):
			if comment != nil {
				if err := comment(line); err != nil {
					return err
				}
			}
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		case line == "" && err == nil:
			if name != "" && frame != nil {
				if err := frame(eventFrame{name: name, data: data}); err != nil {
					return err
				}
			}
			name, data = "", ""
		}
		if err != nil {
			return err
		}
	}
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
