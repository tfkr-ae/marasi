package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

func init() {
	extensionCmd.AddCommand(extensionListCmd, extensionGetCmd)
	rootCmd.AddCommand(extensionCmd)
}

var extensionCmd = &cobra.Command{
	Use:   "extension",
	Short: "Manage extensions for a service instance",
}

var extensionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List extensions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runExtensionRequest(cmd, http.MethodGet, "/extension", "listing extensions")
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeExtensionList(body, cmd.OutOrStdout())
	},
}

var extensionGetCmd = &cobra.Command{
	Use:   "get UUID",
	Short: "Get one extension",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runExtensionRequest(cmd, http.MethodGet, "/extension/"+args[0], "getting extension")
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeExtensionGet(body, cmd.OutOrStdout())
	},
}

func runExtensionRequest(cmd *cobra.Command, method, path, operation string) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating extension request: %w", err)
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
		return nil, errors.Join(wrapError("reading extension response", readErr), wrapError("closing extension response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return nil, controlAPIError(operation, response.Status, body)
	}
	return body, nil
}

func writeExtensionList(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding extension list: %w", err)
	}
	for _, item := range response.Items {
		state := "disabled"
		if item.Enabled {
			state = "enabled"
		}
		if _, err := fmt.Fprintf(stdout, "%s %s %s\n", item.ID, item.Name, state); err != nil {
			return err
		}
	}
	return nil
}

func writeExtensionGet(body []byte, stdout io.Writer) error {
	var response struct {
		LuaContent string `json:"lua_content"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding extension: %w", err)
	}
	_, err := io.WriteString(stdout, response.LuaContent)
	return err
}
