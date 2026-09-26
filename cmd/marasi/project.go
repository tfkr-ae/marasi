package main

import (
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

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var projectOpenPath string
var projectOpenName string

func init() {
	projectOpenCmd.Flags().StringVar(&projectOpenPath, "path", "", "Project path")
	projectOpenCmd.Flags().StringVar(&projectOpenName, "name", "", "Project name under the default projects directory")
	projectOpenCmd.MarkFlagsMutuallyExclusive("path", "name")
	projectOpenCmd.MarkFlagsOneRequired("path", "name")
	projectCmd.AddCommand(projectListCmd, projectOpenCmd)
	rootCmd.AddCommand(projectCmd)
}

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage projects",
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
		for _, item := range items {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", item.Name, item.Project); err != nil {
				return fmt.Errorf("writing project list: %w", err)
			}
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
		return openProject(ctx, instancePath, instance, jsonOutput, path, cmd.OutOrStdout())
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

func openProject(ctx context.Context, instancePath, instanceName string, asJSON bool, path string, stdout io.Writer) error {
	body, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	if err != nil {
		return fmt.Errorf("encoding project open request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://marasi/project/open", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating project open request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("instance %s is not running", instanceName)
	}

	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(
			wrapError("reading project open response", readErr),
			wrapError("closing project open response", closeErr),
		)
	}
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
