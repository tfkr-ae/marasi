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
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var launchpadCreateName string
var launchpadCreateDescription string
var launchpadUpdateName string
var launchpadUpdateDescription string
var launchpadLinkRequest string
var launchpadLaunchScheme string
var launchpadLaunchRawFile string
var launchpadLaunchDetach bool

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
	launchpadLaunchCmd.Flags().BoolVar(&launchpadLaunchDetach, "detach", false, "Print the launch id and pending status, and return without waiting for the launch to finish")
	launchpadLaunchCmd.MarkFlagRequired("scheme")
	launchpadCmd.AddCommand(launchpadCreateCmd, launchpadListCmd, launchpadGetCmd, launchpadUpdateCmd, launchpadLinkCmd, launchpadLaunchCmd, launchpadPendingCmd, launchpadStatusCmd, launchpadCancelCmd)
	rootCmd.AddCommand(launchpadCmd)
}

var launchpadCmd = &cobra.Command{
	Use:   "launchpad",
	Short: "Create launchpads and launch raw HTTP requests",
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
	Long:  "List launchpads oldest-first. Each row shows the launchpad id, name, description, entry count, and the status code of the newest entry. The status shows - when the launchpad has no entries or its newest entry has no response yet.\n\nWith --json the command writes the control API body: {\"items\":[...]}, where each item has entry_count and last_status_code (null when there is no status).",
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
	Short: "Launch a raw HTTP working copy and wait for its outcome",
	Long:  "Launch a raw HTTP working copy for a launchpad. Pass --raw-file or piped stdin. Set --scheme to http or https. The launch id is the UUID of the traffic pair the launch creates.\n\nBy default the command waits until the launch ends and prints its final record: completed, cancelled, or failed with a reason. A failed launch exits non-zero. An interrupt (Ctrl-C or SIGTERM) while waiting cancels the launch and prints the cancelled record. If the launch finished before the cancel took effect, the command exits non-zero and reports the launch's final status.\n\nWith --detach the command prints the launch id and its pending status, and returns without waiting. Use launchpad cancel to cancel a detached launch.\n\nWith --json the command writes one JSON value: the final launch record, the start response with --detach, or an error.",
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
		return runLaunch(cmd, path, launchpadRequest{Raw: &encoded, Scheme: &launchpadLaunchScheme})
	},
}

var launchpadPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List pending launches",
	Long:  "List the launches that are still pending, including launches started by other clients. Each row shows the launch id, the launchpad id, and the status. A launch leaves the list when it is completed, cancelled, or failed. Use launchpad status to read a finished launch, and launchpad cancel to cancel a pending one.\n\nWith --json the command writes the control API body: {\"items\":[...]}.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runLaunchpadCommand(cmd, http.MethodGet, "/launch", "listing pending launches", "pending", launchpadRequest{})
	},
}

var launchpadStatusCmd = &cobra.Command{
	Use:   "status LAUNCH_ID",
	Short: "Get one launch, pending or finished",
	Long:  "Get a launch by its launch id, including a launch started by another client. The command prints the launch id, the launchpad id, and the status: pending, completed, cancelled, or failed. A finished launch also shows its reason when it has one. The command exits zero for any launch it finds, including a failed launch. An unknown launch id exits non-zero with not_found.\n\nWith --json the command writes the launch record from the control API.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/launch/", args[0], "")
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return readLaunch(ctx, cmd, path)
	},
}

// readLaunch prints the launch at launchPath. Like cancelLaunch, a refused read
// reports the control API error in human mode too, so an unknown launch reads
// as not_found.
func readLaunch(ctx context.Context, cmd *cobra.Command, launchPath string) error {
	const operation = "reading launch"
	called, err := callInstance(ctx, http.MethodGet, launchPath, "", nil)
	if err != nil {
		return err
	}
	if called.StatusCode != http.StatusOK {
		return controlAPIError(operation, called.Status, called.Body)
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(called.Body)
		return err
	}
	return writeLaunchStatusHuman(called.Body, cmd.OutOrStdout())
}

var launchpadCancelCmd = &cobra.Command{
	Use:   "cancel LAUNCH_ID",
	Short: "Cancel a pending launch",
	Long:  "Cancel a pending launch by its launch id, including a launch started by another client. The launch ends as cancelled and its traffic pair records launch.status cancelled. Use it for a launch started with launchpad launch --detach or by another client; interrupting a waiting launchpad launch cancels its own launch. If the launch already finished, the command exits non-zero and reports the launch's final status.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		launchPath, err := serviceIDPath("/launch/", args[0], "")
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return cancelLaunch(ctx, cmd, launchPath)
	},
}

// launchCancelError reports a refused cancel. A launch_finished refusal names
// the launch's final status so the caller sees what actually happened.
func launchCancelError(operation string, response instanceResponse) error {
	var finished struct {
		Error    string `json:"error"`
		LaunchID string `json:"launch_id"`
		Status   string `json:"status"`
		Reason   string `json:"reason"`
	}
	if json.Unmarshal(response.Body, &finished) != nil || finished.Error != "launch_finished" || finished.Status == "" {
		return controlAPIError(operation, response.Status, response.Body)
	}
	if finished.Reason != "" {
		return fmt.Errorf("%s: launch_finished: launch %s is %s (%s)", operation, finished.LaunchID, finished.Status, finished.Reason)
	}
	return fmt.Errorf("%s: launch_finished: launch %s is %s", operation, finished.LaunchID, finished.Status)
}

// launchPollInterval is how often a waiting launch command reads its launch.
const launchPollInterval = 100 * time.Millisecond

