package update

import (
	"errors"
	"testing"
	"time"
)

func mustBuild(t *testing.T, version string) Build {
	t.Helper()
	build, err := ParseBuild(version)
	if err != nil {
		t.Fatalf("ParseBuild(%q): %v", version, err)
	}
	return build
}

func TestParseBuild(t *testing.T) {
	cases := []struct {
		version string
		want    Build
	}{
		{"23.06.2026", Build{Version: "23.06.2026", Date: time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC), Suffix: 1}},
		{"23.06.2026-2", Build{Version: "23.06.2026-2", Date: time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC), Suffix: 2}},
		{"18.10.2026 (nightly 4a99c5d)", Build{Version: "18.10.2026 (nightly 4a99c5d)", Date: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), Suffix: 1, Nightly: true, Commit: "4a99c5d"}},
	}
	for _, c := range cases {
		got := mustBuild(t, c.version)
		if got != c.want {
			t.Errorf("ParseBuild(%q) = %+v, want %+v", c.version, got, c.want)
		}
	}
}

func TestParseBuildRejectsDevAndMalformed(t *testing.T) {
	for _, version := range []string{"dev", "18.10.2026 (dev)", ""} {
		if _, err := ParseBuild(version); !errors.Is(err, ErrDevBuild) {
			t.Errorf("ParseBuild(%q) error = %v, want ErrDevBuild", version, err)
		}
	}
	for _, version := range []string{"v1.0.0", "2026.10.18", "18.10.2026-", "18.10.2026-0", "18.10.2026-x", "18.10.2026 (nightly zz)", "32.01.2026"} {
		if _, err := ParseBuild(version); err == nil || errors.Is(err, ErrDevBuild) {
			t.Errorf("ParseBuild(%q) error = %v, want a malformed-version error", version, err)
		}
	}
}

func TestCompareOrdersByDateThenSuffix(t *testing.T) {
	ordered := []string{"30.12.2025", "01.01.2026", "01.01.2026-2", "01.01.2026-10", "02.01.2026"}
	for i := 0; i < len(ordered)-1; i++ {
		older, newer := mustBuild(t, ordered[i]), mustBuild(t, ordered[i+1])
		if Compare(older, newer) >= 0 || Compare(newer, older) <= 0 {
			t.Errorf("want %s < %s", ordered[i], ordered[i+1])
		}
	}
}

func TestCompareSameDateReleaseAndNightlyAreEqual(t *testing.T) {
	release := mustBuild(t, "18.10.2026")
	nightly := mustBuild(t, "18.10.2026 (nightly 4a99c5d)")
	if Compare(release, nightly) != 0 || Compare(nightly, release) != 0 {
		t.Fatal("a release and a nightly of the same date must compare equal")
	}
}

func TestIsUpdateSameDateReleaseVsNightly(t *testing.T) {
	release := mustBuild(t, "18.10.2026")
	nightly := mustBuild(t, "18.10.2026 (nightly 4a99c5d)")
	nightly.Published = time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)

	if IsUpdate(Nightly, release, nightly) {
		t.Error("on nightly, a same-date nightly is not an update for a release")
	}
	if IsUpdate(Release, nightly, release) {
		t.Error("on release, a same-date release is not an update for a nightly")
	}
	later := mustBuild(t, "19.10.2026")
	if !IsUpdate(Release, nightly, later) {
		t.Error("on release, a release dated after the nightly is an update")
	}
	earlier := mustBuild(t, "17.10.2026-3")
	if IsUpdate(Release, nightly, earlier) {
		t.Error("switching to release never offers an older release")
	}
}

func TestIsUpdateNightlyOrdering(t *testing.T) {
	installed := mustBuild(t, "18.10.2026 (nightly aaaaaaa)")
	installed.Published = time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)

	sameCommit := mustBuild(t, "18.10.2026 (nightly aaaaaaa)")
	sameCommit.Published = installed.Published.Add(time.Hour)
	if IsUpdate(Nightly, installed, sameCommit) {
		t.Error("the same commit is never an update")
	}

	rebuilt := mustBuild(t, "18.10.2026 (nightly bbbbbbb)")
	rebuilt.Published = installed.Published.Add(time.Hour)
	if !IsUpdate(Nightly, installed, rebuilt) {
		t.Error("same date, another commit, published later is an update")
	}

	publishedEarlier := rebuilt
	publishedEarlier.Published = installed.Published.Add(-time.Hour)
	if IsUpdate(Nightly, installed, publishedEarlier) {
		t.Error("same date, another commit, published earlier is not an update")
	}

	unknown := installed
	unknown.Published = time.Time{}
	if !IsUpdate(Nightly, unknown, rebuilt) {
		t.Error("with the installed publish time unknown, another same-date commit is an update")
	}

	tomorrow := mustBuild(t, "19.10.2026 (nightly ccccccc)")
	if !IsUpdate(Nightly, installed, tomorrow) {
		t.Error("a later-dated nightly is an update")
	}
	yesterday := mustBuild(t, "17.10.2026 (nightly ddddddd)")
	yesterday.Published = installed.Published.Add(time.Hour)
	if IsUpdate(Nightly, installed, yesterday) {
		t.Error("an earlier-dated nightly is never an update")
	}
}

func TestParseChannel(t *testing.T) {
	for _, s := range []string{"release", "nightly"} {
		if c, err := ParseChannel(s); err != nil || string(c) != s {
			t.Errorf("ParseChannel(%q) = %q, %v", s, c, err)
		}
	}
	if _, err := ParseChannel("beta"); err == nil {
		t.Error("ParseChannel(beta) must fail")
	}
}
