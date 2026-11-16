package h2client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// H2spec is the h2spec command the harness installs with go install, so it
// never enters go.mod: github.com/summerwind/h2spec v2.6.0 (MIT), commit
// 70ac2294010887f48b18e2d64f5cccd48421fad1. The tag predates the module's
// major-version path, so Go names it by its pseudo-version.
const H2spec = "github.com/summerwind/h2spec/cmd/h2spec@v1.5.1-0.20200804131034-70ac22940108"

// installH2spec builds H2spec into dir and returns the binary's path. Go's
// build cache makes it quick after the first time. Running the binary once
// per case is cheaper than a go run per case, which links every time.
func installH2spec(ctx context.Context, dir string, log io.Writer) (string, error) {
	bin, err := filepath.Abs(filepath.Join(dir, "bin"))
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "go", "install", H2spec)
	cmd.Env = append(os.Environ(), "GOBIN="+bin, "GOFLAGS=")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("installing %s: %w", H2spec, err)
	}
	return filepath.Join(bin, "h2spec"), nil
}

// Test is one h2spec case.
type Test struct {
	Group string // h2spec's group ID, its spec key and section: "http2/6.9.1"
	Seq   int    // the case's place in its group, from 1
	Desc  string
}

// ID is h2spec's own ID for the case, which selects it on h2spec's
// command line: "http2/6.9.1/3".
func (t Test) ID() string { return t.Group + "/" + strconv.Itoa(t.Seq) }

// Name is the case's name in a case ID: its h2spec ID with dashes for
// slashes, since a case pattern's * does not match a slash.
func (t Test) Name() string { return strings.ReplaceAll(t.ID(), "/", "-") }

// roots are the titles of h2spec's three specs in its dry run, by key.
var roots = map[string]string{
	"Generic tests for HTTP/2 server":                "generic",
	"Hypertext Transfer Protocol Version 2 (HTTP/2)": "http2",
	"HPACK: Header Compression for HTTP/2":           "hpack",
}

var (
	groupLine = regexp.MustCompile(`^\s+(\d+(?:\.\d+)*)\. `)
	testLine  = regexp.MustCompile(`^\s+(\d+): (.*)$`)
)

// ParseDryRun lists the cases in the output of h2spec --dryrun, in the
// order h2spec runs them. Strict cases are not in it: the harness runs
// h2spec without --strict.
func ParseDryRun(output []byte) ([]Test, error) {
	var tests []Test
	var key, group string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \r")
		if k, ok := roots[line]; ok {
			key, group = k, ""
			continue
		}
		if m := testLine.FindStringSubmatch(line); m != nil {
			if group == "" {
				return nil, fmt.Errorf("h2spec's dry run lists case %q outside a group", line)
			}
			seq, _ := strconv.Atoi(m[1])
			tests = append(tests, Test{Group: group, Seq: seq, Desc: m[2]})
			continue
		}
		if m := groupLine.FindStringSubmatch(line); m != nil {
			if key == "" {
				return nil, fmt.Errorf("h2spec's dry run lists group %q outside a spec", line)
			}
			group = key + "/" + m[1]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(tests) == 0 {
		return nil, errors.New("h2spec's dry run lists no cases")
	}
	return tests, nil
}

// Verdict is h2spec's verdict on a case.
type Verdict string

// Verdicts. h2spec writes every failed case as a JUnit error, never a
// failure; both read as Failed.
const (
	Passed  Verdict = "passed"
	Failed  Verdict = "failed"
	Skipped Verdict = "skipped"
	// NotRun: h2spec could not start the case, so it wrote no report. It
	// stops at the first such case, for example when the server does not
	// agree to h2 by ALPN.
	NotRun Verdict = "not-run"
)

// Result is one case of an h2spec JUnit report.
type Result struct {
	Group   string // the case's group ID
	Desc    string
	Verdict Verdict
}

type junitReport struct {
	XMLName xml.Name `xml:"testsuites"`
	Suites  []struct {
		Cases []struct {
			Package   string    `xml:"package,attr"`
			ClassName string    `xml:"classname,attr"`
			Failure   *struct{} `xml:"failure"`
			Error     *struct{} `xml:"error"`
			Skipped   *struct{} `xml:"skipped"`
		} `xml:"testcase"`
	} `xml:"testsuite"`
}

// LoadJUnit reads h2spec's JUnit report (-j) and lists the cases it ran in
// order. The JUnit report names a case by its group and description, not
// its place in the group.
func LoadJUnit(file string) ([]Result, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var report junitReport
	if err := xml.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", file, err)
	}
	var results []Result
	for _, suite := range report.Suites {
		for _, c := range suite.Cases {
			result := Result{Group: c.Package, Desc: c.ClassName, Verdict: Passed}
			switch {
			case c.Skipped != nil:
				result.Verdict = Skipped
			case c.Failure != nil || c.Error != nil:
				result.Verdict = Failed
			}
			results = append(results, result)
		}
	}
	return results, nil
}
