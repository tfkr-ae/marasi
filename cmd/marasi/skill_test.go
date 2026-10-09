package main

import (
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/skills"
)

func TestSkillCommandPrintsSkill(t *testing.T) {
	binary := buildMarasi(t)

	// The skill needs no instance, so an unusable instance name must not stop it.
	stdout, stderr, err := runMarasi(binary, "--config-dir", t.TempDir(), "--instance", "../bad", "skill")
	if err != nil || stderr != "" || stdout != skills.Marasi {
		t.Fatalf("\nwanted:\nthe embedded skill\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	if !strings.HasPrefix(stdout, "---\nname: marasi\n") {
		t.Fatalf("\nwanted:\nskill frontmatter\ngot:\n%q", stdout[:min(len(stdout), 40)])
	}

	stdout, stderr, err = runMarasi(binary, "skill", "--json")
	assertJSONCommandError(t, stdout, stderr, err, "skill does not support --json")
}

// TestSkillCommandsExist checks every marasi command line in the skill's
// shell examples against the CLI, so the skill cannot drift from it.
func TestSkillCommandsExist(t *testing.T) {
	lines := skillCommandLines(skills.Marasi)
	if len(lines) == 0 {
		t.Fatal("\nwanted:\nmarasi commands in the skill\ngot:\nnone")
	}
	for _, line := range lines {
		args := strings.Fields(line)[1:]
		command, _, err := rootCmd.Find(args)
		if err != nil || command == rootCmd {
			t.Errorf("\nwanted:\na marasi command\ngot:\n%q: %v", line, err)
			continue
		}
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") || arg == "-" {
				continue
			}
			var flag string
			if name, ok := strings.CutPrefix(arg, "--"); ok {
				name, _, _ = strings.Cut(name, "=")
				if command.Flag(name) != nil {
					continue
				}
				flag = "--" + name
			} else {
				if command.Flags().ShorthandLookup(arg[1:2]) != nil || command.InheritedFlags().ShorthandLookup(arg[1:2]) != nil {
					continue
				}
				flag = arg[:2]
			}
			t.Errorf("\nwanted:\n%s to accept %s\ngot:\nunknown flag in %q", command.CommandPath(), flag, line)
		}
	}
}

// skillCommandLines returns the marasi command lines in skill's sh blocks,
// with backslash continuations joined.
func skillCommandLines(skill string) []string {
	var lines []string
	inBlock := false
	pending := ""
	for _, line := range strings.Split(skill, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "```sh":
			inBlock = true
			continue
		case strings.HasPrefix(trimmed, "```"):
			inBlock = false
			continue
		case !inBlock:
			continue
		}
		pending += " " + strings.TrimSuffix(trimmed, `\`)
		if strings.HasSuffix(trimmed, `\`) {
			continue
		}
		if command := strings.TrimSpace(pending); strings.HasPrefix(command, "marasi ") {
			lines = append(lines, command)
		}
		pending = ""
	}
	return lines
}
