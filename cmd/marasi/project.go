package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var projectOpenPath string
var projectOpenName string

func init() {
	projectOpenCmd.Flags().StringVar(&projectOpenPath, "path", "", "Project file path, such as /path/project.marasi. Set this or --name, not both")
	projectOpenCmd.Flags().StringVar(&projectOpenName, "name", "", "Project name under the default projects directory. Set this or --path, not both")
	projectOpenCmd.MarkFlagsMutuallyExclusive("path", "name")
	projectOpenCmd.MarkFlagsOneRequired("path", "name")
	projectCmd.AddCommand(projectListCmd, projectOpenCmd)
	rootCmd.AddCommand(projectCmd)
}

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "List projects and open one in an instance",
}

type projectListItem struct {
	Name    string `json:"name"`
	Project string `json:"project"`
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List named projects",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := listProjects(configDir)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Items []projectListItem `json:"items"`
			}{Items: items})
		}
		table := newListTable(cmd.OutOrStdout(), "NAME", "PROJECT")
		for _, item := range items {
			table.row(item.Name, item.Project)
		}
		if err := table.flush(); err != nil {
			return fmt.Errorf("writing project list: %w", err)
		}
		return nil
	},
}

func listProjects(configDir string) ([]projectListItem, error) {
	projectsDir := filepath.Join(configDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []projectListItem{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading projects directory %s: %w", projectsDir, err)
	}

	items := make([]projectListItem, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".marasi") {
			continue
		}
		path := filepath.Join(projectsDir, entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("checking project %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		canonicalPath, err := service.ResolveProjectPath(path)
		if err != nil {
			return nil, fmt.Errorf("resolving project %s: %w", path, err)
		}
		items = append(items, projectListItem{
			Name:    strings.TrimSuffix(entry.Name(), ".marasi"),
			Project: canonicalPath,
		})
	}
	slices.SortFunc(items, func(left, right projectListItem) int {
		return strings.Compare(left.Name, right.Name)
	})
	return items, nil
}

var projectOpenCmd = &cobra.Command{
	Use:     "open",
	Aliases: []string{"switch"},
	Short:   "Open a project in the selected service instance",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		path, err := resolveProjectOpenPath(cmd)
		if err != nil {
			return err
		}
		return openProject(ctx, instancePath, instance, jsonOutput, path, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func resolveProjectOpenPath(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("path") {
		path, err := resolveProjectPath(projectOpenPath)
		if err != nil {
			return "", fmt.Errorf("%s: %w", projectOpenPath, err)
		}
		return path, nil
	}
	path, err := resolveNamedProjectPath(configDir, projectOpenName)
	if err != nil {
		return "", fmt.Errorf("%s: %w", projectOpenName, err)
	}
	return path, nil
}

func openProject(ctx context.Context, instancePath, instanceName string, asJSON bool, path string, stdout, stderr io.Writer) error {
	// JSON mode writes nothing to stderr (docs/adr/2026-10-06-cli-output-contract.md), so only a human sees waits.
	if !asJSON {
		stop := watchProjectWaits(ctx, instancePath, path, stderr)
		defer stop()
	}
	body, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	if err != nil {
		return fmt.Errorf("encoding project open request: %w", err)
	}
	response, err := dialInstance(ctx, instancePath+".sock", instanceName, http.MethodPost, "/project/open", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	responseBody := response.Body
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return controlAPIError("opening project", response.Status, responseBody)
		}
		return fmt.Errorf("opening project: %s", response.Status)
	}
	if asJSON {
		if _, err := stdout.Write(responseBody); err != nil {
			return fmt.Errorf("writing project open response: %w", err)
		}
		return nil
	}
	return writeProjectOpenHuman(responseBody, stdout)
}

func writeProjectOpenHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding project open response: %w", err)
	}
	if response.Project == "" {
		return errors.New("invalid project open response: project must be a non-empty string")
	}
	if _, err := fmt.Fprintf(stdout, "project: %s\n", response.Project); err != nil {
		return fmt.Errorf("writing project open result: %w", err)
	}
	return nil
}

// projectWaitConnectTimeout bounds how long project open waits for its
// events subscription before it opens without reporting waits.
const projectWaitConnectTimeout = 2 * time.Second

// watchProjectWaits prints each project.waiting event for target to stderr
// until the returned stop is called. It returns once the subscription is
// connected, or has failed, so no wait is missed. A failed subscription is
// not an error: the open itself reports an instance that is not running.
func watchProjectWaits(ctx context.Context, instancePath, target string, stderr io.Writer) func() {
	ctx, cancel := context.WithCancel(ctx)
	client := service.NewClient(instancePath + ".sock")
	connected, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		defer client.Close()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/events", nil)
		if err != nil {
			return
		}
		response, err := client.Do(request)
		if err != nil {
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return
		}
		_ = readEventFrames(bufio.NewReader(response.Body), func(line string) error {
			if line == ": connected" {
				close(connected)
			}
			return nil
		}, func(frame eventFrame) error {
			if frame.name == "project.waiting" {
				writeProjectWaits(stderr, target, frame.data)
			}
			return nil
		})
	}()
	timer := time.NewTimer(projectWaitConnectTimeout)
	defer timer.Stop()
	select {
	case <-connected:
	case <-finished:
	case <-timer.C:
	}
	return func() {
		cancel()
		<-finished
	}
}

// writeProjectWaits prints one line per wait in a project.waiting event for target.
func writeProjectWaits(stderr io.Writer, target, data string) {
	var event struct {
		Project    string                `json:"project"`
		WaitingFor []service.ProjectWait `json:"waiting_for"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil || event.Project != target {
		return
	}
	for _, wait := range event.WaitingFor {
		switch wait.Kind {
		case "checkpoint":
			line := fmt.Sprintf("waiting for Checkpoint %s %s", wait.Type, wait.ID)
			if wait.Summary != "" {
				line += ": " + wait.Summary
			}
			fmt.Fprintln(stderr, line)
		case "extension":
			fmt.Fprintf(stderr, "waiting for extension %s\n", wait.Name)
		case "work":
			noun := "requests or control calls"
			if wait.Count == 1 {
				noun = "request or control call"
			}
			fmt.Fprintf(stderr, "waiting for %d %s in project work\n", wait.Count, noun)
		}
	}
}
