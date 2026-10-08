package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var extensionUpdateFile string
var extensionSettingsFile string
var extensionCallArgs string

func init() {
	extensionUpdateCmd.Flags().StringVar(&extensionUpdateFile, "file", "", "Read Lua from a file. If omitted, read Lua from stdin")
	extensionSettingsSetCmd.Flags().StringVar(&extensionSettingsFile, "file", "", "Read settings JSON from a file. If omitted, read JSON from stdin")
	extensionCallCmd.Flags().StringVar(&extensionCallArgs, "args", "", "JSON array of arguments passed to FUNCTION")
	extensionSettingsCmd.AddCommand(extensionSettingsGetCmd, extensionSettingsSetCmd)
	extensionCmd.AddCommand(extensionListCmd, extensionGetCmd, extensionUpdateCmd, extensionLogsCmd, extensionSettingsCmd, extensionCallCmd, extensionEnableCmd, extensionDisableCmd)
	rootCmd.AddCommand(extensionCmd)
}

var extensionCmd = &cobra.Command{
	Use:   "extension",
	Short: "List, update, and call extensions",
}

var extensionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List extensions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/extension", "listing extensions", nil)
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
		path, err := serviceIDPath("/extension/", args[0], "")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, path, "getting extension", nil)
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
	Short: "Replace an extension's Lua source",
	Long:  "Replace an extension's Lua source. Pass --file or piped stdin.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		lua, err := readExtensionInput(cmd, extensionUpdateFile, "reading lua file", "reading lua from stdin", "extension update requires --file or piped stdin")
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			LuaContent string `json:"lua_content"`
		}{LuaContent: lua})
		if err != nil {
			return fmt.Errorf("encoding extension update: %w", err)
		}
		path, err := serviceIDPath("/extension/", args[0], "")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodPost, path, "updating extension", payload)
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
		path, err := serviceIDPath("/extension/", args[0], "/logs")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, path, "listing extension logs", nil)
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

var extensionSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Get or set extension settings",
}

var extensionSettingsGetCmd = &cobra.Command{
	Use:   "get UUID",
	Short: "Get extension settings",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/extension/", args[0], "/settings")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, path, "getting extension settings", nil)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(body)
		return err
	},
}

var extensionSettingsSetCmd = &cobra.Command{
	Use:   "set UUID",
	Short: "Replace extension settings",
	Long:  "Replace extension settings from a JSON object. Pass --file or piped stdin.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := readExtensionInput(cmd, extensionSettingsFile, "reading settings file", "reading settings from stdin", "extension settings set requires --file or piped stdin")
		if err != nil {
			return err
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return fmt.Errorf("decoding extension settings: %w", err)
		}
		payload, err := json.Marshal(struct {
			Settings map[string]any `json:"settings"`
		}{Settings: settings})
		if err != nil {
			return fmt.Errorf("encoding extension settings: %w", err)
		}
		path, err := serviceIDPath("/extension/", args[0], "/settings")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodPost, path, "setting extension settings", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "extension %s settings updated\n", args[0])
		return err
	},
}

var extensionCallCmd = &cobra.Command{
	Use:   "call UUID FUNCTION",
	Short: "Call an extension function",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		payload, err := encodeExtensionCall(cmd, args[1])
		if err != nil {
			return err
		}
		path, err := serviceIDPath("/extension/", args[0], "/call")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodPost, path, "calling extension", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "extension %s called %s\n", args[0], args[1])
		return err
	},
}

var extensionEnableCmd = &cobra.Command{
	Use:   "enable UUID",
	Short: "Enable an extension",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtensionEnableDisable(cmd, args[0], true, "enabling extension", "enabled")
	},
}

var extensionDisableCmd = &cobra.Command{
	Use:   "disable UUID",
	Short: "Disable an extension",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtensionEnableDisable(cmd, args[0], false, "disabling extension", "disabled")
	},
}

func runExtensionEnableDisable(cmd *cobra.Command, id string, enabled bool, operation, state string) error {
	payload, err := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{Enabled: enabled})
	if err != nil {
		return fmt.Errorf("encoding extension %s: %w", state, err)
	}
	path, err := serviceIDPath("/extension/", id, "/enable")
	if err != nil {
		return err
	}
	body, err := runControlRequest(cmd, http.MethodPost, path, operation, payload)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "extension %s %s\n", id, state)
	return err
}

func encodeExtensionCall(cmd *cobra.Command, function string) ([]byte, error) {
	request := struct {
		Function string `json:"function"`
		Args     []any  `json:"args,omitempty"`
	}{Function: function}
	if cmd.Flags().Changed("args") {
		args, err := parseExtensionCallArgs(extensionCallArgs)
		if err != nil {
			return nil, err
		}
		request.Args = args
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encoding extension call: %w", err)
	}
	return payload, nil
}

func parseExtensionCallArgs(raw string) ([]any, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("decoding extension args: %w", err)
	}
	args, ok := value.([]any)
	if !ok {
		return nil, errors.New("extension args must be a JSON array")
	}
	return args, nil
}

func readExtensionInput(cmd *cobra.Command, path, fileErr, stdinErr, missing string) (string, error) {
	if cmd.Flags().Changed("file") {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", fileErr, err)
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
			return "", errors.New(missing)
		}
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("%s: %w", stdinErr, err)
	}
	return string(raw), nil
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
