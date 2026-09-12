package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var trafficJSON bool
var trafficListHost string
var trafficListMethod string
var trafficListStatusCode string
var trafficListPath string
var trafficListLimit string
var trafficListCursor string

func init() {
	trafficCmd.PersistentFlags().BoolVar(&trafficJSON, "json", false, "Print the control API response body")
	trafficListCmd.Flags().StringVar(&trafficListHost, "host", "", "Keep only this exact host")
	trafficListCmd.Flags().StringVar(&trafficListMethod, "method", "", "Keep only this exact method")
	trafficListCmd.Flags().StringVar(&trafficListStatusCode, "status-code", "", "Keep only this exact status code")
	trafficListCmd.Flags().StringVar(&trafficListPath, "path", "", "Keep pairs whose path starts with this prefix")
	trafficListCmd.Flags().StringVar(&trafficListLimit, "limit", "200", "Page size")
	trafficListCmd.Flags().StringVar(&trafficListCursor, "cursor", "", "Fetch the next older page")
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

		return listTraffic(ctx, instancePath, instance, trafficJSON, trafficListQuery(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func trafficListQuery() string {
	query := url.Values{}
	query.Set("limit", trafficListLimit)
	if trafficListHost != "" {
		query.Set("host", trafficListHost)
	}
	if trafficListMethod != "" {
		query.Set("method", trafficListMethod)
	}
	if trafficListStatusCode != "" {
		query.Set("status_code", trafficListStatusCode)
	}
	if trafficListPath != "" {
		query.Set("path", trafficListPath)
	}
	if trafficListCursor != "" {
		query.Set("cursor", trafficListCursor)
	}
	return query.Encode()
}

func listTraffic(ctx context.Context, instancePath, instanceName string, asJSON bool, query string, stdout, stderr io.Writer) error {
	socketPath := instancePath + ".sock"
	client := service.NewClient(socketPath)
	defer client.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/traffic?"+query, nil)
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
	if asJSON {
		if _, err := stdout.Write(body); err != nil {
			return err
		}
	} else if response.StatusCode == http.StatusOK {
		if err := writeTrafficListHuman(body, stdout, stderr); err != nil {
			return err
		}
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("listing traffic: %s", response.Status)
	}
	return nil
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
