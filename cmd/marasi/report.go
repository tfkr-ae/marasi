package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/report"
)

var (
	reportExportTitle            string
	reportExportClient           string
	reportExportType             string
	reportExportScope            string
	reportExportAssessor         string
	reportExportStart            string
	reportExportEnd              string
	reportExportDraft            bool
	reportExportTruncate         int
	reportExportIncludeTestCases bool
	reportExportOutput           string
)

func init() {
	reportTemplateCmd.AddCommand(reportTemplateListCmd, reportTemplateAddCmd, reportTemplateRemoveCmd, reportTemplateRestoreCmd)
	reportExportCmd.Flags().StringVar(&reportExportTitle, "title", "", "Report title")
	reportExportCmd.Flags().StringVar(&reportExportClient, "client", "", "Client name")
	reportExportCmd.Flags().StringVar(&reportExportType, "type", "", "Assessment type")
	reportExportCmd.Flags().StringVar(&reportExportScope, "scope", "", "Assessment scope")
	reportExportCmd.Flags().StringVar(&reportExportAssessor, "assessor", "", "Assessor")
	reportExportCmd.Flags().StringVar(&reportExportStart, "start", "", "Assessment start date (YYYY-MM-DD)")
	reportExportCmd.Flags().StringVar(&reportExportEnd, "end", "", "Assessment end date (YYYY-MM-DD)")
	reportExportCmd.Flags().BoolVar(&reportExportDraft, "draft", true, "Mark the report as a draft")
	reportExportCmd.Flags().IntVar(&reportExportTruncate, "truncate", 0, "Maximum body length in bytes")
	reportExportCmd.Flags().BoolVar(&reportExportIncludeTestCases, "include-test-cases", true, "Include test cases")
	reportExportCmd.Flags().StringVar(&reportExportOutput, "output", "", "Output path")
	reportExportCmd.MarkFlagRequired("start")
	reportExportCmd.MarkFlagRequired("end")
	reportCmd.AddCommand(reportTemplateCmd, reportExportCmd)
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

var reportTemplateRemoveCmd = &cobra.Command{
	Use:   "remove NAME",
	Short: "Remove a report template",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runControlRequest(cmd, http.MethodDelete, "/report/template/"+url.PathEscape(args[0]), "removing report template", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "report template %s removed\n", args[0])
		return err
	},
}

var reportTemplateRestoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore the default report template",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runControlRequest(cmd, http.MethodPost, "/report/template/restore", "restoring report template", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "report template default_template.md restored\n")
		return err
	},
}

var reportExportCmd = &cobra.Command{
	Use:   "export NAME",
	Short: "Export a report",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := time.Parse("2006-01-02", reportExportStart); err != nil {
			return fmt.Errorf("invalid start date %q", reportExportStart)
		}
		if _, err := time.Parse("2006-01-02", reportExportEnd); err != nil {
			return fmt.Errorf("invalid end date %q", reportExportEnd)
		}
		if reportExportTruncate < 0 {
			return errors.New("truncate length must not be negative")
		}
		output, err := reportExportPath(reportExportTitle, args[0], reportExportOutput)
		if err != nil {
			return err
		}
		requestBody, err := json.Marshal(struct {
			Template         string `json:"template"`
			Title            string `json:"title"`
			Client           string `json:"client"`
			Type             string `json:"type"`
			Scope            string `json:"scope"`
			Assessor         string `json:"assessor"`
			Start            string `json:"start"`
			End              string `json:"end"`
			IsDraft          bool   `json:"is_draft"`
			TruncateLength   int    `json:"truncate_length"`
			IncludeTestCases bool   `json:"include_test_cases"`
		}{
			Template:         args[0],
			Title:            reportExportTitle,
			Client:           reportExportClient,
			Type:             reportExportType,
			Scope:            reportExportScope,
			Assessor:         reportExportAssessor,
			Start:            reportExportStart,
			End:              reportExportEnd,
			IsDraft:          reportExportDraft,
			TruncateLength:   reportExportTruncate,
			IncludeTestCases: reportExportIncludeTestCases,
		})
		if err != nil {
			return fmt.Errorf("encoding report export request: %w", err)
		}
		rendered, err := runControlRequest(cmd, http.MethodPost, "/report", "exporting report", requestBody)
		if err != nil {
			return err
		}
		if err := replaceFile(output, rendered); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Path string `json:"path"`
			}{Path: output})
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), output)
		return err
	},
}

func reportExportPath(title, templateName, output string) (string, error) {
	if output == "" {
		name := title
		if name == "" {
			name = filepath.Base(templateName)
		} else if ext := filepath.Ext(templateName); ext != "" && !strings.HasSuffix(name, ext) {
			name += ext
		}
		if !report.ValidTemplateName(name) {
			return "", fmt.Errorf("invalid report output name %q", name)
		}
		output = name
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		return "", fmt.Errorf("resolving report output path: %w", err)
	}
	return abs, nil
}

func replaceFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".marasi-report-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return os.Rename(temp, path)
}
