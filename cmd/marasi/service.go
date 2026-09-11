package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

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
	defer repo.Close()

	err = proxy.WithOptions(
		marasi.WithWordlistManager(wordlists),
		marasi.WithDefaultRepositories(repo),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
	)
	if err != nil {
		return fmt.Errorf("starting proxy base options: %w", err)
	}

	<-ctx.Done()
	return nil
}
