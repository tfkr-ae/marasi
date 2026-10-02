package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

var waypointHostname string
var waypointOverride string

type waypointRequest struct {
	Hostname string `json:"hostname"`
	Override string `json:"override"`
}

type waypointRemoveRequest struct {
	Hostname string `json:"hostname"`
}

func init() {
	waypointAddCmd.Flags().StringVar(&waypointHostname, "hostname", "", "Host and port to match")
	waypointAddCmd.Flags().StringVar(&waypointOverride, "override", "", "Host and port to use instead")
	waypointAddCmd.MarkFlagRequired("hostname")
	waypointAddCmd.MarkFlagRequired("override")
	waypointUpdateCmd.Flags().StringVar(&waypointHostname, "hostname", "", "Host and port to match")
	waypointUpdateCmd.Flags().StringVar(&waypointOverride, "override", "", "Host and port to use instead")
	waypointUpdateCmd.MarkFlagRequired("hostname")
	waypointUpdateCmd.MarkFlagRequired("override")
	waypointRemoveCmd.Flags().StringVar(&waypointHostname, "hostname", "", "Host and port to match")
	waypointRemoveCmd.MarkFlagRequired("hostname")
	waypointCmd.AddCommand(waypointListCmd, waypointAddCmd, waypointUpdateCmd, waypointRemoveCmd)
	rootCmd.AddCommand(waypointCmd)
}

var waypointCmd = &cobra.Command{
	Use:   "waypoint",
	Short: "Map a host and port to another host and port",
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

var waypointUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update a waypoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := json.Marshal(waypointRequest{Hostname: waypointHostname, Override: waypointOverride})
		if err != nil {
			return fmt.Errorf("encoding waypoint request: %w", err)
		}
		response, err := runControlRequest(cmd, http.MethodPost, "/waypoint/update", "updating waypoint", body)
		if err != nil {
			return err
		}
		return writeWaypointMutationResult(cmd, response, "waypoint "+strings.TrimSpace(waypointHostname)+" updated")
	},
}

var waypointRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove a waypoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := json.Marshal(waypointRemoveRequest{Hostname: waypointHostname})
		if err != nil {
			return fmt.Errorf("encoding waypoint request: %w", err)
		}
		response, err := runWaypointRequest(cmd, http.MethodDelete, body, "removing waypoint")
		if err != nil {
			return err
		}
		return writeWaypointMutationResult(cmd, response, "waypoint "+strings.TrimSpace(waypointHostname)+" removed")
	},
}

func runWaypointRequest(cmd *cobra.Command, method string, body []byte, operation string) ([]byte, error) {
	return runControlRequest(cmd, method, "/waypoint", operation, body)
}

func writeWaypointMutationResult(cmd *cobra.Command, response []byte, confirmation string) error {
	if jsonOutput {
		_, err := cmd.OutOrStdout().Write(response)
		return err
	}
	_, err := fmt.Fprintln(cmd.ErrOrStderr(), confirmation)
	return err
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
