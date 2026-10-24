package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/wordlist"
)

var wordlistPreviewLimit int

func init() {
	wordlistPreviewCmd.Flags().IntVar(&wordlistPreviewLimit, "limit", 20, "Number of entries to print, from 1 to 100")
	wordlistCmd.AddCommand(wordlistListCmd, wordlistPreviewCmd, wordlistAddCmd, wordlistRemoveCmd)
	rootCmd.AddCommand(wordlistCmd)
}

var wordlistPreviewCmd = &cobra.Command{
	Use:   "preview NAME",
	Short: "Preview a wordlist",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := wordlistNamePath(args[0])
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("limit") {
			path += "?" + url.Values{"limit": {fmt.Sprint(wordlistPreviewLimit)}}.Encode()
		}
		body, err := runControlRequest(cmd, http.MethodGet, path, "previewing wordlist", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeWordlistPreview(body, cmd.OutOrStdout())
	},
}

var wordlistCmd = &cobra.Command{
	Use:   "wordlist",
	Short: "List, add, preview, and remove wordlists",
	Args:  cobra.NoArgs,
	RunE:  showSubcommandHelp,
}

var wordlistListCmd = &cobra.Command{
	Use:   "list",
	Short: "List wordlists",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/wordlist", "listing wordlists", nil)
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

var wordlistAddCmd = &cobra.Command{
	Use:   "add PATH",
	Short: "Add a wordlist",
	Long:  "Add a wordlist. The source file is moved into the wordlists directory. Over --remote its contents are uploaded and the local file is deleted once the instance accepts them.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := filepath.Abs(args[0])
		if err != nil {
			return fmt.Errorf("resolving wordlist path: %w", err)
		}
		response, err := runAddRequest(cmd, "/wordlist", path, "adding wordlist")
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(response)
		} else {
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "wordlist %s added\n", filepath.Base(path))
		}
		return err
	},
}

var wordlistRemoveCmd = &cobra.Command{
	Use:   "remove NAME",
	Short: "Remove a wordlist",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := wordlistNamePath(args[0])
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodDelete, path, "removing wordlist", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "wordlist %s removed\n", args[0])
		return err
	},
}

func writeSizedList(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding list: %w", err)
	}
	for index, item := range response.Items {
		if index > 0 {
			if _, err := fmt.Fprintln(stdout); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "name: %s\nsize: %d\n", item.Name, item.Size); err != nil {
			return err
		}
	}
	return nil
}

func writeWordlistPreview(body []byte, stdout io.Writer) error {
	var response struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding wordlist preview: %w", err)
	}
	for _, item := range response.Items {
		if _, err := fmt.Fprintln(stdout, item); err != nil {
			return err
		}
	}
	return nil
}

func wordlistNamePath(name string) (string, error) {
	if !wordlist.ValidName(name) {
		return "", fmt.Errorf("invalid wordlist name %q", name)
	}
	return "/wordlist/" + url.PathEscape(name), nil
}
