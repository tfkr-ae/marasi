package main

import (
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
)

var trafficListQueryText string
var trafficListLimit string
var trafficListCursor string
var trafficMetadataUpdateFile string

const trafficPathDisplayLimit = 40

const trafficListLong = `List the newest page of traffic.

--query (-q) takes an AIP-160 query that narrows the list. Fields:

  host, method, scheme, path, content_type
      = and !=, with exact case. A * at the start or end of the value
      matches anything there, for example host = "*.example.com" or
      path = "/api/*".
  status_code
      = != < <= > >=, for example status_code >= 500.
  requested_at, responded_at
      = != < <= > >= against an RFC 3339 timestamp, for example
      requested_at > "2024-01-02T15:04:05Z".
  metadata.<key>
      = and != against the JSON value at that key, for example
      metadata.extension = "workshop".
  request_head, request_body, response_head, response_body
      : (contains), ignoring case, for example response_body:"password".
      A head is the request or status line plus headers. Binary bodies
      are not searched.

Bare text, for example "hbGci", searches every text part. Text terms
need at least 3 characters.

Combine conditions with AND, OR, NOT, -, and parentheses. A space
between conditions means AND. OR binds tighter than AND, so
a AND b OR c means a AND (b OR c). Use parentheses to group otherwise.`

func init() {
	trafficListCmd.Flags().StringVarP(&trafficListQueryText, "query", "q", "", "AIP-160 query that narrows the list; see the fields above")
	trafficListCmd.Flags().StringVar(&trafficListLimit, "limit", "200", "Maximum number of items to return, from 1 to 500")
	trafficListCmd.Flags().StringVar(&trafficListCursor, "cursor", "", "UUID of the last item, used to fetch the next older page")
	trafficMetadataUpdateCmd.Flags().StringVar(&trafficMetadataUpdateFile, "file", "", "Read the metadata JSON from a file instead of stdin")
	trafficMetadataCmd.AddCommand(trafficMetadataGetCmd, trafficMetadataUpdateCmd)
	trafficCmd.AddCommand(trafficListCmd, trafficGetCmd, trafficMetadataCmd)
	rootCmd.AddCommand(trafficCmd)
}

var trafficCmd = &cobra.Command{
	Use:   "traffic",
	Short: "List and get captured traffic",
}

var trafficListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the newest page of traffic",
	Long:  trafficListLong,
	Example: `  marasi traffic list -q 'host = "*.example.com" AND status_code >= 500'
  marasi traffic list -q 'method = "POST" path = "/api/*"'
  marasi traffic list -q 'metadata.extension = "workshop" AND NOT status_code = 404'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		query, err := trafficListQuery()
		if err != nil {
			return err
		}
		return listTraffic(ctx, instancePath, instance, jsonOutput, query, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var trafficGetCmd = &cobra.Command{
	Use:   "get UUID",
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
	Use:   "get UUID",
	Short: "Get metadata for a request/response pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/traffic/", args[0], "/metadata")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, path, "getting metadata", nil)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(body)
		return err
	},
}

var trafficMetadataUpdateCmd = &cobra.Command{
	Use:   "update UUID",
	Short: "Replace metadata for a request/response pair",
	Long:  "Replace metadata for a request/response pair. Pass exactly one of --file and piped stdin.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		payload, err := readTrafficMetadataUpdate(cmd)
		if err != nil {
			return err
		}
		path, err := serviceIDPath("/traffic/", args[0], "/metadata")
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodPut, path, "updating metadata", payload)
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
func trafficListQuery() (string, error) {
	limit, err := parsePageLimit(trafficListLimit)
	if err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("limit", limit)
	if trafficListQueryText != "" {
		query.Set("q", trafficListQueryText)
	}
	if err := setCursorQuery(query, trafficListCursor); err != nil {
		return "", err
	}
	return query.Encode(), nil
}

// listTraffic prints one page of traffic for the instance.
func listTraffic(ctx context.Context, instancePath, instanceName string, asJSON bool, query string, stdout, stderr io.Writer) error {
	response, err := dialInstance(ctx, instancePath+".sock", instanceName, http.MethodGet, "/traffic?"+query, "", nil)
	if err != nil {
		return err
	}
	body := response.Body
	if response.StatusCode != http.StatusOK {
		if asJSON || isInvalidQuery(body) {
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
// A page that reports an incomplete traffic index also gets a notice on stderr;
// pages without an index field, such as Launchpad members, get none.
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
		Index      *struct {
			Complete bool `json:"complete"`
		} `json:"index"`
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
	if page.Index != nil && !page.Index.Complete {
		fmt.Fprintln(stderr, "notice: the traffic index is still building; text results may be incomplete")
	}
	return nil
}

// getTraffic prints one request/response pair.
func getTraffic(ctx context.Context, instancePath, instanceName string, asJSON bool, id string, stdout io.Writer) error {
	id, err := parseServiceID(id)
	if err != nil {
		return err
	}
	response, err := dialInstance(ctx, instancePath+".sock", instanceName, http.MethodGet, "/traffic/"+id, "", nil)
	if err != nil {
		return err
	}
	body := response.Body
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
// A query error's position is appended so it survives into the CLI error.
func controlAPIError(operation, status string, body []byte) error {
	var payload struct {
		Error    json.RawMessage `json:"error"`
		Message  string          `json:"message"`
		Position int             `json:"position"`
	}
	var message string
	if json.Unmarshal(body, &payload) == nil && json.Unmarshal(payload.Error, &message) == nil {
		if payload.Message != "" && payload.Position > 0 {
			return fmt.Errorf("%s: %s: %s at position %d", operation, message, payload.Message, payload.Position)
		}
		if payload.Message != "" {
			return fmt.Errorf("%s: %s: %s", operation, message, payload.Message)
		}
		return fmt.Errorf("%s: %s", operation, message)
	}
	return fmt.Errorf("%s: %s", operation, status)
}

// isInvalidQuery reports whether a control API error body is an invalid_query
// error, whose message the user needs to fix the query.
func isInvalidQuery(body []byte) bool {
	var payload struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Error == "invalid_query"
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
