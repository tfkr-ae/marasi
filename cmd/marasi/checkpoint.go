package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var checkpointKind string
var checkpointForwardFile string
var checkpointInterceptResponse bool

func init() {
	checkpointListCmd.Flags().StringVar(&checkpointKind, "kind", "", "Filter by http or websocket")
	checkpointForwardCmd.Flags().StringVar(&checkpointForwardFile, "file", "", "Read edited bytes from a file")
	checkpointForwardCmd.Flags().BoolVar(&checkpointInterceptResponse, "intercept-response", false, "Hold the matching response")
	checkpointCmd.AddCommand(checkpointListCmd, checkpointGetCmd, checkpointForwardCmd, checkpointDropCmd, checkpointInterceptCmd, checkpointWebsocketInterceptCmd)
	rootCmd.AddCommand(checkpointCmd)
}

var checkpointCmd = &cobra.Command{
	Use:   "checkpoint",
	Short: "Manage Checkpoint for a service instance",
}

var checkpointListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Checkpoint items",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path := "/checkpoint"
		if cmd.Flags().Changed("kind") {
			if checkpointKind != "http" && checkpointKind != "websocket" {
				return errors.New("checkpoint list --kind must be http or websocket")
			}
			path += "?" + url.Values{"kind": {checkpointKind}}.Encode()
		}
		body, err := runCheckpointRequest(cmd, http.MethodGet, path, "listing checkpoint items", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeCheckpointList(body, cmd.OutOrStdout())
	},
}

var checkpointGetCmd = &cobra.Command{
	Use:   "get UUID",
	Short: "Get one Checkpoint item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runCheckpointRequest(cmd, http.MethodGet, "/checkpoint/"+args[0], "getting checkpoint item", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		return writeCheckpointGet(body, cmd.OutOrStdout())
	},
}

var checkpointForwardCmd = &cobra.Command{
	Use:   "forward UUID",
	Short: "Forward a Checkpoint item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		payload, err := encodeCheckpointForward(cmd, args[0])
		if err != nil {
			return err
		}
		body, err := runCheckpointRequest(cmd, http.MethodPost, "/checkpoint/"+args[0]+"/forward", "forwarding checkpoint item", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint %s forwarded\n", args[0])
		return err
	},
}

var checkpointDropCmd = &cobra.Command{
	Use:   "drop UUID",
	Short: "Drop a Checkpoint item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runCheckpointRequest(cmd, http.MethodPost, "/checkpoint/"+args[0]+"/drop", "dropping checkpoint item", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint %s dropped\n", args[0])
		return err
	},
}

var checkpointInterceptCmd = &cobra.Command{
	Use:   "intercept on|off",
	Short: "Set HTTP Checkpoint intercept",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCheckpointFlag(cmd, args[0], "/checkpoint/intercept", "intercept", "setting checkpoint intercept", "intercept")
	},
}

var checkpointWebsocketInterceptCmd = &cobra.Command{
	Use:   "websocket-intercept on|off",
	Short: "Set WebSocket Checkpoint intercept",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCheckpointFlag(cmd, args[0], "/checkpoint/websocket-intercept", "websocket_intercept", "setting checkpoint websocket intercept", "websocket-intercept")
	},
}

func runCheckpointFlag(cmd *cobra.Command, value, path, field, operation, name string) error {
	if value != "on" && value != "off" {
		return fmt.Errorf("checkpoint %s requires on or off", name)
	}
	payload, err := json.Marshal(map[string]bool{field: value == "on"})
	if err != nil {
		return fmt.Errorf("encoding checkpoint %s: %w", name, err)
	}
	body, err := runCheckpointRequest(cmd, http.MethodPost, path, operation, payload)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint %s %s\n", name, value)
	return err
}

func encodeCheckpointForward(cmd *cobra.Command, id string) ([]byte, error) {
	raw, edited, err := readCheckpointForwardBytes(cmd)
	if err != nil {
		return nil, err
	}
	request := struct {
		Raw               *string `json:"raw,omitempty"`
		Payload           *string `json:"payload,omitempty"`
		InterceptResponse bool    `json:"intercept_response,omitempty"`
	}{InterceptResponse: checkpointInterceptResponse}
	if edited {
		item, err := runCheckpointRequest(cmd, http.MethodGet, "/checkpoint/"+id, "forwarding checkpoint item", nil)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &parsed); err != nil {
			return nil, fmt.Errorf("decoding checkpoint item: %w", err)
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		if parsed.Type == "websocket" {
			request.Payload = &encoded
		} else {
			request.Raw = &encoded
		}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encoding checkpoint forward: %w", err)
	}
	return payload, nil
}

func readCheckpointForwardBytes(cmd *cobra.Command) ([]byte, bool, error) {
	if cmd.Flags().Changed("file") {
		raw, err := os.ReadFile(checkpointForwardFile)
		if err != nil {
			return nil, false, fmt.Errorf("reading checkpoint file: %w", err)
		}
		return raw, true, nil
	}
	stdin := cmd.InOrStdin()
	if file, ok := stdin.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return nil, false, fmt.Errorf("checking stdin: %w", err)
		}
		if info.Mode()&os.ModeCharDevice != 0 {
			return nil, false, nil
		}
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, false, fmt.Errorf("reading checkpoint from stdin: %w", err)
	}
	return raw, true, nil
}

func runCheckpointRequest(cmd *cobra.Command, method, path, operation string, payload []byte) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if payload != nil {
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating checkpoint request: %w", err)
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
		return nil, errors.Join(wrapError("reading checkpoint response", readErr), wrapError("closing checkpoint response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return nil, controlAPIError(operation, response.Status, body)
	}
	return body, nil
}

func writeCheckpointList(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding checkpoint list: %w", err)
	}
	for _, item := range response.Items {
		if _, err := fmt.Fprintf(stdout, "%s %s\n", item.ID, item.Type); err != nil {
			return err
		}
	}
	return nil
}

func writeCheckpointGet(body []byte, stdout io.Writer) error {
	var item struct {
		Type    string `json:"type"`
		Raw     []byte `json:"raw"`
		Payload []byte `json:"payload"`
	}
	if err := json.Unmarshal(body, &item); err != nil {
		return fmt.Errorf("decoding checkpoint item: %w", err)
	}
	data := item.Raw
	if item.Type == "websocket" {
		data = item.Payload
	}
	_, err := stdout.Write(data)
	return err
}
