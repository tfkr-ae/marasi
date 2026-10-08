package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
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
	eventsConnected = false
	jsonOutput = recognizedJSONMode(args)
	command, _, _ := rootCmd.Find(args)
	streamingJSON := jsonOutput && command == eventsCmd
	rootCmd.SilenceErrors = jsonOutput
	stdout := rootCmd.OutOrStdout()
	var output bytes.Buffer
	if jsonOutput && !streamingJSON {
		rootCmd.SetOut(&output)
	}
	err := rootCmd.Execute()
	if jsonOutput && !streamingJSON {
		rootCmd.SetOut(stdout)
	}
	rootCmd.SilenceErrors = false
	if err == nil {
		if jsonOutput && !streamingJSON {
			_, err = io.Copy(stdout, &output)
		}
		return err
	}
	if jsonOutput {
		if streamingJSON && eventsConnected {
			return err
		}
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
			// A shorthand flag such as -q value or -qvalue.
			flag := command.Flags().ShorthandLookup(arg[1:2])
			if flag == nil {
				return requested
			}
			if len(arg) == 2 && flag.NoOptDefVal == "" {
				index++
			}
			continue
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
	rootCmd.PersistentFlags().StringVar(&instance, "instance", "default", "Service instance name")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Print command output as JSON")
}

// showSubcommandHelp prints command help when a group command is run with no subcommand.
// With --json, it returns an error that names the subcommands.
func showSubcommandHelp(cmd *cobra.Command, _ []string) error {
	err := fmt.Errorf("%s: choose %s", cmd.CommandPath(), subcommandChoice(cmd))
	if jsonOutput {
		return err
	}
	cmd.SilenceErrors = true
	if helpErr := cmd.Help(); helpErr != nil {
		return helpErr
	}
	return err
}

// subcommandChoice lists available subcommands in help order.
func subcommandChoice(cmd *cobra.Command) string {
	var names []string
	for _, child := range cmd.Commands() {
		if child.IsAvailableCommand() {
			names = append(names, child.Name())
		}
	}
	switch len(names) {
	case 0:
		return "a subcommand"
	case 1:
		return names[0]
	case 2:
		return names[0] + " or " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	}
}

// prepareInstancePath resolves --config-dir and --instance into instancePath.
func prepareInstancePath(cmd *cobra.Command, _ []string) error {
	if configDir == "" {
		return fmt.Errorf("config dir is empty")
	}
	if cmd == listServiceInstancesCmd || cmd == projectListCmd {
		return nil
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

// parseServiceID checks that raw is a UUID and returns its canonical form.
func parseServiceID(raw string) (string, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid uuid %q", raw)
	}
	return id.String(), nil
}

// serviceIDPath inserts a parsed UUID between prefix and suffix.
func serviceIDPath(prefix, raw, suffix string) (string, error) {
	id, err := parseServiceID(raw)
	if err != nil {
		return "", err
	}
	return prefix + id + suffix, nil
}

// parsePageLimit checks that raw is a page size the service accepts.
func parsePageLimit(raw string) (string, error) {
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 || parsed > 500 {
		return "", fmt.Errorf("invalid limit %q", raw)
	}
	return strconv.Itoa(parsed), nil
}

// setCursorQuery sets cursor when raw is non-empty, after parsing it as a UUID.
func setCursorQuery(query url.Values, raw string) error {
	if raw == "" {
		return nil
	}
	id, err := parseServiceID(raw)
	if err != nil {
		return err
	}
	query.Set("cursor", id)
	return nil
}

// defaultConfigDir returns the per-user Marasi config directory, or empty if it cannot be determined.
func defaultConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Marasi")
}
