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
	"runtime"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var chromePathValue string
var chromePathOS = runtime.GOOS
var chromeStartProfile string

type chromePathRequest struct {
	OS   string `json:"os"`
	Path string `json:"path"`
}

type chromeProfileRequest struct {
	Name string `json:"name"`
}

type chromeStartRequest struct {
	Profile string `json:"profile"`
}

func init() {
	chromePathAddCmd.Flags().StringVar(&chromePathValue, "path", "", "Chrome executable path")
	chromePathAddCmd.Flags().StringVar(&chromePathOS, "os", runtime.GOOS, "Operating system")
	chromePathAddCmd.MarkFlagRequired("path")
	chromePathRemoveCmd.Flags().StringVar(&chromePathValue, "path", "", "Chrome executable path")
	chromePathRemoveCmd.Flags().StringVar(&chromePathOS, "os", runtime.GOOS, "Operating system")
	chromePathRemoveCmd.MarkFlagRequired("path")
	chromePathCmd.AddCommand(chromePathAddCmd, chromePathRemoveCmd, chromePathListCmd)
	chromeProfileCmd.AddCommand(chromeProfileAddCmd, chromeProfileRemoveCmd, chromeProfileListCmd)
	chromeStartCmd.Flags().StringVar(&chromeStartProfile, "profile", "", "Chrome profile name")
	chromeCmd.AddCommand(chromePathCmd, chromeProfileCmd, chromeStartCmd)
	rootCmd.AddCommand(chromeCmd)
}

var chromeCmd = &cobra.Command{
	Use:   "chrome",
	Short: "Manage Chrome for a service instance",
}

var chromePathCmd = &cobra.Command{
	Use:   "path",
	Short: "Manage Chrome executable paths",
}

var chromePathAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a Chrome executable path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runChromePathCommand(cmd, http.MethodPost, "adding chrome path", "chrome path added")
	},
}

var chromePathRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove a Chrome executable path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runChromePathCommand(cmd, http.MethodDelete, "removing chrome path", "chrome path removed")
	},
}

var chromePathListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Chrome executable paths",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runChromeRequest(cmd, http.MethodGet, "/chrome/path", "listing chrome paths", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeChromePaths(body, cmd.OutOrStdout())
	},
}

var chromeProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage Chrome profiles",
}

var chromeProfileAddCmd = &cobra.Command{
	Use:   "add NAME",
	Short: "Add a Chrome profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := json.Marshal(chromeProfileRequest{Name: args[0]})
		if err != nil {
			return fmt.Errorf("encoding chrome profile request: %w", err)
		}
		return runChromeMutation(cmd, http.MethodPost, "/chrome/profile", "adding chrome profile", body, "chrome profile "+args[0]+" added")
	},
}

var chromeProfileRemoveCmd = &cobra.Command{
	Use:   "remove NAME",
	Short: "Remove a Chrome profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runChromeMutation(cmd, http.MethodDelete, "/chrome/profile/"+url.PathEscape(args[0]), "removing chrome profile", nil, "chrome profile "+args[0]+" removed")
	},
}

var chromeProfileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Chrome profiles",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := runChromeRequest(cmd, http.MethodGet, "/chrome/profile", "listing chrome profiles", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeChromeProfiles(body, cmd.OutOrStdout())
	},
}

var chromeStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start Chrome",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body := []byte("{}")
		if cmd.Flags().Changed("profile") {
			var err error
			body, err = json.Marshal(chromeStartRequest{Profile: chromeStartProfile})
			if err != nil {
				return fmt.Errorf("encoding chrome start request: %w", err)
			}
		}
		response, err := runChromeRequest(cmd, http.MethodPost, "/chrome/start", "starting chrome", body)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(response)
			return err
		}
		var result struct {
			Profile string `json:"profile"`
		}
		if err := json.Unmarshal(response, &result); err != nil {
			return fmt.Errorf("decoding chrome start response: %w", err)
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "chrome started with profile %s\n", result.Profile)
		return err
	},
}

func runChromePathCommand(cmd *cobra.Command, method, operation, confirmation string) error {
	body, err := json.Marshal(chromePathRequest{OS: chromePathOS, Path: chromePathValue})
	if err != nil {
		return fmt.Errorf("encoding chrome path request: %w", err)
	}
	return runChromeMutation(cmd, method, "/chrome/path", operation, body, confirmation)
}

func runChromeMutation(cmd *cobra.Command, method, path, operation string, body []byte, confirmation string) error {
	response, err := runChromeRequest(cmd, method, path, operation, body)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
	} else {
		_, err = fmt.Fprintln(cmd.ErrOrStderr(), confirmation)
	}
	return err
}

func runChromeRequest(cmd *cobra.Command, method, path, operation string, body []byte) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating chrome request: %w", err)
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
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(wrapError("reading chrome response", readErr), wrapError("closing chrome response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return nil, controlAPIError(operation, response.Status, responseBody)
	}
	return responseBody, nil
}

func writeChromePaths(body []byte, stdout io.Writer) error {
	var response struct {
		Items []chromePathRequest `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding chrome paths: %w", err)
	}
	for index, path := range response.Items {
		if index > 0 {
			if _, err := fmt.Fprintln(stdout); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "os: %s\npath: %s\n", path.OS, path.Path); err != nil {
			return err
		}
	}
	return nil
}

func writeChromeProfiles(body []byte, stdout io.Writer) error {
	var response struct {
		Items []chromeProfileRequest `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding chrome profiles: %w", err)
	}
	for _, profile := range response.Items {
		if _, err := fmt.Fprintln(stdout, profile.Name); err != nil {
			return err
		}
	}
	return nil
}
