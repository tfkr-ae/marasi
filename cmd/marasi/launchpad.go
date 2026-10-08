package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var launchpadCreateName string
var launchpadCreateDescription string
var launchpadUpdateName string
var launchpadUpdateDescription string
var launchpadLinkRequest string
var launchpadLaunchScheme string
var launchpadLaunchRawFile string

type launchpadRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	RequestID   *string `json:"request_id,omitempty"`
	Raw         *string `json:"raw,omitempty"`
	Scheme      *string `json:"scheme,omitempty"`
}

func init() {
	launchpadCreateCmd.Flags().StringVar(&launchpadCreateName, "name", "", "Launchpad name")
	launchpadCreateCmd.Flags().StringVar(&launchpadCreateDescription, "description", "", "Launchpad description")
	launchpadCreateCmd.MarkFlagRequired("name")
	launchpadUpdateCmd.Flags().StringVar(&launchpadUpdateName, "name", "", "Launchpad name")
	launchpadUpdateCmd.Flags().StringVar(&launchpadUpdateDescription, "description", "", "Launchpad description")
	launchpadLinkCmd.Flags().StringVar(&launchpadLinkRequest, "request", "", "UUID of the request/response pair")
	launchpadLinkCmd.MarkFlagRequired("request")
	launchpadLaunchCmd.Flags().StringVar(&launchpadLaunchScheme, "scheme", "", "Request scheme: http or https. Required")
	launchpadLaunchCmd.Flags().StringVar(&launchpadLaunchRawFile, "raw-file", "", "Read the raw HTTP request from a file. If omitted, read it from stdin")
	launchpadLaunchCmd.MarkFlagRequired("scheme")
	launchpadCmd.AddCommand(launchpadCreateCmd, launchpadListCmd, launchpadGetCmd, launchpadUpdateCmd, launchpadLinkCmd, launchpadLaunchCmd)
	rootCmd.AddCommand(launchpadCmd)
}

var launchpadCmd = &cobra.Command{
	Use:   "launchpad",
	Short: "Create launchpads and send raw HTTP requests",
}

var launchpadCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an empty launchpad",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if launchpadCreateName == "" {
			return errors.New("launchpad create requires a non-empty --name")
		}
		request := launchpadRequest{Name: &launchpadCreateName}
		if cmd.Flags().Changed("description") {
			request.Description = &launchpadCreateDescription
		}
		return runLaunchpadCommand(cmd, http.MethodPost, "/launchpad", "creating launchpad", "create", request)
	},
}

var launchpadListCmd = &cobra.Command{
	Use:   "list",
	Short: "List launchpads oldest-first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runLaunchpadCommand(cmd, http.MethodGet, "/launchpad", "listing launchpads", "list", launchpadRequest{})
	},
}

var launchpadGetCmd = &cobra.Command{
	Use:   "get UUID",
	Short: "Get one launchpad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/launchpad/", args[0], "")
		if err != nil {
			return err
		}
		return runLaunchpadCommand(cmd, http.MethodGet, path, "getting launchpad", "get", launchpadRequest{})
	},
}

var launchpadUpdateCmd = &cobra.Command{
	Use:   "update UUID",
	Short: "Update a launchpad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var request launchpadRequest
		if cmd.Flags().Changed("name") {
			if launchpadUpdateName == "" {
				return errors.New("launchpad update requires a non-empty --name")
			}
			request.Name = &launchpadUpdateName
		}
		if cmd.Flags().Changed("description") {
			request.Description = &launchpadUpdateDescription
		}
		if request.Name == nil && request.Description == nil {
			return errors.New("launchpad update requires --name or --description")
		}
		path, err := serviceIDPath("/launchpad/", args[0], "")
		if err != nil {
			return err
		}
		return runLaunchpadCommand(cmd, http.MethodPost, path, "updating launchpad", "update", request)
	},
}

