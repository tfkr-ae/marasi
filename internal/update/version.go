package update

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Channel is the stream of builds an installed build follows (GLOSSARY
// "Channel").
type Channel string

const (
	// Release follows builds tagged DD.MM.YYYY[-N].
	Release Channel = "release"
	// Nightly follows the single published nightly.
	Nightly Channel = "nightly"
)

// ParseChannel parses "release" or "nightly".
func ParseChannel(s string) (Channel, error) {
	switch c := Channel(s); c {
	case Release, Nightly:
		return c, nil
	}
	return "", fmt.Errorf("channel %q is not release or nightly", s)
}

// ErrDevBuild reports a development build: its version is "dev" or ends in
// "(dev)", or it embeds no signing key. A development build never checks for
// or installs updates.
var ErrDevBuild = errors.New("development builds don't check for or install updates")

// dateLayout is the DD.MM.YYYY date that names a build (ADR-0009).
const dateLayout = "02.01.2006"

// Build is a parsed version string (ADR-0009):
//
//   - release: "DD.MM.YYYY" or "DD.MM.YYYY-N" for the Nth cut of a day
//   - nightly: "DD.MM.YYYY (nightly <shortsha>)"
type Build struct {
	// Version is the version string as given.
	Version string
	// Date is the UTC day the build is named after.
	Date time.Time
	// Suffix is the release's cut of the day: 1 for the plain tag, N for
	// "-N". A nightly has 1.
	Suffix int
	// Nightly is set for a nightly build.
	Nightly bool
	// Commit is a nightly's commit as its version string has it.
	Commit string
	// Published is when GitHub published the build; zero when unknown.
	// Only the nightly ordering reads it.
	Published time.Time
}

// ParseBuild parses a version string. It returns ErrDevBuild for a
// development build.
func ParseBuild(version string) (Build, error) {
	if version == "" || version == "dev" || strings.HasSuffix(version, "(dev)") {
		return Build{}, ErrDevBuild
	}
	malformed := fmt.Errorf("version %q is not DD.MM.YYYY, DD.MM.YYYY-N or DD.MM.YYYY (nightly <commit>)", version)
	build := Build{Version: version, Suffix: 1}
	datePart := version
	if before, rest, ok := strings.Cut(version, " (nightly "); ok {
		commit, ok := strings.CutSuffix(rest, ")")
		if !ok || !isCommitHash(commit) {
			return Build{}, malformed
		}
		datePart, build.Nightly, build.Commit = before, true, commit
	} else if before, suffix, ok := strings.Cut(version, "-"); ok {
		n, err := strconv.Atoi(suffix)
		if err != nil || n < 1 || strings.HasPrefix(suffix, "+") {
			return Build{}, malformed
		}
		datePart, build.Suffix = before, n
	}
	if len(datePart) != len(dateLayout) {
		return Build{}, malformed
	}
	date, err := time.Parse(dateLayout, datePart)
	if err != nil {
		return Build{}, malformed
	}
	build.Date = date
	return build, nil
}

// Compare orders builds by date, then release suffix. A release and a
// nightly of the same date compare equal, as do two nightlies of the same
// date. It returns -1, 0 or 1.
func Compare(a, b Build) int {
	if c := a.Date.Compare(b.Date); c != 0 {
		return c
	}
	switch {
	case a.Nightly || b.Nightly:
		return 0
	case a.Suffix < b.Suffix:
		return -1
	case a.Suffix > b.Suffix:
		return 1
	}
	return 0
}

// IsUpdate reports whether candidate, found on channel, is an update for
// installed: newer by Compare, never older (ADR-0049). On the nightly
// channel a nightly of the same date as an installed nightly is also newer
// when it is from another commit and was published later. When the
// installed build's publish time is unknown, another commit is enough: only
// one nightly is published at a time and each replaces the last.
func IsUpdate(channel Channel, installed, candidate Build) bool {
	if c := Compare(candidate, installed); c != 0 {
		return c > 0
	}
	if channel != Nightly || !candidate.Nightly || !installed.Nightly {
		return false
	}
	if sameCommit(candidate.Commit, installed.Commit) {
		return false
	}
	return installed.Published.IsZero() || candidate.Published.After(installed.Published)
}

// isCommitHash reports whether s looks like a full or shortened git hash.
func isCommitHash(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// sameCommit reports whether two hashes, either possibly shortened, name the
// same commit.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// SameCommit reports whether two commit hashes, either possibly shortened,
// name the same commit. An empty hash matches nothing.
func SameCommit(a, b string) bool {
	return a != "" && b != "" && sameCommit(a, b)
}
