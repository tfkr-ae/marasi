package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func init() {
	jobsCmd.AddCommand(jobsListCmd, jobsRebuildCmd)
	rootCmd.AddCommand(jobsCmd)
}

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "List and rebuild the open project's background jobs",
	Long: `List and rebuild the open project's background jobs. The jobs build the
search indexes and the sitemap from captured traffic. Until a job is
complete, reads that depend on it can miss pairs.`,
}

var jobsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the background jobs and their progress",
	Long:  "List the background jobs: name, complete or running, and pairs done out of the total.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/jobs", "listing jobs", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var list struct {
			Items []backgroundJob `json:"items"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return fmt.Errorf("decoding jobs: %w", err)
		}
		writer := newTableWriter(cmd.OutOrStdout())
		for _, job := range list.Items {
			state := "running"
			if job.Complete {
				state = "complete"
			}
			fmt.Fprintf(writer, "%s\t%s\t%d/%d\n", job.Name, state, job.Done, job.Total)
		}
		return writer.Flush()
	},
}

var jobsRebuildCmd = &cobra.Command{
	Use:   "rebuild NAME",
	Short: "Start a background job again from the beginning",
	Long: `Start a background job again from the beginning, for example to rebuild
the sitemap. The command returns once the job has restarted; follow it with
jobs list. A job that cannot be rebuilt fails with not_rebuildable.`,
	Example: "  marasi jobs rebuild sitemap",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "/jobs/" + url.PathEscape(args[0]) + "/rebuild"
		body, err := runControlRequestStatus(cmd, http.MethodPost, path, "rebuilding job", nil, http.StatusAccepted)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var job backgroundJob
		if err := json.Unmarshal(body, &job); err != nil {
			return fmt.Errorf("decoding job: %w", err)
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "job %s rebuild started\n", job.Name)
		return err
	},
}

// backgroundJob is one job in the /jobs routes.
type backgroundJob struct {
	Name     string `json:"name"`
	Complete bool   `json:"complete"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
}
