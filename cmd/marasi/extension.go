package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var extensionUpdateFile string

func init() {
	extensionUpdateCmd.Flags().StringVar(&extensionUpdateFile, "file", "", "Read lua from a file")
	extensionCmd.AddCommand(extensionListCmd, extensionGetCmd, extensionUpdateCmd, extensionLogsCmd)
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
		body, err := runExtensionRequest(cmd, http.MethodGet, "/extension", "listing extensions", nil)
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
		body, err := runExtensionRequest(cmd, http.MethodGet, "/extension/"+args[0], "getting extension", nil)
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

var extensionUpdateCmd = &cobra.Command{
	Use:   "update UUID",
	Short: "Update extension lua",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		lua, err := readExtensionLua(cmd)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			LuaContent string `json:"lua_content"`
		}{LuaContent: lua})
		if err != nil {
			return fmt.Errorf("encoding extension update: %w", err)
		}
		body, err := runExtensionRequest(cmd, http.MethodPost, "/extension/"+args[0], "updating extension", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "extension %s updated\n", args[0])
		return err
	},
}

var extensionLogsCmd = &cobra.Command{
	Use:   "logs UUID",
	Short: "List extension print logs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runExtensionRequest(cmd, http.MethodGet, "/extension/"+args[0]+"/logs", "listing extension logs", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeExtensionLogs(body, cmd.OutOrStdout())
	},
}

func readExtensionLua(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("file") {
		raw, err := os.ReadFile(extensionUpdateFile)
		if err != nil {
			return "", fmt.Errorf("reading lua file: %w", err)
		}
		return string(raw), nil
	}
	stdin := cmd.InOrStdin()
	if file, ok := stdin.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return "", fmt.Errorf("checking stdin: %w", err)
		}
		if info.Mode()&os.ModeCharDevice != 0 {
			return "", errors.New("extension update requires --file or piped stdin")
		}
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading lua from stdin: %w", err)
	}
	return string(raw), nil
}

func runExtensionRequest(cmd *cobra.Command, method, path, operation string, payload []byte) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if payload != nil {
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating extension request: %w", err)
	}
	if payload != nil {
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

func writeExtensionLogs(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			Time time.Time `json:"time"`
			Text string    `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding extension logs: %w", err)
	}
	for _, item := range response.Items {
		if _, err := fmt.Fprintf(stdout, "%s %s\n", item.Time.Format(time.RFC3339), item.Text); err != nil {
			return err
		}
	}
	return nil
}
