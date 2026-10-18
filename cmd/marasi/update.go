package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/internal/update"
	"github.com/tfkr-ae/marasi/service"
)

// updateNotesLines is how many lines of release notes human output shows.
const updateNotesLines = 5

// reinstallWarning is the error reinstall gives without --yes (ADR-0049).
const reinstallWarning = "reinstalling replaces the installed build with the latest build on its channel, even when that build is older, " +
	"and projects opened in a newer build may not open in it; rerun with --yes to reinstall"

var reinstallConfirmed bool

func init() {
	updateReinstallCmd.Flags().BoolVar(&reinstallConfirmed, "yes", false, "Confirm the reinstall, even when the latest build is older than the installed build")
	updateCmd.AddCommand(updateCheckCmd, updateInstallCmd, updateChannelCmd, updateReinstallCmd)
	rootCmd.AddCommand(updateCmd)
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Show and install updates through a service instance",
	Long: "Show the installed build, its channel, and any update. " +
		"A running service instance checks for and installs updates; start one with marasi service start.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runUpdateStatus(cmd, http.MethodGet, "/update", "getting update status")
	},
}

var updateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check the channel for an update now",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runUpdateStatus(cmd, http.MethodPost, "/update/check", "checking for updates")
	},
}

var updateChannelCmd = &cobra.Command{
	Use:   "channel [release|nightly]",
	Short: "Print or switch the update channel",
	Long: "Print the update channel, or switch it to release or nightly. " +
		"Switching never installs; it never moves to an older build either (use marasi update reinstall --yes for that).",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			body, err := updateRequest(cmd, http.MethodGet, "/update", "getting update channel", nil, http.StatusOK)
			if err != nil {
				return err
			}
			return writeUpdateChannel(cmd, body, "")
		}
		channel, err := update.ParseChannel(args[0])
		if err != nil {
			return fmt.Errorf("unknown channel %q: choose release or nightly", args[0])
		}
		request, err := json.Marshal(struct {
			Channel update.Channel `json:"channel"`
		}{Channel: channel})
		if err != nil {
			return fmt.Errorf("encoding channel request: %w", err)
		}
		body, err := updateRequest(cmd, http.MethodPut, "/update/channel", "switching update channel", request, http.StatusOK)
		if err != nil {
			return err
		}
		return writeUpdateChannel(cmd, body, channel)
	},
}

var updateInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the update and follow its progress",
	Long: "Install the update on the channel through the service instance and follow its progress. " +
		"Ctrl-C stops following; the install continues on the instance. With --json, writes one event per line.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return followUpdateInstall(cmd, "/update/install", nil, "installing update", "installing")
	},
}

var updateReinstallCmd = &cobra.Command{
	Use:   "reinstall",
	Short: "Install the latest build on the channel, even when it is older",
	Long: "Install the latest build on the channel, even when it is older than the installed build. " +
		"Projects opened in a newer build may not open in it, so --yes is required.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !reinstallConfirmed {
			return errors.New(reinstallWarning)
		}
		return followUpdateInstall(cmd, "/update/reinstall", []byte(`{"confirm":true}`), "reinstalling", "reinstalling")
	},
}

// withStartHint tells the user to start an instance when none is running,
// since only an instance checks for and installs updates (ADR-0050).
func withStartHint(err error) error {
	var notRunning instanceNotRunningError
	if !errors.As(err, &notRunning) {
		return err
	}
	start := "marasi service start"
	if notRunning.name != "default" {
		start += " --instance " + notRunning.name
	}
	return fmt.Errorf("%w; start one with %s", err, start)
}

// updateRequest sends one update request and returns the body of a
// wantStatus response.
func updateRequest(cmd *cobra.Command, method, path, operation string, body []byte, wantStatus int) ([]byte, error) {
	response, err := runControlRequestStatus(cmd, method, path, operation, body, wantStatus)
	return response, withStartHint(err)
}

func runUpdateStatus(cmd *cobra.Command, method, path, operation string) error {
	body, err := updateRequest(cmd, method, path, operation, nil, http.StatusOK)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSONBody(cmd, body)
	}
	return writeUpdateStatusHuman(body, cmd.OutOrStdout())
}

type updateBuild struct {
	Version   string  `json:"version"`
	Channel   string  `json:"channel"`
	Published *string `json:"published"`
	Notes     string  `json:"notes"`
	Page      string  `json:"page"`
}

