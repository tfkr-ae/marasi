package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

func init() {
	scopeCmd.AddCommand(scopeCheckCmd)
	rootCmd.AddCommand(scopeCmd)
}

var scopeCmd = &cobra.Command{
	Use:   "scope",
	Short: "Check URLs against a service instance's scope",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return errors.New("scope requires a subcommand")
	},
}

var scopeCheckCmd = &cobra.Command{
	Use:   "check <url>",
	Short: "Check whether a URL is in scope",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := json.Marshal(struct {
			URL string `json:"url"`
		}{URL: args[0]})
		if err != nil {
			return fmt.Errorf("encoding scope check request: %w", err)
		}

		response, err := runControlRequest(cmd, http.MethodPost, "/scope/check", "checking scope", body)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(response)
			return err
		}
		return writeScopeCheckHuman(response, cmd.OutOrStdout())
	},
}

func writeScopeCheckHuman(body []byte, stdout io.Writer) error {
	var response struct {
		InScope   bool   `json:"in_scope"`
		TestedURL string `json:"tested_url"`
		Rule      *struct {
			Pattern   string `json:"pattern"`
			MatchType string `json:"match_type"`
		} `json:"rule"`
		CompassEnabled bool `json:"compass_enabled"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding scope check response: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "tested_url: %s\nin_scope: %t\n", response.TestedURL, response.InScope); err != nil {
		return err
	}
	if response.Rule != nil {
		if _, err := fmt.Fprintf(stdout, "rule: %s\nmatch_type: %s\n", response.Rule.Pattern, response.Rule.MatchType); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "compass_enabled: %t\n", response.CompassEnabled)
	return err
}
