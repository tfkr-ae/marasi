package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

var upstreamAddCA string
var upstreamUpdateURL string
var upstreamUpdateCA string
var upstreamUpdateNoCA bool
var upstreamUseNone bool

type upstreamAddRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	CA   string `json:"ca,omitempty"`
}

// upstreamUpdateRequest leaves out a field that does not change. CA is a
// pointer to a pointer so that --no-ca sends "ca":null.
type upstreamUpdateRequest struct {
	URL *string  `json:"url,omitempty"`
	CA  **string `json:"ca,omitempty"`
}

func init() {
	upstreamAddCmd.Flags().StringVar(&upstreamAddCA, "ca", "", "PEM file with an extra trusted CA certificate, copied into the entry")
	upstreamUpdateCmd.Flags().StringVar(&upstreamUpdateURL, "url", "", "New URL: http, https or socks5://[user:pass@]host:port")
	upstreamUpdateCmd.Flags().StringVar(&upstreamUpdateCA, "ca", "", "PEM file with an extra trusted CA certificate, copied into the entry")
	upstreamUpdateCmd.Flags().BoolVar(&upstreamUpdateNoCA, "no-ca", false, "Remove the entry's CA")
	upstreamUpdateCmd.MarkFlagsMutuallyExclusive("ca", "no-ca")
	upstreamUpdateCmd.MarkFlagsOneRequired("url", "ca", "no-ca")
	upstreamUseCmd.Flags().BoolVar(&upstreamUseNone, "none", false, "Clear the selection and connect directly")
	upstreamCmd.AddCommand(upstreamListCmd, upstreamAddCmd, upstreamUpdateCmd, upstreamRemoveCmd, upstreamUseCmd)
	rootCmd.AddCommand(upstreamCmd)
}

var upstreamCmd = &cobra.Command{
	Use:   "upstream",
	Short: "Manage this machine's upstream proxies",
	Long: `Manage this machine's list of named upstream proxies in marasi_config.yaml.
Output never shows credentials or CA certificates.`,
}

var upstreamListCmd = &cobra.Command{
	Use:   "list",
	Short: "List upstream proxies without credentials",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/upstream-proxy", "listing upstream proxies", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		return writeUpstreamProxies(body, cmd.OutOrStdout())
	},
}

var upstreamAddCmd = &cobra.Command{
	Use:   "add NAME URL",
	Short: "Add an upstream proxy",
	Long: `Add an upstream proxy. URL is http, https or socks5://[user:pass@]host:port.
--ca reads a PEM file once and copies the certificate into the entry.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		request := upstreamAddRequest{Name: args[0], URL: args[1]}
		if cmd.Flags().Changed("ca") {
			ca, err := readUpstreamCA(upstreamAddCA)
			if err != nil {
				return err
			}
			request.CA = ca
		}
		body, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("encoding upstream proxy request: %w", err)
		}
		return runUpstreamMutation(cmd, http.MethodPost, "/upstream-proxy", "adding upstream proxy", body, "upstream proxy "+args[0]+" added")
	},
}

var upstreamUpdateCmd = &cobra.Command{
	Use:   "update NAME",
	Short: "Change an upstream proxy's URL or CA",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var request upstreamUpdateRequest
		if cmd.Flags().Changed("url") {
			request.URL = &upstreamUpdateURL
		}
		if cmd.Flags().Changed("ca") {
			ca, err := readUpstreamCA(upstreamUpdateCA)
			if err != nil {
				return err
			}
			value := &ca
			request.CA = &value
		}
		if upstreamUpdateNoCA {
			var none *string
			request.CA = &none
		}
		body, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("encoding upstream proxy request: %w", err)
		}
		return runUpstreamMutation(cmd, http.MethodPost, "/upstream-proxy/"+url.PathEscape(args[0]), "updating upstream proxy", body, "upstream proxy "+args[0]+" updated")
	},
}

var upstreamRemoveCmd = &cobra.Command{
	Use:   "remove NAME",
	Short: "Remove an upstream proxy",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUpstreamMutation(cmd, http.MethodDelete, "/upstream-proxy/"+url.PathEscape(args[0]), "removing upstream proxy", nil, "upstream proxy "+args[0]+" removed")
	},
}

var upstreamUseCmd = &cobra.Command{
	Use:   "use NAME | --none",
	Short: "Select the open project's upstream proxy",
	Long: `Select the upstream proxy all of the open project's traffic to the origin
leaves through, or --none to connect directly. The project stores the
selection. Nothing is dialled: a broken upstream proxy shows up as failed
requests, each with a proxy log. A request never falls back to a direct
connection, and a project whose selection this machine does not list fails
every request until you select a listed one or --none.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if upstreamUseNone {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		name, confirmation := "", "upstream proxy selection cleared: connecting directly"
		if !upstreamUseNone {
			name, confirmation = args[0], "upstream proxy "+args[0]+" selected"
		}
		body, err := json.Marshal(struct {
			Name string `json:"name"`
		}{Name: name})
		if err != nil {
			return fmt.Errorf("encoding upstream proxy selection: %w", err)
		}
		return runUpstreamMutation(cmd, http.MethodPut, "/upstream-proxy-selection", "selecting upstream proxy", body, confirmation)
	},
}

// readUpstreamCA reads a CA PEM file once; the instance checks its contents.
func readUpstreamCA(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading CA file: %w", err)
	}
	return string(data), nil
}

func runUpstreamMutation(cmd *cobra.Command, method, path, operation string, body []byte, confirmation string) error {
	response, err := runControlRequest(cmd, method, path, operation, body)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSONBody(cmd, response)
	}
	_, err = fmt.Fprintln(cmd.ErrOrStderr(), confirmation)
	return err
}

func writeUpstreamProxies(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			Name           string `json:"name"`
			Scheme         string `json:"scheme"`
			Host           string `json:"host"`
			Port           int    `json:"port"`
			HasCredentials bool   `json:"has_credentials"`
			HasCA          bool   `json:"has_ca"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding upstream proxies: %w", err)
	}
	table := newListTable(stdout, "NAME", "SCHEME", "HOST", "PORT", "CREDENTIALS", "CA")
	for _, item := range response.Items {
		table.row(item.Name, item.Scheme, item.Host, item.Port, yesNo(item.HasCredentials), yesNo(item.HasCA))
	}
	return table.flush()
}
