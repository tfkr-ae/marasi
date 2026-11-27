package update

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestCheckSurfacesRateLimitErrors(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		headers     http.Header
		wantLimited bool
		wantContain []string
	}{
		{
			name:   "should detect 403 with X-RateLimit-Remaining 0 and X-RateLimit-Reset",
			status: http.StatusForbidden,
			headers: http.Header{
				"X-RateLimit-Remaining": {"0"},
				"X-RateLimit-Reset":     {"1795665600"},
			},
			wantLimited: true,
			wantContain: []string{"GitHub rate limit exceeded", "2026-11-26T04:00:00Z", "install the release by hand"},
		},
		{
			name:   "should detect 429 with Retry-After seconds",
			status: http.StatusTooManyRequests,
			headers: http.Header{
				"Retry-After": {"120"},
			},
			wantLimited: true,
			wantContain: []string{"GitHub rate limit exceeded", "try again in 120s"},
		},
		{
			name:   "should detect 429 with Retry-After HTTP date",
			status: http.StatusTooManyRequests,
			headers: http.Header{
				"Retry-After": {"Fri, 27 Nov 2026 05:30:00 GMT"},
			},
			wantLimited: true,
			wantContain: []string{"GitHub rate limit exceeded", "2026-11-27T05:30:00Z"},
		},
		{
			name:   "should detect secondary rate limit 403 with Retry-After",
			status: http.StatusForbidden,
			headers: http.Header{
				"Retry-After": {"60"},
			},
			wantLimited: true,
			wantContain: []string{"GitHub rate limit exceeded", "try again in 60s"},
		},
		{
			name:        "should detect 429 without reset headers",
			status:      http.StatusTooManyRequests,
			headers:     http.Header{},
			wantLimited: true,
			wantContain: []string{"GitHub rate limit exceeded", "try again later"},
		},
		{
			name:        "should keep plain 403 without rate limit headers as non-rate-limit error",
			status:      http.StatusForbidden,
			headers:     http.Header{},
			wantLimited: false,
			wantContain: []string{"403 Forbidden"},
		},
		{
			name:   "should keep 403 with remaining quota as non-rate-limit error",
			status: http.StatusForbidden,
			headers: http.Header{
				"X-RateLimit-Remaining": {"42"},
			},
			wantLimited: false,
			wantContain: []string{"403 Forbidden"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := newSigningKey(t)
			g := newFakeGitHub(t)
			g.failRelease(tc.status, tc.headers)
			u := newTestUpdater(t, g, key, "18.10.2026")

			_, err := u.Check(context.Background(), Release)
			if err == nil {
				t.Fatalf("\nwanted:\nerror\ngot:\nnil")
			}
			if got := errors.Is(err, ErrRateLimited); got != tc.wantLimited {
				t.Fatalf("\nwanted:\nerrors.Is(err, ErrRateLimited) = %v\ngot:\n%v (%v)", tc.wantLimited, got, err)
			}
			for _, sub := range tc.wantContain {
				if !strings.Contains(err.Error(), sub) {
					t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%q", sub, err.Error())
				}
			}
		})
	}
}

func TestInstallRateLimitFallbackAndErrorReporting(t *testing.T) {
	t.Run("should fall back to browser_download_url when API asset download is rate-limited", func(t *testing.T) {
		archive := makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("new build via browser")})
		f := newInstallFixture(t, "linux", "marasi", map[string][]byte{
			"marasi-linux-amd64.tar.gz": archive,
		})
		u := f.updater(t, Config{GOOS: "linux"})
		c := f.candidate(t, u)
		f.github.failRoutesWithHeaders(
			http.StatusForbidden,
			http.Header{"X-RateLimit-Remaining": {"0"}, "X-RateLimit-Reset": {"1795665600"}},
			0,
			nil,
		)

		if err := u.Install(context.Background(), c, f.target, nil); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got, err := os.ReadFile(f.target)
		if err != nil || string(got) != "new build via browser" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q (%v)", "new build via browser", got, err)
		}
		if routes := f.github.downloadRoutes(); !slices.Equal(routes, []string{"browser", "browser", "browser"}) {
			t.Fatalf("\nwanted:\n[browser browser browser]\ngot:\n%v", routes)
		}
	})

	t.Run("should report actionable rate-limit error when both API and browser routes fail", func(t *testing.T) {
		archive := makeTarGz(t, tarEntrySpec{name: "marasi", content: []byte("new build")})
		f := newInstallFixture(t, "linux", "marasi", map[string][]byte{
			"marasi-linux-amd64.tar.gz": archive,
		})
		u := f.updater(t, Config{GOOS: "linux"})
		c := f.candidate(t, u)
		f.github.failRoutesWithHeaders(
			http.StatusForbidden,
			http.Header{"X-RateLimit-Remaining": {"0"}, "X-RateLimit-Reset": {"1795665600"}},
			http.StatusTooManyRequests,
			http.Header{"Retry-After": {"30"}},
		)

		err := u.Install(context.Background(), c, f.target, nil)
		failure := assertFailure(t, err, KindNetwork, StepDownload)
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("\nwanted:\nErrRateLimited\ngot:\n%v", err)
		}
		for _, sub := range []string{"GitHub rate limit exceeded", "try again in 30s", "2026-11-26T04:00:00Z"} {
			if !strings.Contains(failure.Error(), sub) {
				t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%q", sub, failure.Error())
			}
		}
		f.assertUnchanged(t)
	})

	t.Run("should report actionable rate-limit error when only a single asset URL exists", func(t *testing.T) {
		f := newInstallFixture(t, "linux", "marasi", map[string][]byte{
			"marasi-linux-amd64": []byte("new build"),
		})
		u := f.updater(t, Config{GOOS: "linux"})
		c := f.candidate(t, u)
		for i := range c.Assets {
			c.Assets[i].BrowserURL = ""
		}
		f.github.failRoutesWithHeaders(
			http.StatusTooManyRequests,
			http.Header{"Retry-After": {"45"}},
			0,
			nil,
		)

		err := u.Install(context.Background(), c, f.target, nil)
		failure := assertFailure(t, err, KindNetwork, StepDownload)
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("\nwanted:\nErrRateLimited\ngot:\n%v", err)
		}
		if !strings.Contains(failure.Error(), "try again in 45s") {
			t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%q", "try again in 45s", failure.Error())
		}
		f.assertUnchanged(t)
	})
}
