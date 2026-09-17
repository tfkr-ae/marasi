package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

var waypointHostname string
var waypointOverride string

type waypointRequest struct {
	Hostname string `json:"hostname"`
	Override string `json:"override"`
}

func init() {
	waypointAddCmd.Flags().StringVar(&waypointHostname, "hostname", "", "Original host and port")
	waypointAddCmd.Flags().StringVar(&waypointOverride, "override", "", "Override host and port")
	waypointAddCmd.MarkFlagRequired("hostname")
	waypointAddCmd.MarkFlagRequired("override")
	waypointCmd.AddCommand(waypointListCmd, waypointAddCmd)
	rootCmd.AddCommand(waypointCmd)
}

var waypointCmd = &cobra.Command{
	Use:   "waypoint",
	Short: "Manage waypoints for a service instance",
}

var waypointListCmd = &cobra.Command{
	Use:   "list",
	Short: "List waypoints",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runWaypointRequest(cmd, http.MethodGet, nil, "listing waypoints")
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeWaypointList(body, cmd.OutOrStdout())
	},
}

var waypointAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a waypoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := json.Marshal(waypointRequest{Hostname: waypointHostname, Override: waypointOverride})
		if err != nil {
			return fmt.Errorf("encoding waypoint request: %w", err)
		}
		response, err := runWaypointRequest(cmd, http.MethodPost, body, "adding waypoint")
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(response)
		} else {
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "waypoint %s added\n", strings.TrimSpace(waypointHostname))
		}
		return err
	},
}

func runWaypointRequest(cmd *cobra.Command, method string, body []byte, operation string) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi/waypoint", requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating waypoint request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("instance %s is not running", instance)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(wrapError("reading waypoint response", readErr), wrapError("closing waypoint response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return nil, controlAPIError(operation, response.Status, responseBody)
	}
	return responseBody, nil
}

func writeWaypointList(body []byte, stdout io.Writer) error {
	var response struct {
		Items []waypointRequest `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding waypoint list: %w", err)
	}
	for index, waypoint := range response.Items {
		if index > 0 {
			if _, err := fmt.Fprintln(stdout); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "hostname: %s\noverride: %s\n", waypoint.Hostname, waypoint.Override); err != nil {
			return err
		}
	}
	return nil
}
