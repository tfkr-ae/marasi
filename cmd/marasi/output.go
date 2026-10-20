package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// writeJSONBody prints a route's JSON response body as it is, for --json
// (docs/adr/2026-10-06-json-flag-for-agents.md).
func writeJSONBody(cmd *cobra.Command, body []byte) error {
	_, err := cmd.OutOrStdout().Write(body)
	return err
}

// newTableWriter returns the tab writer human output uses for aligned
// columns: two spaces between columns. Flush it after the last row.
func newTableWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// listTable writes a list command's human rows as aligned columns, with the
// column names both above and below the rows so a long list keeps its labels
// in view. An empty list prints nothing (docs/adr/2026-10-17-list-tables-name-their-columns-above-and-below.md).
type listTable struct {
	writer  *tabwriter.Writer
	columns string
	started bool
}

// newListTable starts a table with the given column names. Call flush after
// the last row.
func newListTable(w io.Writer, columns ...string) *listTable {
	return &listTable{writer: newTableWriter(w), columns: strings.Join(columns, "\t") + "\n"}
}

// row adds one row, printing each cell as fmt.Print would.
func (t *listTable) row(cells ...any) {
	if !t.started {
		io.WriteString(t.writer, t.columns)
		t.started = true
	}
	for index, cell := range cells {
		if index > 0 {
			io.WriteString(t.writer, "\t")
		}
		fmt.Fprint(t.writer, cell)
	}
	io.WriteString(t.writer, "\n")
}

// flush writes the footer and the aligned table.
func (t *listTable) flush() error {
	if t.started {
		io.WriteString(t.writer, t.columns)
	}
	return t.writer.Flush()
}
