package main

import (
	"bytes"
	"context"
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

type launchpadRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func init() {
	launchpadCreateCmd.Flags().StringVar(&launchpadCreateName, "name", "", "Launchpad name")
	launchpadCreateCmd.Flags().StringVar(&launchpadCreateDescription, "description", "", "Launchpad description")
	launchpadCreateCmd.MarkFlagRequired("name")
	launchpadUpdateCmd.Flags().StringVar(&launchpadUpdateName, "name", "", "Launchpad name")
	launchpadUpdateCmd.Flags().StringVar(&launchpadUpdateDescription, "description", "", "Launchpad description")
	launchpadCmd.AddCommand(launchpadCreateCmd, launchpadListCmd, launchpadGetCmd, launchpadUpdateCmd)
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
	return err
}
