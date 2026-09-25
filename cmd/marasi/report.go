package main

import (
	"errors"
	"net/http"

	"github.com/spf13/cobra"
)

func init() {
	reportTemplateCmd.AddCommand(reportTemplateListCmd)
	reportCmd.AddCommand(reportTemplateCmd)
	rootCmd.AddCommand(reportCmd)
}

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Manage reports for a service instance",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return errors.New("report requires a subcommand")
	},
}

var reportTemplateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage report templates for a service instance",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return errors.New("report template requires a subcommand")
	},
}

var reportTemplateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List report templates",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/report/template", "listing report templates", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeSizedList(body, cmd.OutOrStdout())
	},
}
