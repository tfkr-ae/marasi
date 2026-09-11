package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/wordlist"
)

func init() {
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

		return startService(ctx, configDir)
	},
}

func startService(ctx context.Context, configDir string) error {
	if configDir == "" {
		return fmt.Errorf("config dir is empty")
	}

	proxy, err := marasi.New(marasi.WithConfigDir(configDir))
	if err != nil {
		return fmt.Errorf("starting proxy with config dir: %w", err)
	}

	wordlists, err := wordlist.NewManager(configDir)
	if err != nil {
		return fmt.Errorf("creating wordlist manager : %w", err)
	}

	err = proxy.WithOptions(
		marasi.WithWordlistManager(wordlists),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
	)
	if err != nil {
		return fmt.Errorf("starting proxy base options: %w", err)
	}

	<-ctx.Done()
	return nil
}
