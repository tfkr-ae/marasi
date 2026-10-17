package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var notesSetFile string
var notesListLimit string
var notesListCursor string

const notesDisplayLimit = 40

func init() {
	notesSetCmd.Flags().StringVar(&notesSetFile, "file", "", "Read the note from a file")
	notesListCmd.Flags().StringVar(&notesListLimit, "limit", "200", "Maximum number of items to return, from 1 to 500")
	notesListCmd.Flags().StringVar(&notesListCursor, "cursor", "", "UUID of the last item, used to fetch the next older page")
	notesCmd.AddCommand(notesListCmd, notesGetCmd, notesSetCmd, notesClearCmd)
	rootCmd.AddCommand(notesCmd)
}

var notesCmd = &cobra.Command{
	Use:   "notes",
	Short: "List, get, set, and clear notes on traffic",
}

var notesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List request/response pairs that have a note",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, err := parsePageLimit(notesListLimit)
		if err != nil {
			return err
		}
		query := url.Values{}
		query.Set("limit", limit)
		if err := setCursorQuery(query, notesListCursor); err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, "/notes?"+query.Encode(), "listing notes", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeNotesListHuman(body, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var notesSetCmd = &cobra.Command{
	Use:   "set UUID [TEXT]",
	Short: "Set the note on a request/response pair",
	Long:  "Set the note on a request/response pair. Pass exactly one of TEXT, --file, or piped stdin.",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/notes/", args[0], "")
		if err != nil {
			return err
		}
		note, err := textInput{what: "note", file: notesSetFile}.read(cmd, args[1:])
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			Note string `json:"note"`
		}{Note: note})
		if err != nil {
			return fmt.Errorf("encoding note request: %w", err)
		}
		body, err := runControlRequest(cmd, http.MethodPut, path, "setting note", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "note %s set\n", args[0])
		return err
	},
}

var notesGetCmd = &cobra.Command{
	Use:   "get UUID",
	Short: "Get the full note on a request/response pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/notes/", args[0], "")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, path, "getting note", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		var note struct {
			Note string `json:"note"`
		}
		if err := json.Unmarshal(body, &note); err != nil {
			return fmt.Errorf("decoding note: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), note.Note)
		return err
	},
}

var notesClearCmd = &cobra.Command{
	Use:   "clear UUID",
	Short: "Clear the note on a request/response pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/notes/", args[0], "")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodDelete, path, "clearing note", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "note %s cleared\n", args[0])
		return err
	},
}

func writeNotesListHuman(body []byte, stdout, stderr io.Writer) error {
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Host string `json:"host"`
			Path string `json:"path"`
			Note string `json:"note"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decoding notes list: %w", err)
	}

	table := newListTable(stdout, "ID", "URL", "NOTE")
	for _, item := range page.Items {
		table.row(item.ID, truncateDisplay(item.Host+item.Path, notesDisplayLimit), truncateDisplay(item.Note, notesDisplayLimit))
	}
	if err := table.flush(); err != nil {
		return err
	}
	if page.NextCursor != nil {
		fmt.Fprintf(stderr, "next_cursor=%s\n", *page.NextCursor)
	}
	return nil
}

func truncateDisplay(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', '\f':
			return ' '
		default:
			return r
		}
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit-3]) + "..."
	}
	return value
}
