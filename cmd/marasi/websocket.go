package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

func init() {
	websocketListCmd.Flags().StringVar(&websocketListLimit, "limit", "200", "Page size")
	websocketListCmd.Flags().StringVar(&websocketListCursor, "cursor", "", "Fetch the next older page")
	websocketMessagesCmd.Flags().StringVar(&websocketMessagesLimit, "limit", "200", "Page size")
	websocketMessagesCmd.Flags().StringVar(&websocketMessagesCursor, "cursor", "", "Fetch the next older page")
	websocketInjectCmd.Flags().StringVar(&websocketInjectDirection, "direction", "", "Origin of the frame: client or server")
	websocketInjectCmd.Flags().IntVar(&websocketInjectOpcode, "opcode", 0, "WebSocket frame opcode (0-15)")
	websocketInjectCmd.Flags().StringVar(&websocketInjectFile, "file", "", "Read frame bytes from a file")
	websocketCmd.AddCommand(websocketListCmd, websocketGetCmd, websocketMessagesCmd, websocketInjectCmd)
	rootCmd.AddCommand(websocketCmd)
	trafficCmd.AddCommand(trafficWebSocketCmd)
}

var websocketCmd = &cobra.Command{
	Use:   "websocket",
	Short: "Inspect WebSocket connections",
}

var websocketListLimit string
var websocketListCursor string
var websocketMessagesLimit string
var websocketMessagesCursor string
var websocketInjectDirection string
var websocketInjectOpcode int
var websocketInjectFile string

var websocketInjectCmd = &cobra.Command{
	Use:   "inject CONNECTION_ID",
	Short: "Inject a frame into a live WebSocket connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("direction") || websocketInjectDirection != "client" && websocketInjectDirection != "server" {
			return errors.New("websocket inject --direction must be client or server")
		}
		if !cmd.Flags().Changed("opcode") || websocketInjectOpcode < 0 || websocketInjectOpcode > 15 {
			return errors.New("websocket inject --opcode must be an integer from 0 to 15")
		}
		var raw []byte
		var err error
		if cmd.Flags().Changed("file") {
			raw, err = os.ReadFile(websocketInjectFile)
			if err != nil {
				return fmt.Errorf("reading websocket file: %w", err)
			}
		} else {
			stdin := cmd.InOrStdin()
			if file, ok := stdin.(*os.File); ok {
				info, err := file.Stat()
				if err != nil {
					return fmt.Errorf("checking stdin: %w", err)
				}
				if info.Mode()&os.ModeCharDevice != 0 {
					stdin = nil
				}
			}
			if stdin != nil {
				raw, err = io.ReadAll(stdin)
				if err != nil {
					return fmt.Errorf("reading websocket from stdin: %w", err)
				}
			}
		}
		payload, err := json.Marshal(struct {
			Direction string `json:"direction"`
			Opcode    int    `json:"opcode"`
			Payload   string `json:"payload"`
		}{websocketInjectDirection, websocketInjectOpcode, base64.StdEncoding.EncodeToString(raw)})
		if err != nil {
			return fmt.Errorf("encoding websocket injection: %w", err)
		}
		body, err := runCheckpointRequest(cmd, http.MethodPost, "/websocket/"+url.PathEscape(args[0])+"/inject", "injecting websocket message", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "websocket %s injected\n", args[0])
		return err
	},
}

var websocketListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved WebSocket connections",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		query := url.Values{"limit": {websocketListLimit}}
		if websocketListCursor != "" {
			query.Set("cursor", websocketListCursor)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/websocket?"+query.Encode(), nil)
		if err != nil {
			return fmt.Errorf("creating websocket list request: %w", err)
		}
		client := service.NewClient(instancePath + ".sock")
		defer client.Close()
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("instance %s is not running", instance)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(wrapError("reading websocket list response", readErr), wrapError("closing websocket list response", closeErr))
		}
		if response.StatusCode != http.StatusOK {
			return controlAPIError("listing websocket connections", response.Status, body)
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		var page struct {
			Items []struct {
				ID        string `json:"id"`
				RequestID string `json:"request_id"`
				State     string `json:"state"`
				Transport string `json:"transport"`
				Host      string `json:"host"`
				Path      string `json:"path"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decoding websocket list: %w", err)
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		for _, item := range page.Items {
			path := item.Path
			pathRunes := []rune(path)
			if len(pathRunes) > trafficPathDisplayLimit {
				path = string(pathRunes[:trafficPathDisplayLimit-3]) + "..."
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", item.ID, item.RequestID, item.State, item.Transport, item.Host, path)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if page.NextCursor != nil {
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "next_cursor=%s\n", *page.NextCursor)
		}
		return err
	},
}

var websocketGetCmd = &cobra.Command{
	Use:   "get CONNECTION_ID",
	Short: "Get one WebSocket connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return getWebSocketConnection(cmd, "/websocket/"+args[0], "getting websocket connection")
	},
}

var websocketMessagesCmd = &cobra.Command{
	Use:   "messages CONNECTION_ID",
	Short: "List stored WebSocket messages",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		query := url.Values{"limit": {websocketMessagesLimit}}
		if websocketMessagesCursor != "" {
			query.Set("cursor", websocketMessagesCursor)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/websocket/"+url.PathEscape(args[0])+"/message?"+query.Encode(), nil)
		if err != nil {
			return fmt.Errorf("creating websocket messages request: %w", err)
		}
		client := service.NewClient(instancePath + ".sock")
		defer client.Close()
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("instance %s is not running", instance)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(wrapError("reading websocket messages response", readErr), wrapError("closing websocket messages response", closeErr))
		}
		if response.StatusCode != http.StatusOK {
			return controlAPIError("listing websocket messages", response.Status, body)
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		var page struct {
			Items []struct {
				ID        string `json:"id"`
				Direction string `json:"direction"`
				Opcode    int    `json:"opcode"`
				Payload   string `json:"payload"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decoding websocket messages: %w", err)
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		for _, message := range page.Items {
			payload, err := base64.StdEncoding.DecodeString(message.Payload)
			if err != nil {
				return fmt.Errorf("decoding websocket message payload: %w", err)
			}
			preview := fmt.Sprintf("binary %d bytes", len(payload))
			if message.Opcode == 1 && utf8.Valid(payload) {
				runes := []rune(string(payload))
				if len(runes) > 80 {
					runes = runes[:80]
				}
				quoted := strconv.Quote(string(runes))
				preview = quoted[1 : len(quoted)-1]
			}
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\n", message.ID, message.Direction, message.Opcode, preview)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if page.NextCursor != nil {
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "next_cursor=%s\n", *page.NextCursor)
		}
		return err
	},
}

var trafficWebSocketCmd = &cobra.Command{
	Use:   "websocket REQUEST_ID",
	Short: "Get the WebSocket connection opened by a traffic pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return getWebSocketConnection(cmd, "/traffic/"+args[0]+"/websocket", "getting traffic websocket")
	},
}

func getWebSocketConnection(cmd *cobra.Command, path, operation string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi"+path, nil)
	if err != nil {
		return fmt.Errorf("creating websocket request: %w", err)
	}
	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("instance %s is not running", instance)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(wrapError("reading websocket response", readErr), wrapError("closing websocket response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return controlAPIError(operation, response.Status, body)
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}
	return writeWebSocketConnectionHuman(body, cmd.OutOrStdout())
}

func writeWebSocketConnectionHuman(body []byte, stdout io.Writer) error {
	var connection struct {
		ID          string  `json:"id"`
		RequestID   string  `json:"request_id"`
		State       string  `json:"state"`
		Transport   string  `json:"transport"`
		Host        string  `json:"host"`
		Path        string  `json:"path"`
		StartedAt   string  `json:"started_at"`
		ClosedAt    *string `json:"closed_at"`
		CloseCode   int     `json:"close_code"`
		CloseReason string  `json:"close_reason"`
	}
	if err := json.Unmarshal(body, &connection); err != nil {
		return fmt.Errorf("decoding websocket connection: %w", err)
	}
	closedAt := "null"
	if connection.ClosedAt != nil {
		closedAt = *connection.ClosedAt
	}
	_, err := fmt.Fprintf(stdout,
		"id: %s\nrequest_id: %s\nstate: %s\ntransport: %s\nhost: %s\npath: %s\nstarted_at: %s\nclosed_at: %s\nclose_code: %d\nclose_reason: %s\n",
		connection.ID,
		connection.RequestID,
		connection.State,
		connection.Transport,
		connection.Host,
		connection.Path,
		connection.StartedAt,
		closedAt,
		connection.CloseCode,
		connection.CloseReason,
	)
	return err
}
