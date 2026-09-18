package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var wordlistPreviewLimit int

func init() {
	wordlistPreviewCmd.Flags().IntVar(&wordlistPreviewLimit, "limit", 20, "Number of entries to preview")
	wordlistCmd.AddCommand(wordlistListCmd, wordlistPreviewCmd)
	rootCmd.AddCommand(wordlistCmd)
}

var wordlistPreviewCmd = &cobra.Command{
	Use:   "preview NAME",
	Short: "Preview a wordlist",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "/wordlist/" + url.PathEscape(args[0])
		if cmd.Flags().Changed("limit") {
			path += "?" + url.Values{"limit": {fmt.Sprint(wordlistPreviewLimit)}}.Encode()
		}
		body, err := runWordlistRequest(cmd, path, "previewing wordlist")
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
	Short: "Manage wordlists for a service instance",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return errors.New("wordlist requires a subcommand")
	},
}

var wordlistListCmd = &cobra.Command{
	Use:   "list",
	Short: "List wordlists",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runWordlistRequest(cmd, "/wordlist", "listing wordlists")
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeWordlistList(body, cmd.OutOrStdout())
	},
}

func runWordlistRequest(cmd *cobra.Command, path, operation string) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating wordlist request: %w", err)
	}
	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("instance %s is not running", instance)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(wrapError("reading wordlist response", readErr), wrapError("closing wordlist response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return nil, controlAPIError(operation, response.Status, body)
	}
	return body, nil
}

func writeWordlistList(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding wordlist list: %w", err)
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
