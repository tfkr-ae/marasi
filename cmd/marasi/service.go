package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/wordlist"
)

var projectName string

func init() {
	startCmd.Flags().StringVar(&projectName, "project", "scratchpad", "Project name")
	serviceCmd.AddCommand(startCmd)
	rootCmd.AddCommand(serviceCmd)
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage marasi services",
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a marasi instance",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		return startService(ctx, configDir, projectName)
	},
}

func startService(ctx context.Context, configDir, projectName string) error {
	if configDir == "" {
		return fmt.Errorf("config dir is empty")
	}

	path, err := projectPath(configDir, projectName)
	if err != nil {
		return err
	}

	proxy, err := marasi.New(marasi.WithConfigDir(configDir))
	if err != nil {
		return fmt.Errorf("starting proxy with config dir: %w", err)
	}

	wordlists, err := wordlist.NewManager(configDir)
	if err != nil {
		return fmt.Errorf("creating wordlist manager : %w", err)
	}

	unlock, err := lockProject(path)
	if err != nil {
		return fmt.Errorf("locking project: %w", err)
	}
	defer unlock()

	dbConn, err := db.New(path, proxy.Logger)
	if err != nil {
		return fmt.Errorf("opening project: %w", err)
	}
	repo := db.NewProxyRepo(dbConn)

	err = proxy.WithOptions(
		marasi.WithWordlistManager(wordlists),
		marasi.WithDefaultRepositories(repo),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
	)
	if err != nil {
		repo.Close()
		return fmt.Errorf("starting proxy base options: %w", err)
	}

	<-ctx.Done()
	proxy.Close()
	return nil
}

func projectPath(configDir, name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".marasi")
	if name == "" {
		return "", errors.New("invalid project name: cannot be empty")
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

	return filepath.Join(configDir, "projects", name+".marasi"), nil
}

func lockProject(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating projects dir: %w", err)
	}

	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening project lock %s: %w", lockPath, err)
	}

	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("project already open: %w", err)
	}

	return func() error {
		unlockErr := unlockFile(f)
		closeErr := f.Close()
		if unlockErr != nil {
			return fmt.Errorf("unlocking project: %w", unlockErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing project lock: %w", closeErr)
		}
		return nil
	}, nil
}
