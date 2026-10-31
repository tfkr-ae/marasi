package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

func init() {
	scopeCmd.AddCommand(scopeCheckCmd, scopeListCmd)
	rootCmd.AddCommand(scopeCmd)
}

var scopeCmd = &cobra.Command{
	Use:   "scope",
	Short: "List scope rules or check whether a URL is in scope",
	Args:  cobra.NoArgs,
	RunE:  showSubcommandHelp,
}

var scopeCheckCmd = &cobra.Command{
	Use:   "check URL",
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

var scopeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the scope rules and default policy",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runControlRequest(cmd, http.MethodGet, "/scope", "listing scope rules", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, response)
		}
		return writeScopeListHuman(response, cmd.OutOrStdout())
	},
}

func writeScopeListHuman(body []byte, stdout io.Writer) error {
	type rule struct {
		Pattern   string `json:"pattern"`
		MatchType string `json:"match_type"`
	}
	var response struct {
		DefaultAllow bool   `json:"default_allow"`
		Include      []rule `json:"include"`
		Exclude      []rule `json:"exclude"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding scope list response: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "default_allow: %t\n", response.DefaultAllow); err != nil {
		return err
	}
	table := newListTable(stdout, "RULE", "MATCH TYPE", "PATTERN")
	for _, rule := range response.Include {
		table.row("include", rule.MatchType, rule.Pattern)
	}
	for _, rule := range response.Exclude {
		table.row("exclude", rule.MatchType, rule.Pattern)
	}
	return table.flush()
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
