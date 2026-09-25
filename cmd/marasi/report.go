package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/spf13/cobra"
)

func init() {
	reportTemplateCmd.AddCommand(reportTemplateListCmd, reportTemplateAddCmd)
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

var reportTemplateAddCmd = &cobra.Command{
	Use:   "add PATH",
	Short: "Add a report template",
	Long:  "Add a report template. The source file is moved into the templates directory.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := filepath.Abs(args[0])
		if err != nil {
			return fmt.Errorf("resolving report template path: %w", err)
		}
		requestBody, err := json.Marshal(struct {
			Path string `json:"path"`
		}{Path: path})
		if err != nil {
			return fmt.Errorf("encoding report template add request: %w", err)
		}
		body, err := runControlRequest(cmd, http.MethodPost, "/report/template", "adding report template", requestBody)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
		} else {
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "report template %s added\n", filepath.Base(path))
		}
		return err
	},
}
