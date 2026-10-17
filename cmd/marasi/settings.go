package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

var settingsListPrefix string
var settingsSetFile string

const settingsValueDisplayLimit = 60

func init() {
	settingsListCmd.Flags().StringVar(&settingsListPrefix, "prefix", "", "List only keys that start with this prefix")
	settingsSetCmd.Flags().StringVar(&settingsSetFile, "file", "", "Read the JSON value from a file")
	settingsCmd.AddCommand(settingsListCmd, settingsGetCmd, settingsSetCmd, settingsDeleteCmd)
	rootCmd.AddCommand(settingsCmd)
}

var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Manage client settings stored in the open project",
	Long: `Manage client settings stored in the open project. A client setting is a
JSON value a client keeps under its own client name and a key, such as
marasi-app and ledger.exclusion. Marasi stores it but never reads its
meaning. The client name marasi is Marasi's own and cannot be written.`,
}

var settingsListCmd = &cobra.Command{
	Use:   "list CLIENT",
	Short: "List a client's settings",
	Long:  "List a client's settings: each key and its JSON value, shortened to fit. Use settings get for a full value.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "/client-settings/" + url.PathEscape(args[0])
		if settingsListPrefix != "" {
			path += "?" + url.Values{"prefix": {settingsListPrefix}}.Encode()
		}
		body, err := runControlRequest(cmd, http.MethodGet, path, "listing client settings", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var list struct {
			Items []clientSetting `json:"items"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return fmt.Errorf("decoding client settings: %w", err)
		}
		table := newListTable(cmd.OutOrStdout(), "KEY", "VALUE")
		for _, setting := range list.Items {
			table.row(setting.Key, truncateDisplay(compactJSON(setting.Value), settingsValueDisplayLimit))
		}
		return table.flush()
	},
}

var settingsGetCmd = &cobra.Command{
	Use:   "get CLIENT KEY",
	Short: "Print the JSON value of one client setting",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, clientSettingPath(args[0], args[1]), "getting client setting", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var setting clientSetting
		if err := json.Unmarshal(body, &setting); err != nil {
			return fmt.Errorf("decoding client setting: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), compactJSON(setting.Value))
		return err
	},
}

var settingsSetCmd = &cobra.Command{
	Use:   "set CLIENT KEY [JSON]",
	Short: "Set one client setting to a JSON value",
	Long: `Set one client setting to a JSON value, replacing any value it had. Pass
exactly one of JSON, --file, or piped stdin. Quote strings as JSON, for
example '"dark"'.`,
	Example: `  marasi settings set my-client theme '"dark"'
  marasi settings set my-client ledger/view --file view.json
  jq -c . view.json | marasi settings set my-client ledger/view`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		value, err := textInput{what: "value", file: settingsSetFile}.read(cmd, args[2:])
		if err != nil {
			return err
		}
		if !json.Valid([]byte(value)) {
			return errors.New("the value is not valid JSON")
		}
		payload, err := json.Marshal(struct {
			Value json.RawMessage `json:"value"`
		}{Value: json.RawMessage(value)})
		if err != nil {
			return fmt.Errorf("encoding client setting: %w", err)
		}
		body, err := runControlRequest(cmd, http.MethodPut, clientSettingPath(args[0], args[1]), "setting client setting", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "setting %s %s set\n", args[0], args[1])
		return err
	},
}

var settingsDeleteCmd = &cobra.Command{
	Use:   "delete CLIENT KEY",
	Short: "Delete one client setting",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runControlRequest(cmd, http.MethodDelete, clientSettingPath(args[0], args[1]), "deleting client setting", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "setting %s %s deleted\n", args[0], args[1])
		return err
	},
}

// clientSetting is one client setting in the /client-settings routes.
type clientSetting struct {
	Client string          `json:"client"`
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
}

// clientSettingPath escapes client and key into a /client-settings path.
func clientSettingPath(client, key string) string {
	return "/client-settings/" + url.PathEscape(client) + "/" + url.PathEscape(key)
}

// compactJSON returns value without insignificant whitespace, or as it is
// when it does not compact.
func compactJSON(value json.RawMessage) string {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, value); err != nil {
		return string(value)
	}
	return buffer.String()
}
