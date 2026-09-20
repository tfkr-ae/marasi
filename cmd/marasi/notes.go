package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var notesSetFile string

func init() {
	notesSetCmd.Flags().StringVar(&notesSetFile, "file", "", "Read the note from a file")
	notesCmd.AddCommand(notesSetCmd, notesClearCmd)
	rootCmd.AddCommand(notesCmd)
}

var notesCmd = &cobra.Command{
	Use:   "notes",
	Short: "Manage notes for a service instance",
}

var notesSetCmd = &cobra.Command{
	Use:   "set uuid [text]",
	Short: "Set the note on a request/response pair",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		note, err := readNoteSetText(cmd, args)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			Note string `json:"note"`
		}{Note: note})
		if err != nil {
			return fmt.Errorf("encoding note request: %w", err)
		}
		body, err := runNotesRequest(cmd, http.MethodPut, "/notes/"+args[0], "setting note", payload)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "note %s set\n", args[0])
		return err
	},
}

var notesClearCmd = &cobra.Command{
	Use:   "clear uuid",
	Short: "Clear the note on a request/response pair",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runNotesRequest(cmd, http.MethodDelete, "/notes/"+args[0], "clearing note", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "note %s cleared\n", args[0])
		return err
	},
}

func readNoteSetText(cmd *cobra.Command, args []string) (string, error) {
	fileSet := cmd.Flags().Changed("file")
	hasText := len(args) == 2
	stdinPresent, err := noteStdinPresent(cmd)
	if err != nil {
		return "", err
	}
	sources := 0
	if hasText {
		sources++
	}
	if fileSet {
		sources++
	}
	if stdinPresent {
		sources++
	}
	if sources != 1 {
		if sources == 0 {
			return "", errors.New("notes set requires text, --file, or piped stdin")
		}
		return "", errors.New("notes set requires exactly one of text, --file, or piped stdin")
	}
	var note string
	switch {
	case hasText:
		note = args[1]
	case fileSet:
		raw, err := os.ReadFile(notesSetFile)
		if err != nil {
			return "", fmt.Errorf("reading note file: %w", err)
		}
		note = string(raw)
	default:
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("reading note from stdin: %w", err)
		}
		note = string(raw)
	}
	if note == "" {
		return "", errors.New("notes set requires a non-empty note")
	}
	return note, nil
}

func noteStdinPresent(cmd *cobra.Command) (bool, error) {
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

func runNotesRequest(cmd *cobra.Command, method, path, operation string, payload []byte) ([]byte, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var requestBody io.Reader
	if payload != nil {
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating note request: %w", err)
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
		return nil, errors.Join(wrapError("reading note response", readErr), wrapError("closing note response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return nil, controlAPIError(operation, response.Status, body)
	}
	return body, nil
}
