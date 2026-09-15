package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
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
	launchpadLinkCmd.Flags().StringVar(&launchpadLinkRequest, "request", "", "Request UUID")
	launchpadLinkCmd.MarkFlagRequired("request")
	launchpadLaunchCmd.Flags().StringVar(&launchpadLaunchScheme, "scheme", "", "Request scheme (http or https)")
	launchpadLaunchCmd.Flags().StringVar(&launchpadLaunchRawFile, "raw-file", "", "Read the raw HTTP request from a file")
	launchpadLaunchCmd.MarkFlagRequired("scheme")
	launchpadCmd.AddCommand(launchpadCreateCmd, launchpadListCmd, launchpadGetCmd, launchpadUpdateCmd, launchpadLinkCmd, launchpadLaunchCmd)
	rootCmd.AddCommand(launchpadCmd)
}

var launchpadCmd = &cobra.Command{
	Use:   "launchpad",
	Short: "Manage launchpads for a service instance",
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
		return runLaunchpadCommand(cmd, "create", "", request)
	},
}

var launchpadListCmd = &cobra.Command{
	Use:   "list",
	Short: "List launchpads newest-first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runLaunchpadCommand(cmd, "list", "", launchpadRequest{})
	},
}

var launchpadGetCmd = &cobra.Command{
	Use:   "get uuid",
	Short: "Get one launchpad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLaunchpadCommand(cmd, "get", args[0], launchpadRequest{})
	},
}

var launchpadUpdateCmd = &cobra.Command{
	Use:   "update uuid",
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
		return runLaunchpadCommand(cmd, "update", args[0], request)
	},
}

var launchpadLinkCmd = &cobra.Command{
	Use:   "link uuid",
	Short: "Link a traffic pair to a launchpad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLaunchpadCommand(cmd, "link", args[0], launchpadRequest{RequestID: &launchpadLinkRequest})
	},
}

var launchpadLaunchCmd = &cobra.Command{
	Use:   "launch uuid",
	Short: "Launch a raw request from a working copy",
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
		return runLaunchpadCommand(cmd, "launch", args[0], launchpadRequest{Raw: &encoded, Scheme: &launchpadLaunchScheme})
	},
}

func runLaunchpadCommand(cmd *cobra.Command, action, id string, body launchpadRequest) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	response, err := controlLaunchpad(ctx, instancePath, instance, jsonOutput, action, id, body)
	if err != nil {
		return err
	}
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
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "launchpad %s launched successfully\n", id)
		return err
	default:
		return fmt.Errorf("unsupported launchpad action %q", action)
	}
}

func controlLaunchpad(ctx context.Context, instancePath, instanceName string, asJSON bool, action, id string, payload launchpadRequest) ([]byte, error) {
	method := http.MethodGet
	path := "/launchpad"
	operation := "listing launchpads"
	var requestBody io.Reader
	switch action {
	case "create":
		method, operation = http.MethodPost, "creating launchpad"
	case "get":
		path, operation = path+"/"+id, "getting launchpad"
	case "update":
		method, path, operation = http.MethodPost, path+"/"+id, "updating launchpad"
	case "link":
		method, path, operation = http.MethodPost, path+"/"+id+"/link", "linking launchpad request"
	case "launch":
		method, path, operation = http.MethodPost, path+"/"+id+"/launch", "launching launchpad request"
	}
	if method == http.MethodPost {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding launchpad request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating launchpad request: %w", err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("instance %s is not running", instanceName)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(wrapError("reading launchpad response", readErr), wrapError("closing launchpad response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return nil, controlAPIError(operation, response.Status, body)
		}
		return nil, fmt.Errorf("%s: %s", operation, response.Status)
	}
	return body, nil
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
		fmt.Fprintf(writer, "%s\t%s\t%s\n", item.ID, item.Name, item.Description)
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
