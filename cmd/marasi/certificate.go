package main

import (
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

func init() {
	certificateGetCmd.Flags().String("format", "pem", "CA certificate output format: pem or der")
	certificateCmd.AddCommand(certificateGetCmd)
	rootCmd.AddCommand(certificateCmd)
}

var certificateCmd = &cobra.Command{
	Use:   "certificate",
	Short: "Fetch a service instance's CA certificate",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return errors.New("certificate requires a subcommand")
	},
}

var certificateGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get the loaded CA certificate",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if jsonOutput && cmd.Flags().Changed("format") {
			return errors.New("certificate get does not support --format with --json")
		}
		format, err := cmd.Flags().GetString("format")
		if err != nil {
			return err
		}
		if format != "pem" && format != "der" {
			return fmt.Errorf("invalid certificate format: %s", format)
		}

		body, err := runControlRequest(cmd, http.MethodGet, "/certificate", "getting CA certificate", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}

		var response struct {
			PEM string `json:"pem"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("decoding CA certificate response: %w", err)
		}
		if response.PEM == "" {
			return errors.New("decoding CA certificate response: missing pem")
		}
		if format == "pem" {
			_, err = io.WriteString(cmd.OutOrStdout(), response.PEM)
			return err
		}
		block, _ := pem.Decode([]byte(response.PEM))
		if block == nil || block.Type != "CERTIFICATE" {
			return errors.New("decoding CA certificate PEM: invalid certificate block")
		}
		_, err = cmd.OutOrStdout().Write(block.Bytes)
		return err
	},
}
