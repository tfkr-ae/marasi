package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// envAnnotation is the flag annotation that holds the environment variable
// bound to a flag by bindEnv.
const envAnnotation = "marasi_env"

// flagConflicts are the flag pairs that pick the same setting two ways. A
// passed flag ignores the variable of the other flag in its pair, and the two
// variables together, with neither flag passed, are a usage error.
var flagConflicts = [][2]string{
	{"project", "project-name"},
	{"remote", "instance"},
}

// boundVariables lists every variable bindEnv has bound, in binding order.
var boundVariables []string

// envSources maps each flag that applyEnv set from the environment to its
// variable, for messages that name where a value came from.
var envSources = map[string]string{}

// bindEnv lets variable set flag name of flags when the flag is not passed,
// and names the variable in the flag's help. The variable is read by applyEnv
// when a command runs, so a flag always wins over it.
func bindEnv(flags *pflag.FlagSet, name, variable string) {
	flag := flags.Lookup(name)
	flag.Usage += " (env " + variable + ")"
	if err := flags.SetAnnotation(name, envAnnotation, []string{variable}); err != nil {
		panic(err)
	}
	boundVariables = append(boundVariables, variable)
}

// applyEnv sets each bound flag of cmd that was not passed from its variable,
// when the variable is set and not empty. A set flag reads as passed from
// here on, and envSources records which variable set it.
func applyEnv(cmd *cobra.Command) error {
	clear(envSources)
	flags := cmd.Flags()
	values := map[string]string{}
	flags.VisitAll(func(flag *pflag.Flag) {
		variable := flag.Annotations[envAnnotation]
		if len(variable) != 1 || flag.Changed {
			return
		}
		if value := os.Getenv(variable[0]); value != "" {
			values[flag.Name] = value
		}
	})
	for _, pair := range flagConflicts {
		first, second := flags.Lookup(pair[0]), flags.Lookup(pair[1])
		if first == nil || second == nil {
			continue
		}
		switch {
		case first.Changed || second.Changed:
			delete(values, first.Name)
			delete(values, second.Name)
		case values[first.Name] != "" && values[second.Name] != "":
			return fmt.Errorf("%s and %s cannot be used together: unset one", envVariable(first), envVariable(second))
		}
	}
	var err error
	flags.VisitAll(func(flag *pflag.Flag) {
		value, ok := values[flag.Name]
		if !ok || err != nil {
			return
		}
		variable := envVariable(flag)
		if setErr := flags.Set(flag.Name, value); setErr != nil {
			err = fmt.Errorf("invalid %s %q: %w", variable, value, setErr)
			return
		}
		envSources[flag.Name] = variable
	})
	return err
}

// envVariable is the variable bound to flag.
func envVariable(flag *pflag.Flag) string {
	return flag.Annotations[envAnnotation][0]
}

// flagSource names where a passed flag's value came from: its variable when
// applyEnv set it, and the flag itself otherwise.
func flagSource(name string) string {
	if variable, ok := envSources[name]; ok {
		return variable
	}
	return "--" + name
}

// withoutBoundVariables returns environ without the variables bound to flags. A service child gets its settings as flags, and a variable
// left in its environment would bring back a setting its parent dropped, such
// as a control address stopped before a restart.
func withoutBoundVariables(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, variable := range environ {
		name, _, _ := strings.Cut(variable, "=")
		if !slices.Contains(boundVariables, name) {
			kept = append(kept, variable)
		}
	}
	return kept
}
