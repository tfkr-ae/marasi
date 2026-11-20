package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

func init() {
	streamMessagesCmd.Flags().StringVar(&streamMessagesLimit, "limit", "200", "Maximum number of items to return, from 1 to 500")
	streamMessagesCmd.Flags().StringVar(&streamMessagesCursor, "cursor", "", "ID of the last message of the page before, to fetch the next page")
	streamInjectCmd.Flags().StringVar(&streamInjectDirection, "direction", "", "Direction to inject into: request (toward the origin) or response (toward the client). Required")
	streamInjectCmd.Flags().StringVar(&streamInjectFile, "file", "", "Read the message's payload from a file. If omitted, use piped stdin or an empty payload")
	streamInjectCmd.Flags().StringVar(&streamInjectCompress, "compress", "auto", "auto compresses the message as its direction compresses (grpc-encoding, connect-content-encoding), never sends it uncompressed")
	streamCmd.AddCommand(streamMessagesCmd, streamGetCmd, streamInjectCmd, streamCancelCmd)
	rootCmd.AddCommand(streamCmd)
}

var streamCmd = &cobra.Command{
	Use:   "stream",
	Short: "Read, inject and cancel the stream messages of streamed pairs (gRPC)",
}

var streamInjectDirection string
var streamInjectFile string
var streamInjectCompress string

var streamMessagesLimit string
var streamMessagesCursor string

// streamMessage is a stream message as the control API returns it.
type streamMessage struct {
	ID         string          `json:"id"`
	PairID     string          `json:"pair_id"`
	Direction  string          `json:"direction"`
	Framing    string          `json:"framing"`
	Flags      *int            `json:"flags"`
	Payload    []byte          `json:"payload"`
	Compressed bool            `json:"compressed"`
	CreatedAt  string          `json:"created_at"`
	Metadata   json.RawMessage `json:"metadata"`
	// Text is the message's text form, which only stream get returns, nil
	// when it has none.
	Text *string `json:"text"`
}

var streamMessagesCmd = &cobra.Command{
	Use:   "messages PAIR_ID",
	Short: "List a streamed pair's stream messages, oldest first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := parseServiceID(args[0])
		if err != nil {
			return err
		}
		limit, err := parsePageLimit(streamMessagesLimit)
		if err != nil {
			return err
		}
		query := url.Values{"limit": {limit}}
		if err := setCursorQuery(query, streamMessagesCursor); err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, "/stream/"+id+"/message?"+query.Encode(), "listing stream messages", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		var page struct {
			Items      []streamMessage `json:"items"`
			NextCursor *string         `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decoding stream messages: %w", err)
		}
		table := newListTable(cmd.OutOrStdout(), "ID", "DIRECTION", "FRAMING", "FLAGS", "PREVIEW")
		for _, message := range page.Items {
			table.row(message.ID, message.Direction, message.Framing, streamFlags(message.Flags), streamPreview(message, 80))
		}
		if err := table.flush(); err != nil {
			return err
		}
		if page.NextCursor != nil {
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "next_cursor=%s\n", *page.NextCursor)
		}
		return err
	},
}

var streamGetCmd = &cobra.Command{
	Use:   "get PAIR_ID MESSAGE_ID",
	Short: "Get one stream message of a streamed pair",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		pairID, err := parseServiceID(args[0])
		if err != nil {
			return err
		}
		id, err := parseServiceID(args[1])
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, "/stream/"+pairID+"/message/"+id, "getting stream message", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		var message streamMessage
		if err := json.Unmarshal(body, &message); err != nil {
			return fmt.Errorf("decoding stream message: %w", err)
		}
		return writeStreamMessageHuman(cmd.OutOrStdout(), message)
	},
}

var streamInjectCmd = &cobra.Command{
	Use:   "inject PAIR_ID",
	Short: "Inject a stream message into a direction of a running streamed pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if streamInjectDirection != "request" && streamInjectDirection != "response" {
			return errors.New("stream inject --direction must be request or response")
		}
		if streamInjectCompress != "auto" && streamInjectCompress != "never" {
			return errors.New("stream inject --compress must be auto or never")
		}
		id, err := parseServiceID(args[0])
		if err != nil {
			return err
		}
		var compress string
		if cmd.Flags().Changed("compress") {
			compress = streamInjectCompress
		}
		raw, err := readInjectedBytes(cmd, streamInjectFile, "stream message")
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			Direction string `json:"direction"`
			Payload   []byte `json:"payload"`
			Compress  string `json:"compress,omitempty"`
		}{streamInjectDirection, raw, compress})
		if err != nil {
			return fmt.Errorf("encoding stream injection: %w", err)
		}
		body, err := runControlRequest(cmd, http.MethodPost, "/stream/"+id+"/inject", "injecting stream message", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		var message streamMessage
		if err := json.Unmarshal(body, &message); err != nil {
			return fmt.Errorf("decoding stream message: %w", err)
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "stream message %s injected\n", message.ID)
		return err
	},
}

var streamCancelCmd = &cobra.Command{
	Use:   "cancel PAIR_ID",
	Short: "Cancel a running streamed pair on both legs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := parseServiceID(args[0])
		if err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodPost, "/stream/"+id+"/cancel", "cancelling streamed pair", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "stream %s cancelled\n", id)
		return err
	},
}

func writeStreamMessageHuman(w io.Writer, m streamMessage) error {
	metadata := string(m.Metadata)
	if metadata == "" {
		metadata = "{}"
	}
	payload := streamPreview(m, -1)
	if !printable(m.Payload) {
		payload += " (--json gives them base64)"
	}
	_, err := fmt.Fprintf(w, "id: %s\npair_id: %s\ndirection: %s\nframing: %s\nflags: %s\ncompressed: %t\ncreated_at: %s\nmetadata: %s\npayload: %s\n",
		m.ID, m.PairID, m.Direction, m.Framing, streamFlags(m.Flags), m.Compressed, m.CreatedAt, metadata, payload)
	if err != nil || m.Text == nil {
		return err
	}
	text := *m.Text
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, err = fmt.Fprintf(w, "text:\n%s", text)
	return err
}

// streamFlags is a message's flags byte, - for a framing without one.
func streamFlags(flags *int) string {
	if flags == nil {
		return "-"
	}
	return strconv.Itoa(*flags)
}

// streamPreview is a message's payload as text, quoted and cut to limit
// runes (no cut when limit is negative), or its size when it is binary or
// was too large to keep.
func streamPreview(m streamMessage, limit int) string {
	var meta struct {
		TooLarge bool  `json:"too_large"`
		SizeSeen int64 `json:"size_seen"`
	}
	_ = json.Unmarshal(m.Metadata, &meta)
	switch {
	case meta.TooLarge:
		return fmt.Sprintf("too large: %d bytes", meta.SizeSeen)
	case !printable(m.Payload):
		return fmt.Sprintf("binary %d bytes", len(m.Payload))
	}
	runes := []rune(string(m.Payload))
	if limit >= 0 && len(runes) > limit {
		runes = runes[:limit]
	}
	quoted := strconv.Quote(string(runes))
	return quoted[1 : len(quoted)-1]
}

// printable reports whether payload is UTF-8 text with no control
// characters but whitespace.
func printable(payload []byte) bool {
	if !utf8.Valid(payload) {
		return false
	}
	for _, r := range string(payload) {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
