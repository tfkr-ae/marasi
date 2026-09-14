package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

// executeCommand runs the root cobra command with args.
func executeCommand(args []string) error {
	rootCmd.SetArgs(args)
	jsonOutput = recognizedJSONMode(args)
	rootCmd.SilenceErrors = jsonOutput
	stdout := rootCmd.OutOrStdout()
	var output bytes.Buffer
	if jsonOutput {
		rootCmd.SetOut(&output)
	}
	err := rootCmd.Execute()
	if jsonOutput {
		rootCmd.SetOut(stdout)
	}
	rootCmd.SilenceErrors = false
	if err == nil {
		if jsonOutput {
			_, err = io.Copy(stdout, &output)
		}
		return err
	}
	if jsonOutput {
		encodeErr := json.NewEncoder(stdout).Encode(struct {
			Error string `json:"error"`
		}{Error: err.Error()})
		return errors.Join(err, encodeErr)
	}
	return err
}

// recognizedJSONMode reports whether args request --json before Cobra executes.
func recognizedJSONMode(args []string) bool {
	command, commandArgs, findErr := rootCmd.Find(args)
	requested := false
	for index := 0; index < len(commandArgs); index++ {
		arg := commandArgs[index]
		if arg == "--" {
			return requested
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if findErr != nil {
				return requested
			}
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			return requested
		}

		flagArg := strings.TrimPrefix(arg, "--")
		name, value, hasValue := flagArg, "", false
		if separator := strings.IndexByte(flagArg, '='); separator >= 0 {
			name, value, hasValue = flagArg[:separator], flagArg[separator+1:], true
		}
		flag := command.Flags().Lookup(name)
		if flag == nil {
			return requested
		}
		if name == "json" {
			if !hasValue {
				requested = true
			} else if parsed, err := strconv.ParseBool(value); err == nil {
				requested = parsed
			} else {
				return requested
			}
		}
		if !hasValue && flag.NoOptDefVal == "" {
			index++
		}
	}
	return requested
}

func init() {
	cobra.EnableTraverseRunHooks = true
	rootCmd.PersistentFlags().StringVar(&configDir, "config-dir", defaultConfigDir(), "Marasi config directory")
	rootCmd.PersistentFlags().StringVar(&instance, "instance", "default", "Instance name")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Print command output as JSON")
}

// prepareInstancePath resolves --config-dir and --instance into instancePath.
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

// resolveInstancePath returns the instance directory under configDir for name.
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

	resolvedPath := filepath.Join(configDir, "instances", name)
	socketPath := resolvedPath + ".sock"
	if len([]byte(socketPath))+1 > unixSocketPathLimit {
		return "", fmt.Errorf("instance socket path %q exceeds %d-byte limit", socketPath, unixSocketPathLimit)
	}

	return resolvedPath, nil
}

// defaultConfigDir returns the per-user Marasi config directory, or empty if it cannot be determined.
func defaultConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Marasi")
}