var launchpadLinkCmd = &cobra.Command{
	Use:   "link UUID",
	Short: "Link a traffic pair to a launchpad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/launchpad/", args[0], "/link")
		if err != nil {
			return err
		}
		return runLaunchpadCommand(cmd, http.MethodPost, path, "linking launchpad request", "link", launchpadRequest{RequestID: &launchpadLinkRequest})
	},
}

var launchpadLaunchCmd = &cobra.Command{
	Use:   "launch UUID",
	Short: "Send a raw HTTP working copy",
	Long:  "Send a raw HTTP working copy for a launchpad. Pass --raw-file or piped stdin. Set --scheme to http or https.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if launchpadLaunchScheme != "http" && launchpadLaunchScheme != "https" {
			return errors.New("launchpad launch requires --scheme http or https")
		}
		var raw []byte
		var err error
		if cmd.Flags().Changed("raw-file") {
			raw, err = os.ReadFile(launchpadLaunchRawFile)
			if err != nil {
				return fmt.Errorf("reading raw request file: %w", err)
			}
		} else {
			stdin := cmd.InOrStdin()
			if file, ok := stdin.(*os.File); ok {
				info, statErr := file.Stat()
				if statErr != nil {
					return fmt.Errorf("checking stdin: %w", statErr)
				}
				if info.Mode()&os.ModeCharDevice != 0 {
					return errors.New("launchpad launch requires --raw-file or piped stdin")
				}
			}
			raw, err = io.ReadAll(stdin)
			if err != nil {
				return fmt.Errorf("reading raw request from stdin: %w", err)
			}
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		path, err := serviceIDPath("/launchpad/", args[0], "/launch")
		if err != nil {
			return err
		}
		return runLaunchpadCommand(cmd, http.MethodPost, path, "launching launchpad request", "launch", launchpadRequest{Raw: &encoded, Scheme: &launchpadLaunchScheme})
	},
}

func runLaunchpadCommand(cmd *cobra.Command, method, path, operation, action string, body launchpadRequest) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if method == http.MethodPost {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding launchpad request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	called, err := callInstance(ctx, method, path, "application/json", requestBody)
	if err != nil {
		return err
	}
	if err := rejectInstanceStatus(operation, called, jsonOutput); err != nil {
		return err
	}
	response := called.Body
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
		return err
	}
	switch action {
	case "list":
		return writeLaunchpadListHuman(response, cmd.OutOrStdout())
	case "get":
		return writeLaunchpadGetHuman(response, cmd.OutOrStdout())
	case "create", "update":
		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(response, &result); err != nil || result.ID == "" {
			return errors.New("decoding launchpad response")
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "launchpad %s %sd successfully\n", result.ID, action)
		return err
	case "link":
		var result struct {
			LaunchpadID string `json:"launchpad_id"`
			RequestID   string `json:"request_id"`
		}
		if err := json.Unmarshal(response, &result); err != nil || result.LaunchpadID == "" || result.RequestID == "" {
			return errors.New("decoding launchpad response")
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "request %s linked to launchpad %s successfully\n", result.RequestID, result.LaunchpadID)
		return err
	case "launch":
		var result struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(response, &result); err != nil || result.Status != "launched" {
			return errors.New("decoding launchpad response")
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "launchpad %s launched successfully\n", strings.TrimSuffix(strings.TrimPrefix(path, "/launchpad/"), "/launch"))
		return err
	default:
		return fmt.Errorf("unsupported launchpad action %q", action)
	}
}

func writeLaunchpadListHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding launchpad list: %w", err)
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range response.Items {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", item.ID, truncateDisplay(item.Name, trafficPathDisplayLimit), truncateDisplay(item.Description, trafficPathDisplayLimit))
	}
	return writer.Flush()
}

func writeLaunchpadGetHuman(body []byte, stdout io.Writer) error {
	var response struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding launchpad: %w", err)
	}
	_, err := fmt.Fprintf(stdout, "id: %s\nname: %s\ndescription: %s\n", response.ID, response.Name, response.Description)
	if err != nil {
		return err
	}
	return writeTrafficListHuman(body, stdout, io.Discard)
}
