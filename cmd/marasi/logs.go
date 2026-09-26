package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

var logsLimit string
var logsCursor string

func init() {
	logsCmd.Flags().StringVar(&logsLimit, "limit", "200", "Page size")
	logsCmd.Flags().StringVar(&logsCursor, "cursor", "", "Fetch the next older page")
	rootCmd.AddCommand(logsCmd)
}

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "List proxy logs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/logs?"+logsQuery(), "listing logs", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeProxyLogsHuman(body, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func logsQuery() string {
	query := url.Values{"limit": {logsLimit}}
	if logsCursor != "" {
		query.Set("cursor", logsCursor)
	}
	return query.Encode()
}

func writeProxyLogsHuman(body []byte, stdout, stderr io.Writer) error {
	var page struct {
		Items []struct {
			Timestamp   string  `json:"timestamp"`
			Level       string  `json:"level"`
			Message     string  `json:"message"`
			RequestID   *string `json:"request_id"`
			ExtensionID *string `json:"extension_id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decoding logs: %w", err)
	}
	for i := len(page.Items) - 1; i >= 0; i-- {
		entry := page.Items[i]
		if _, err := fmt.Fprintf(stdout, "%s %s %s", entry.Timestamp, entry.Level, entry.Message); err != nil {
			return err
		}
		if entry.RequestID != nil {
			if _, err := fmt.Fprintf(stdout, " request=%s", *entry.RequestID); err != nil {
				return err
			}
		}
		if entry.ExtensionID != nil {
			if _, err := fmt.Fprintf(stdout, " extension=%s", *entry.ExtensionID); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return err
		}
	}
	if page.NextCursor != nil {
		_, err := fmt.Fprintf(stderr, "next_cursor=%s\n", *page.NextCursor)
		return err
	}
	return nil
}
