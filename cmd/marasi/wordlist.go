package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var wordlistPreviewLimit int

func init() {
	wordlistPreviewCmd.Flags().IntVar(&wordlistPreviewLimit, "limit", 20, "Number of entries to preview")
	wordlistCmd.AddCommand(wordlistListCmd, wordlistPreviewCmd, wordlistAddCmd, wordlistRemoveCmd)
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
		body, err := runWordlistRequest(cmd, http.MethodGet, path, "previewing wordlist", nil)
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
		body, err := runWordlistRequest(cmd, http.MethodGet, "/wordlist", "listing wordlists", nil)
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

var wordlistAddCmd = &cobra.Command{
	Use:   "add PATH",
	Short: "Add a wordlist",
	Long:  "Add a wordlist. The source file is moved into the wordlists directory.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := filepath.Abs(args[0])
		if err != nil {
			return fmt.Errorf("resolving wordlist path: %w", err)
		}
		requestBody, err := json.Marshal(struct {
			Path string `json:"path"`
		}{Path: path})
		if err != nil {
			return fmt.Errorf("encoding wordlist add request: %w", err)
		}
		response, err := runWordlistRequest(cmd, http.MethodPost, "/wordlist", "adding wordlist", requestBody)
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
		body, err := runWordlistRequest(cmd, http.MethodDelete, "/wordlist/"+url.PathEscape(args[0]), "removing wordlist", nil)
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

func runWordlistRequest(cmd *cobra.Command, method, path, operation string, body []byte) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating wordlist request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
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
