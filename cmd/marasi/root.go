package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var configDir string
var instance string

var rootCmd = &cobra.Command{
	Use:          "marasi",
	Short:        "Marasi proxy service",
	SilenceUsage: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configDir, "config-dir", defaultConfigDir(), "Marasi config directory")
	rootCmd.PersistentFlags().StringVar(&instance, "instance", "default", "Instance name")
}

func defaultConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Marasi")
}
