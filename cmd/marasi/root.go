package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var configDir string
var instance string
var instancePath string
var jsonOutput bool

const unixSocketPathLimit = 104

var rootCmd = &cobra.Command{
	Use:               "marasi",
	Short:             "Marasi proxy service",
	SilenceUsage:      true,
	PersistentPreRunE: prepareInstancePath,
}

func init() {
	cobra.EnableTraverseRunHooks = true
	rootCmd.PersistentFlags().StringVar(&configDir, "config-dir", defaultConfigDir(), "Marasi config directory")
	rootCmd.PersistentFlags().StringVar(&instance, "instance", "default", "Instance name")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Print command output as JSON")
}

func prepareInstancePath(*cobra.Command, []string) error {
	if configDir == "" {
		return fmt.Errorf("config dir is empty")
	}

	path, err := resolveInstancePath(configDir, instance)
	if err != nil {
		return err
	}
	instancePath = path
	return nil
}

func resolveInstancePath(configDir, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("invalid instance name: cannot be empty")
	}
	if name == "." || name == ".." || !filepath.IsLocal(name) {
		return "", errors.New("invalid instance name: paths and parent directory references are not allowed")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("invalid instance name: path separators are not allowed")
	}
	if strings.HasSuffix(name, ".sock") {
		return "", errors.New("invalid instance name: .sock suffix is not allowed")
	}

	instancePath := filepath.Join(configDir, "instances", name)
	socketPath := instancePath + ".sock"
	if len([]byte(socketPath))+1 > unixSocketPathLimit {
		return "", fmt.Errorf("instance socket path %q exceeds %d-byte limit", socketPath, unixSocketPathLimit)
	}

	return instancePath, nil
}

func defaultConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Marasi")
}
