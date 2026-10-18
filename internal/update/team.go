package update

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// TeamReader reads the Apple developer team that signed an executable. An
// unsigned executable has team "".
type TeamReader interface {
	TeamID(ctx context.Context, path string) (string, error)
}

// Codesign reads the team with the macOS codesign tool.
type Codesign struct {
	// Path is the codesign executable; "codesign" on PATH when empty.
	Path string
}

// TeamID runs `codesign -dv --verbose=2 path` and returns its
// TeamIdentifier. An unsigned executable, or one signed ad hoc, has "".
func (c Codesign) TeamID(ctx context.Context, path string) (string, error) {
	tool := c.Path
	if tool == "" {
		tool = "codesign"
	}
	out, err := exec.CommandContext(ctx, tool, "-dv", "--verbose=2", path).CombinedOutput()
	team := parseTeamIdentifier(out)
	if err != nil && team == "" {
		if bytes.Contains(out, []byte("not signed")) {
			return "", nil
		}
		return "", fmt.Errorf("reading the code signature of %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return team, nil
}

func parseTeamIdentifier(out []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if team, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "TeamIdentifier="); ok {
			if team == "not set" {
				return ""
			}
			return team
		}
	}
	return ""
}