type launchRecord struct {
	LaunchID string `json:"launch_id"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

// runLaunch starts a launch and, unless --detach is set, waits for its final
// record. An interrupt while waiting cancels the launch and reports the result.
func runLaunch(cmd *cobra.Command, path string, body launchpadRequest) error {
	const operation = "launching launchpad request"
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding launchpad request: %w", err)
	}
	called, err := callInstance(ctx, http.MethodPost, path, "application/json", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	if err := rejectInstanceStatus(operation, called, jsonOutput); err != nil {
		return err
	}
	var started launchRecord
	if err := json.Unmarshal(called.Body, &started); err != nil || started.LaunchID == "" || started.Status == "" {
		return errors.New("decoding launchpad response")
	}
	if launchpadLaunchDetach && jsonOutput {
		_, err = cmd.OutOrStdout().Write(called.Body)
		return err
	}
	if !jsonOutput {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "launch %s %s\n", started.LaunchID, started.Status); err != nil {
			return err
		}
	}
	if launchpadLaunchDetach {
		return nil
	}

	launchPath, err := serviceIDPath("/launch/", started.LaunchID, "")
	if err != nil {
		return err
	}
	final, err := waitForLaunch(ctx, started.LaunchID, launchPath)
	if err != nil && ctx.Err() != nil {
		// Restore default signal handling so a second interrupt ends the command.
		stop()
		return cancelLaunch(cmd.Context(), cmd, launchPath)
	}
	if err != nil {
		return err
	}
	return writeLaunchRecord(cmd, operation, final)
}

// waitForLaunch reads the launch at launchPath until it leaves pending.
// A failed read names the launch, so the caller knows which launch to look up.
func waitForLaunch(ctx context.Context, launchID, launchPath string) (instanceResponse, error) {
	operation := "reading launch " + launchID
	for {
		called, err := callInstance(ctx, http.MethodGet, launchPath, "", nil)
		if err != nil {
			return instanceResponse{}, err
		}
		if err := rejectInstanceStatus(operation, called, jsonOutput); err != nil {
			return instanceResponse{}, err
		}
		var record launchRecord
		if err := json.Unmarshal(called.Body, &record); err != nil || record.Status == "" {
			return instanceResponse{}, errors.New("decoding launch response")
		}
		if record.Status != "pending" {
			return called, nil
		}
		select {
		case <-ctx.Done():
			return instanceResponse{}, ctx.Err()
		case <-time.After(launchPollInterval):
		}
	}
}

// cancelLaunch cancels the launch at launchPath and prints the cancelled
// record. A launch that finished first is an error that names its final status.
func cancelLaunch(ctx context.Context, cmd *cobra.Command, launchPath string) error {
	const operation = "cancelling launch"
	called, err := callInstance(ctx, http.MethodPost, launchPath+"/cancel", "", nil)
	if err != nil {
		return err
	}
	if called.StatusCode != http.StatusOK {
		return launchCancelError(operation, called)
	}
	return writeLaunchRecord(cmd, operation, called)
}

// writeLaunchRecord prints a final launch record. A failed launch is an error
// that names its reason, so the command exits non-zero.
func writeLaunchRecord(cmd *cobra.Command, operation string, response instanceResponse) error {
	var record launchRecord
	if err := json.Unmarshal(response.Body, &record); err != nil || record.LaunchID == "" || record.Status == "" {
		return errors.New("decoding launch response")
	}
	if record.Status == "failed" {
		if record.Reason != "" {
			return fmt.Errorf("%s: launch %s failed (%s)", operation, record.LaunchID, record.Reason)
		}
		return fmt.Errorf("%s: launch %s failed", operation, record.LaunchID)
	}
	if jsonOutput {
		_, err := cmd.OutOrStdout().Write(response.Body)
		return err
	}
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "launch %s %s\n", record.LaunchID, record.Status)
	return err
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
	case "pending":
		return writeLaunchListHuman(response, cmd.OutOrStdout())
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
	default:
		return fmt.Errorf("unsupported launchpad action %q", action)
	}
}

func writeLaunchpadListHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Description    string `json:"description"`
			EntryCount     int    `json:"entry_count"`
			LastStatusCode *int   `json:"last_status_code"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding launchpad list: %w", err)
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range response.Items {
		// "-" marks a launchpad with no entries or a newest entry still in flight.
		lastStatus := "-"
		if item.LastStatusCode != nil {
			lastStatus = strconv.Itoa(*item.LastStatusCode)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\n", item.ID, truncateDisplay(item.Name, trafficPathDisplayLimit), truncateDisplay(item.Description, trafficPathDisplayLimit), item.EntryCount, lastStatus)
	}
	return writer.Flush()
}

func writeLaunchListHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			LaunchID    string `json:"launch_id"`
			LaunchpadID string `json:"launchpad_id"`
			Status      string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding launch list: %w", err)
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range response.Items {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", item.LaunchID, item.LaunchpadID, item.Status)
	}
	return writer.Flush()
}

func writeLaunchStatusHuman(body []byte, stdout io.Writer) error {
	var record struct {
		LaunchID    string `json:"launch_id"`
		LaunchpadID string `json:"launchpad_id"`
		Status      string `json:"status"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(body, &record); err != nil || record.LaunchID == "" || record.Status == "" {
		return errors.New("decoding launch response")
	}
	if _, err := fmt.Fprintf(stdout, "launch_id: %s\nlaunchpad_id: %s\nstatus: %s\n", record.LaunchID, record.LaunchpadID, record.Status); err != nil {
		return err
	}
	if record.Reason != "" {
		_, err := fmt.Fprintf(stdout, "reason: %s\n", record.Reason)
		return err
	}
	return nil
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
