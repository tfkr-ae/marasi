package main

import (
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// writeJSONBody prints a route's JSON response body as it is, for --json
// (ADR-0006).
func writeJSONBody(cmd *cobra.Command, body []byte) error {
	_, err := cmd.OutOrStdout().Write(body)
	return err
}

// newTableWriter returns the tab writer human output uses for aligned
// columns: two spaces between columns. Flush it after the last row.
func newTableWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}