type updateInstall struct {
	State    string `json:"state"`
	Version  string `json:"version"`
	Step     string `json:"step"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Error    *struct {
		Kind    string `json:"kind"`
		Step    string `json:"step"`
		Message string `json:"message"`
		Page    string `json:"page"`
	} `json:"error"`
}

type updateStatus struct {
	Installed struct {
		Version string  `json:"version"`
		Channel *string `json:"channel"`
	} `json:"installed"`
	Development bool    `json:"development"`
	Channel     *string `json:"channel"`
	AutoCheck   bool    `json:"auto_check"`
	LastCheck   *struct {
		At    string  `json:"at"`
		Error *string `json:"error"`
	} `json:"last_check"`
	Available       *updateBuild  `json:"available"`
	Install         updateInstall `json:"install"`
	RestartRequired bool          `json:"restart_required"`
	InstallPath     string        `json:"install_path"`
}

// writeUpdateStatusHuman writes GET /update as key: value lines, with the
// first lines of the available build's notes and a link to the rest.
func writeUpdateStatusHuman(body []byte, stdout io.Writer) error {
	var status updateStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("decoding update status: %w", err)
	}
	var out strings.Builder
	installed := status.Installed.Version
	switch {
	case status.Development:
		installed += " (development build)"
	case status.Installed.Channel != nil:
		installed += " (" + *status.Installed.Channel + ")"
	}
	fmt.Fprintf(&out, "installed: %s\n", installed)
	channel := "none"
	if status.Channel != nil {
		channel = *status.Channel
	}
	fmt.Fprintf(&out, "channel: %s\n", channel)
	switch {
	case status.Development:
		out.WriteString("auto check: off (development build)\n")
	case status.AutoCheck:
		out.WriteString("auto check: on\n")
	default:
		out.WriteString("auto check: off\n")
	}
	switch {
	case status.LastCheck == nil:
		out.WriteString("last check: never\n")
	case status.LastCheck.Error != nil:
		fmt.Fprintf(&out, "last check: %s failed: %s\n", status.LastCheck.At, *status.LastCheck.Error)
	default:
		fmt.Fprintf(&out, "last check: %s\n", status.LastCheck.At)
	}
	if build := status.Available; build == nil {
		out.WriteString("available: none\n")
	} else {
		detail := build.Channel
		if build.Published != nil {
			detail += ", published " + *build.Published
		}
		fmt.Fprintf(&out, "available: %s (%s)\n", build.Version, detail)
		if build.Page != "" {
			fmt.Fprintf(&out, "page: %s\n", build.Page)
		}
		writeUpdateNotes(&out, build.Notes)
	}
	fmt.Fprintf(&out, "install: %s\n", describeInstall(status.Install))
	fmt.Fprintf(&out, "restart required: %s\n", yesNo(status.RestartRequired))
	fmt.Fprintf(&out, "install path: %s\n", status.InstallPath)
	_, err := io.WriteString(stdout, out.String())
	return wrapError("writing update status", err)
}

// writeUpdateNotes writes the first lines of notes, indented, and how many
// more lines the release page has.
func writeUpdateNotes(out *strings.Builder, notes string) {
	notes = strings.Trim(strings.ReplaceAll(notes, "\r\n", "\n"), "\n")
	if strings.TrimSpace(notes) == "" {
		return
	}
	lines := strings.Split(notes, "\n")
	out.WriteString("notes:\n")
	for _, line := range lines[:min(len(lines), updateNotesLines)] {
		fmt.Fprintf(out, "  %s\n", line)
	}
	if more := len(lines) - updateNotesLines; more > 0 {
		fmt.Fprintf(out, "  ... %d more lines on the page\n", more)
	}
}

func describeInstall(install updateInstall) string {
	switch install.State {
	case "running":
		return fmt.Sprintf("running %s, %s", install.Version, describeStep(install.Step, install.Received, install.Total, false))
	case "installed":
		return "installed " + install.Version
	case "failed":
		if install.Error != nil {
			return fmt.Sprintf("failed %s at %s: %s", install.Version, install.Error.Step, install.Error.Message)
		}
		return fmt.Sprintf("failed %s at %s", install.Version, install.Step)
	}
	return install.State
}

const mebibyte = 1 << 20

// describeStep writes an install step and, while downloading, how far it
// got. coarse rounds down to a tenth (or to 10 MiB when the size is
// unknown), so that following an install prints one line per tenth.
func describeStep(step string, received, total int64, coarse bool) string {
	if step != "download" {
		return step
	}
	if total > 0 {
		percent := received * 100 / total
		if coarse {
			percent = percent / 10 * 10
		}
		return fmt.Sprintf("download %d%% of %.1f MiB", percent, float64(total)/mebibyte)
	}
	if coarse {
		received = received / (10 * mebibyte) * (10 * mebibyte)
	}
	return fmt.Sprintf("download %.1f MiB", float64(received)/mebibyte)
}

// writeUpdateChannel prints the configured channel. After a switch from a
// nightly build to release it says the nightly stays (ADR-0049).
func writeUpdateChannel(cmd *cobra.Command, body []byte, switchedTo update.Channel) error {
	if jsonOutput {
		return writeJSONBody(cmd, body)
	}
	var status updateStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("decoding update status: %w", err)
	}
	channel := "none"
	if status.Channel != nil {
		channel = *status.Channel
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "channel: %s\n", channel); err != nil {
		return fmt.Errorf("writing update channel: %w", err)
	}
	if switchedTo == update.Release && status.Installed.Channel != nil && *status.Installed.Channel == string(update.Nightly) {
		_, err := io.WriteString(cmd.ErrOrStderr(), "the installed nightly build stays until the release channel has a newer build; "+
			"marasi update reinstall --yes installs the latest release build now\n")
		return wrapError("writing update channel", err)
	}
	return nil
}

// eventStream is a connected GET /events subscription.
type eventStream struct {
	frames chan eventFrame
	body   io.Closer
	client *service.Client
}

// subscribeEvents connects to the instance's events and returns once the
// subscription is connected, so no later event is missed.
func subscribeEvents(ctx context.Context, socketPath, instanceName string) (*eventStream, error) {
	client := service.NewClient(socketPath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/events", nil)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("creating events request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		client.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, instanceNotRunningError{name: instanceName}
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		client.Close()
		return nil, fmt.Errorf("subscribing to events: %s", response.Status)
	}
	reader := bufio.NewReader(response.Body)
	err = readEventFrames(reader, func(line string) error {
		if line == ": connected" {
			return errEventsConnected
		}
		return nil
	}, nil)
	if !errors.Is(err, errEventsConnected) {
		response.Body.Close()
		client.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("instance %s closed the event subscription before connecting", instanceName)
	}
	stream := &eventStream{frames: make(chan eventFrame, 256), body: response.Body, client: client}
	go stream.read(reader)
	return stream, nil
}

// errEventsConnected ends the read of a subscription's handshake.
var errEventsConnected = errors.New("connected")

func (s *eventStream) read(reader *bufio.Reader) {
	defer close(s.frames)
	_ = readEventFrames(reader, nil, func(frame eventFrame) error {
		s.frames <- frame
		return nil
	})
}

func (s *eventStream) close() {
	s.body.Close()
	s.client.Close()
	for range s.frames {
	}
}

// followUpdateInstall subscribes to events, starts or joins an install at
// path, and follows it until it is installed or fails. Ctrl-C only stops
// following: the install continues on the instance (ADR-0050).
func followUpdateInstall(cmd *cobra.Command, path string, body []byte, operation, verb string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	stream, err := subscribeEvents(ctx, instancePath+".sock", instance)
	if err != nil {
		return withStartHint(err)
	}
	defer stream.close()
	responseBody, err := updateRequest(cmd, http.MethodPost, path, operation, body, http.StatusAccepted)
	if err != nil {
		return err
	}
	var started struct {
		Joined      bool          `json:"joined"`
		Install     updateInstall `json:"install"`
		InstallPath string        `json:"install_path"`
	}
	if err := json.Unmarshal(responseBody, &started); err != nil || started.Install.Version == "" {
		return fmt.Errorf("%s: invalid response %q", operation, strings.TrimSpace(string(responseBody)))
	}
	version := started.Install.Version
	streamConnected = true
	if !jsonOutput {
		if started.Joined {
			fmt.Fprintf(stderr, "following the install of %s already running on instance %s\n", version, instance)
		} else {
			fmt.Fprintf(stderr, "%s %s to %s\n", verb, version, started.InstallPath)
		}
	}

	lastProgress := ""
	for {
		var frame eventFrame
		var open bool
		select {
		case <-ctx.Done():
			return detachFromInstall(stderr, version)
		case frame, open = <-stream.frames:
		}
		if !open {
			if ctx.Err() != nil {
				return detachFromInstall(stderr, version)
			}
			return fmt.Errorf("instance %s closed the event stream before the install of %s finished; run marasi update to see its state", instance, version)
		}
		if frame.name != "update.progress" && frame.name != "update.installed" && frame.name != "update.failed" {
			continue
		}
		var event struct {
			Version     string `json:"version"`
			Step        string `json:"step"`
			Received    int64  `json:"received"`
			Total       int64  `json:"total"`
			InstallPath string `json:"install_path"`
			Kind        string `json:"kind"`
			Message     string `json:"message"`
			Page        string `json:"page"`
		}
		if json.Unmarshal([]byte(frame.data), &event) != nil || event.Version != version {
			continue
		}
		if jsonOutput {
			if err := writeEvent(stdout, frame.name, frame.data, true); err != nil {
				return err
			}
		}
		switch frame.name {
		case "update.progress":
			if jsonOutput {
				continue
			}
			if line := describeStep(event.Step, event.Received, event.Total, true); line != lastProgress {
				lastProgress = line
				fmt.Fprintln(stderr, line)
			}
		case "update.installed":
			if !jsonOutput {
				fmt.Fprintf(stderr, "installed %s at %s; restart instances to run it with marasi service restart\n", version, event.InstallPath)
			}
			return nil
		case "update.failed":
			failure := fmt.Sprintf("%s %s failed at %s (%s): %s", verb, version, event.Step, event.Kind, event.Message)
			if event.Page != "" {
				failure += "; see " + event.Page
			}
			return errors.New(failure)
		}
	}
}

func detachFromInstall(stderr io.Writer, version string) error {
	if !jsonOutput {
		fmt.Fprintf(stderr, "stopped following; the install of %s continues on instance %s, run marasi update to see it\n", version, instance)
	}
	return nil
}
