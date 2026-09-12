package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var trafficJSON bool

func init() {
	trafficCmd.PersistentFlags().BoolVar(&trafficJSON, "json", false, "Print the control API response body")
	trafficCmd.AddCommand(trafficListCmd)
	rootCmd.AddCommand(trafficCmd)
}

var trafficCmd = &cobra.Command{
	Use:   "traffic",
	Short: "Inspect traffic for a service instance",
}

var trafficListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the newest page of traffic",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return listTraffic(ctx, instancePath, instance, trafficJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func listTraffic(ctx context.Context, instancePath, instanceName string, asJSON bool, stdout, stderr io.Writer) error {
	socketPath := instancePath + ".sock"
	client := service.NewClient(socketPath)
	defer client.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/traffic?limit=200", nil)
	if err != nil {
		return fmt.Errorf("creating traffic list request: %w", err)
	}
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
			wrapError("reading traffic list response", readErr),
			wrapError("closing traffic list response", closeErr),
		)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("listing traffic: %s: %s", response.Status, body)
	}
	if asJSON {
		_, err := stdout.Write(body)
		return err
	}

	return writeTrafficListHuman(body, stdout, stderr)
}

func writeTrafficListHuman(body []byte, stdout, stderr io.Writer) error {
	var page struct {
		Items []struct {
			ID         string `json:"id"`
			Method     string `json:"method"`
			Host       string `json:"host"`
			Path       string `json:"path"`
			StatusCode int    `json:"status_code"`
			Length     string `json:"length"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decoding traffic list: %w", err)
	}

	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range page.Items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%s\n", item.ID, item.Method, item.Host, item.Path, item.StatusCode, item.Length)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if page.NextCursor != nil {
		fmt.Fprintf(stderr, "next_cursor=%s\n", *page.NextCursor)
	}
	return nil
}
