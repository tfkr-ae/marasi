package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// textInput names where a command reads one piece of text from: an optional
// trailing argument, the --file flag, or piped stdin.
type textInput struct {
	// what names the text in errors, such as "note" or "value".
	what string
	// file is the --file flag's value.
	file string
	// stdinTaken is why stdin is not a source, such as "--ids-file is -", or
	// empty when piped stdin may supply the text.
	stdinTaken string
}

// read returns the text from exactly one of text (the trailing argument, if
// any), --file, or piped stdin. It rejects none, more than one, and empty
// text. Stdin counts as piped when it is not a character device.
func (input textInput) read(cmd *cobra.Command, text []string) (string, error) {
	fileSet := cmd.Flags().Changed("file")
	stdinPiped := false
	if input.stdinTaken == "" {
		var err error
		if stdinPiped, err = stdinIsPiped(cmd); err != nil {
			return "", err
		}
	}
	sources := len(text)
	for _, set := range []bool{fileSet, stdinPiped} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		name := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
		if input.stdinTaken != "" {
			return "", fmt.Errorf("%s requires exactly one of text or --file when %s", name, input.stdinTaken)
		}
		return "", fmt.Errorf("%s requires exactly one of text, --file, or piped stdin", name)
	}
	var value string
	switch {
	case len(text) == 1:
		value = text[0]
	case fileSet:
		raw, err := os.ReadFile(input.file)
		if err != nil {
			return "", fmt.Errorf("reading %s file: %w", input.what, err)
		}
		value = string(raw)
	default:
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("reading %s from stdin: %w", input.what, err)
		}
		value = string(raw)
	}
	if value == "" {
		return "", fmt.Errorf("the %s is empty", input.what)
	}
	return value, nil
}

// stdinIsPiped reports whether stdin carries input: anything that is not a
// character device, such as a pipe or a file.
func stdinIsPiped(cmd *cobra.Command) (bool, error) {
	file, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return true, nil
	}
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("checking stdin: %w", err)
	}
	return info.Mode()&os.ModeCharDevice == 0, nil
}
