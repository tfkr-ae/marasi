package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var trafficListHost string
var trafficListMethod string
var trafficListStatusCode string
var trafficListPath string
var trafficListLimit string
var trafficListCursor string
var trafficMetadataUpdateFile string

const trafficPathDisplayLimit = 40

func init() {
	trafficListCmd.Flags().StringVar(&trafficListHost, "host", "", "Keep only this exact host")
	trafficListCmd.Flags().StringVar(&trafficListMethod, "method", "", "Keep only this exact method")
	trafficListCmd.Flags().StringVar(&trafficListStatusCode, "status-code", "", "Keep only this exact status code")
	trafficListCmd.Flags().StringVar(&trafficListPath, "path", "", "Keep pairs whose path starts with this prefix")
	trafficListCmd.Flags().StringVar(&trafficListLimit, "limit", "200", "Page size")
	trafficListCmd.Flags().StringVar(&trafficListCursor, "cursor", "", "Fetch the next older page")
	trafficMetadataUpdateCmd.Flags().StringVar(&trafficMetadataUpdateFile, "file", "", "Read the metadata JSON from a file")
	trafficMetadataCmd.AddCommand(trafficMetadataGetCmd, trafficMetadataUpdateCmd)
	trafficCmd.AddCommand(trafficListCmd, trafficGetCmd, trafficMetadataCmd)
	rootCmd.AddCommand(trafficCmd)
}

var trafficCmd = &cobra.Command{
	Use:   "traffic",
	Short: "Inspect traffic for a service instance",
}

var trafficListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the newest page of traffic",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return listTraffic(ctx, instancePath, instance, jsonOutput, trafficListQuery(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var trafficGetCmd = &cobra.Command{
	Use:   "get uuid",
	Short: "Get one request/response pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return getTraffic(ctx, instancePath, instance, jsonOutput, args[0], cmd.OutOrStdout())
	},
}

var trafficMetadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Get or replace metadata for a request/response pair",
}

var trafficMetadataGetCmd = &cobra.Command{
	Use:   "get uuid",
	Short: "Get metadata for a request/response pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runTrafficMetadataRequest(cmd, http.MethodGet, "/traffic/"+args[0]+"/metadata", "getting metadata", nil)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(body)
		return err
	},
}

var trafficMetadataUpdateCmd = &cobra.Command{
	Use:   "update uuid",
	Short: "Replace metadata for a request/response pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		payload, err := readTrafficMetadataUpdate(cmd)
		if err != nil {
			return err
		}
		body, err := runTrafficMetadataRequest(cmd, http.MethodPut, "/traffic/"+args[0]+"/metadata", "updating metadata", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "metadata %s updated\n", args[0])
		return err
	},
}

// trafficListQuery encodes the traffic list flags as a query string.
func trafficListQuery() string {
	query := url.Values{}
	query.Set("limit", trafficListLimit)
	if trafficListHost != "" {
		query.Set("host", trafficListHost)
	}
	if trafficListMethod != "" {
		query.Set("method", trafficListMethod)
	}
	if trafficListStatusCode != "" {
		query.Set("status_code", trafficListStatusCode)
	}
	if trafficListPath != "" {
		query.Set("path", trafficListPath)
	}
	if trafficListCursor != "" {
		query.Set("cursor", trafficListCursor)
	}
	return query.Encode()
}

