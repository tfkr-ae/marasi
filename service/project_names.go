package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// projectSuffix ends every project file name.
const projectSuffix = ".marasi"

// projectsDirMode keeps the projects directory owner-only when startup
// creates it, since project files hold captured traffic and credentials.
const projectsDirMode = 0700

// namedOpenProjectsDirMode is the mode a named project open has always
// created a missing projects directory with.
const namedOpenProjectsDirMode = 0755

// ProjectsDir is the directory under configDir that holds named projects.
func ProjectsDir(configDir string) string {
	return filepath.Join(configDir, "projects")
}

// CreateProjectsDir creates configDir's projects directory if it is missing.
func CreateProjectsDir(configDir string) error {
	if err := os.MkdirAll(ProjectsDir(configDir), projectsDirMode); err != nil {
		return fmt.Errorf("creating projects dir: %w", err)
	}
	return nil
}

// ProjectListItem is one named project: its name, canonical path, and the
// project file's modification time in UTC.
type ProjectListItem struct {
	Name       string    `json:"name"`
	Project    string    `json:"project"`
	ModifiedAt time.Time `json:"modified_at"`
}

// ProjectList is the body of the project list: the CLI's --json output and
// the project list route's response.
type ProjectList struct {
	Items []ProjectListItem `json:"items"`
}

// ListProjects lists the named projects in configDir's projects directory,
// sorted by name. It follows symlinks, so two symlinks to one file are two
// items (docs/adr/2026-10-06-project-list-follows-symlinks.md), each with
// its target's modification time, and skips directories. A missing projects
// directory is an empty list.
func ListProjects(configDir string) ([]ProjectListItem, error) {
	projectsDir := ProjectsDir(configDir)
	entries, err := os.ReadDir(projectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []ProjectListItem{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading projects directory %s: %w", projectsDir, err)
	}

	items := make([]ProjectListItem, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), projectSuffix) {
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
		canonicalPath, err := ResolveProjectPath(path)
		if err != nil {
			return nil, fmt.Errorf("resolving project %s: %w", path, err)
		}
		items = append(items, ProjectListItem{
			Name:       strings.TrimSuffix(entry.Name(), projectSuffix),
			Project:    canonicalPath,
			ModifiedAt: info.ModTime().UTC(),
		})
	}
	slices.SortFunc(items, func(left, right ProjectListItem) int {
		return strings.Compare(left.Name, right.Name)
	})
	return items, nil
}

// CleanProjectName returns name without surrounding space and one .marasi
// suffix, or an error when it cannot name a file in the projects directory.
func CleanProjectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, projectSuffix)
	if name == "" {
		return "", errors.New("invalid project name: cannot be empty")
	}
	if strings.HasSuffix(name, projectSuffix) {
		return "", errors.New("invalid project name: only one .marasi suffix is allowed")
	}
	if !filepath.IsLocal(name) {
		return "", errors.New("invalid project name: absolute paths and parent directory references are not allowed")
	}
	if name == "." || strings.ContainsAny(name, `/\`) {
		return "", errors.New("invalid project name: path separators are not allowed")
	}
	if filepath.Base(name) != name {
		return "", errors.New("invalid project name: subdirectories not allowed")
	}
	return name, nil
}

// ResolveNamedProjectPath returns the canonical path of the named project in
// configDir's projects directory, creating that directory if it is missing.
func ResolveNamedProjectPath(configDir, name string) (string, error) {
	name, err := CleanProjectName(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(ProjectsDir(configDir), namedOpenProjectsDirMode); err != nil {
		return "", fmt.Errorf("creating projects dir: %w", err)
	}
	return ResolveProjectPath(filepath.Join(ProjectsDir(configDir), name+projectSuffix))
}
