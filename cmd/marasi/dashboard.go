package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

// interceptedCountingNotice is printed while the intercepted_count job has
// not reached every pair.
const interceptedCountingNotice = "notice: the intercepted count is still building; it may be incomplete"

func init() {
	rootCmd.AddCommand(dashboardCmd)
}

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Show the open project's pair, note, launchpad and intercepted counts",
	Long: `Show the open project's counts: pairs, notes, launchpads and intercepted
pairs. The project keeps them as pairs are written, so the command does not
scan traffic. Until the intercepted_count job is complete, the intercepted
count leaves out the pairs it has not reached, and a notice says so on stderr.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/dashboard", "reading dashboard", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var counts struct {
			Pairs            int `json:"pairs"`
			Notes            int `json:"notes"`
			Launchpads       int `json:"launchpads"`
			Intercepted      int `json:"intercepted"`
			InterceptedCount struct {
				Complete bool `json:"complete"`
			} `json:"intercepted_count"`
		}
		if err := json.Unmarshal(body, &counts); err != nil {
			return fmt.Errorf("decoding dashboard: %w", err)
		}
		table := newTableWriter(cmd.OutOrStdout())
		fmt.Fprintf(table, "pairs\t%d\nnotes\t%d\nlaunchpads\t%d\nintercepted\t%d\n",
			counts.Pairs, counts.Notes, counts.Launchpads, counts.Intercepted)
		if err := table.Flush(); err != nil {
			return err
		}
		if !counts.InterceptedCount.Complete {
			fmt.Fprintln(cmd.ErrOrStderr(), interceptedCountingNotice)
		}
		return nil
	},
}