// listTraffic prints one page of traffic for the instance.
func listTraffic(ctx context.Context, instancePath, instanceName string, asJSON bool, query string, stdout, stderr io.Writer) error {
	socketPath := instancePath + ".sock"
	client := service.NewClient(socketPath)
	defer client.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/traffic?"+query, nil)
	if err != nil {
		return fmt.Errorf("creating traffic list request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("instance %s is not running", instanceName)
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(
			wrapError("reading traffic list response", readErr),
			wrapError("closing traffic list response", closeErr),
		)
	}
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return controlAPIError("listing traffic", response.Status, body)
		}
		return fmt.Errorf("listing traffic: %s", response.Status)
	}
	if asJSON {
		if _, err := stdout.Write(body); err != nil {
			return err
		}
	} else {
		if err := writeTrafficListHuman(body, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

// writeTrafficListHuman writes a tab-separated traffic page and the next cursor, if any.
func writeTrafficListHuman(body []byte, stdout, stderr io.Writer) error {
	var page struct {
		Items []struct {
			ID         string `json:"id"`
			Method     string `json:"method"`
			Host       string `json:"host"`
			Path       string `json:"path"`
			StatusCode int    `json:"status_code"`
			Length     string `json:"length"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decoding traffic list: %w", err)
	}

	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range page.Items {
		path := item.Path
		pathRunes := []rune(path)
		if len(pathRunes) > trafficPathDisplayLimit {
			path = string(pathRunes[:trafficPathDisplayLimit-3]) + "..."
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%s\n", item.ID, item.Method, item.Host, path, item.StatusCode, item.Length)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if page.NextCursor != nil {
		fmt.Fprintf(stderr, "next_cursor=%s\n", *page.NextCursor)
	}
	return nil
}

// getTraffic prints one request/response pair.
func getTraffic(ctx context.Context, instancePath, instanceName string, asJSON bool, id string, stdout io.Writer) error {
	socketPath := instancePath + ".sock"
	client := service.NewClient(socketPath)
	defer client.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/traffic/"+id, nil)
	if err != nil {
		return fmt.Errorf("creating traffic get request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("instance %s is not running", instanceName)
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(
			wrapError("reading traffic get response", readErr),
			wrapError("closing traffic get response", closeErr),
		)
	}
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return controlAPIError("getting traffic", response.Status, body)
		}
		return fmt.Errorf("getting traffic: %s", response.Status)
	}
	if asJSON {
		if _, err := stdout.Write(body); err != nil {
			return err
		}
	} else {
		if err := writeTrafficGetHuman(body, stdout); err != nil {
			return err
		}
	}
	return nil
}

// controlAPIError formats a control API failure.
// A JSON string error field is preferred over status. A message is appended when the API sends one.
func controlAPIError(operation, status string, body []byte) error {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	var message string
	if json.Unmarshal(body, &payload) == nil && json.Unmarshal(payload.Error, &message) == nil {
		if payload.Message != "" {
			return fmt.Errorf("%s: %s: %s", operation, message, payload.Message)
		}
		return fmt.Errorf("%s: %s", operation, message)
	}
	return fmt.Errorf("%s: %s", operation, status)
}

// writeTrafficGetHuman writes one traffic pair as labeled fields and raw HTTP messages.
func writeTrafficGetHuman(body []byte, stdout io.Writer) error {
	var detail struct {
		ID       string          `json:"id"`
		Note     string          `json:"note"`
		Metadata json.RawMessage `json:"metadata"`
		Request  struct {
			Scheme      string `json:"scheme"`
			Method      string `json:"method"`
			Host        string `json:"host"`
			Path        string `json:"path"`
			Raw         []byte `json:"raw"`
			RequestedAt string `json:"requested_at"`
		} `json:"request"`
		Response struct {
			Status      string `json:"status"`
			StatusCode  int    `json:"status_code"`
			ContentType string `json:"content_type"`
			Length      string `json:"length"`
			Raw         []byte `json:"raw"`
			RespondedAt string `json:"responded_at"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return fmt.Errorf("decoding traffic: %w", err)
	}

	fmt.Fprintf(stdout, "id: %s\n", detail.ID)
	fmt.Fprintf(stdout, "scheme: %s\n", detail.Request.Scheme)
	fmt.Fprintf(stdout, "method: %s\n", detail.Request.Method)
	fmt.Fprintf(stdout, "host: %s\n", detail.Request.Host)
	fmt.Fprintf(stdout, "path: %s\n", detail.Request.Path)
	fmt.Fprintf(stdout, "requested_at: %s\n", detail.Request.RequestedAt)
	fmt.Fprintf(stdout, "status: %s\n", detail.Response.Status)
	fmt.Fprintf(stdout, "status_code: %d\n", detail.Response.StatusCode)
	fmt.Fprintf(stdout, "content_type: %s\n", detail.Response.ContentType)
	fmt.Fprintf(stdout, "length: %s\n", detail.Response.Length)
	fmt.Fprintf(stdout, "responded_at: %s\n", detail.Response.RespondedAt)
	if detail.Note != "" {
		fmt.Fprintf(stdout, "note: %s\n", detail.Note)
	}
	var metadata map[string]any
	if err := json.Unmarshal(detail.Metadata, &metadata); err == nil && len(metadata) > 0 {
		fmt.Fprintf(stdout, "metadata: %s\n", detail.Metadata)
	}
	if err := writeTrafficRaw(stdout, "request", detail.Request.Raw); err != nil {
		return err
	}
	if utf8.Valid(detail.Request.Raw) && len(detail.Request.Raw) > 0 && detail.Request.Raw[len(detail.Request.Raw)-1] != '\n' {
		if _, err := fmt.Fprintln(stdout); err != nil {
			return err
		}
	}
	if detail.Response.StatusCode == -1 {
		fmt.Fprintln(stdout, "no response yet")
		return nil
	}
	return writeTrafficRaw(stdout, "response", detail.Response.Raw)
}

// writeTrafficRaw writes raw if it is valid UTF-8, otherwise a length note.
func writeTrafficRaw(stdout io.Writer, side string, raw []byte) error {
	if utf8.Valid(raw) {
		_, err := stdout.Write(raw)
		return err
	}
	_, err := fmt.Fprintf(stdout, "%s raw: %d bytes, not utf-8\n", side, len(raw))
	return err
}

func readTrafficMetadataUpdate(cmd *cobra.Command) ([]byte, error) {
	fileSet := cmd.Flags().Changed("file")
	stdinPresent, err := trafficMetadataStdinPresent(cmd)
	if err != nil {
		return nil, err
	}
	if fileSet && stdinPresent {
		return nil, errors.New("traffic metadata update requires exactly one of --file or piped stdin")
	}
	if !fileSet && !stdinPresent {
		return nil, errors.New("traffic metadata update requires --file or piped stdin")
	}
	var raw []byte
	if fileSet {
		var err error
		raw, err = os.ReadFile(trafficMetadataUpdateFile)
		if err != nil {
			return nil, fmt.Errorf("reading metadata file: %w", err)
		}
	} else {
		var err error
		raw, err = io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("reading metadata from stdin: %w", err)
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("traffic metadata update requires a non-empty body")
	}
	return raw, nil
}

func trafficMetadataStdinPresent(cmd *cobra.Command) (bool, error) {
	stdin := cmd.InOrStdin()
	file, ok := stdin.(*os.File)
	if !ok {
		return true, nil
	}
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("checking stdin: %w", err)
	}
	return info.Mode()&os.ModeCharDevice == 0, nil
}

func runTrafficMetadataRequest(cmd *cobra.Command, method, path, operation string, payload []byte) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if payload != nil {
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating metadata request: %w", err)
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
		return nil, errors.Join(wrapError("reading metadata response", readErr), wrapError("closing metadata response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return nil, controlAPIError(operation, response.Status, body)
	}
	return body, nil
}
