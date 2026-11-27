package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"aead.dev/minisign"
)

// Size limits for what an install downloads.
const (
	maxChecksumsSize = 64 << 10
	maxBuildSize     = 1 << 30
)

// Step is the step an install is on, or stopped at.
type Step string

const (
	// StepDownload fetches checksums.txt, its signature, and the build.
	StepDownload Step = "download"
	// StepVerify checks the signature, the SHA-256 and, on macOS, the team.
	StepVerify Step = "verify"
	// StepInstall replaces the installed build. Cancelling no longer stops
	// the install once it starts.
	StepInstall Step = "install"
)

// Kind is what an install failed at.
type Kind string

const (
	// KindNetwork: a download did not complete.
	KindNetwork Kind = "network"
	// KindNoBuild: the release has no asset for this GOOS/GOARCH.
	KindNoBuild Kind = "no-build"
	// KindSignature: checksums.txt is missing, unsigned, or not signed by
	// the embedded key.
	KindSignature Kind = "signature"
	// KindChecksum: the build does not match its signed SHA-256.
	KindChecksum Kind = "checksum"
	// KindTeam: on macOS, the new build is not signed by the installed
	// build's Apple team.
	KindTeam Kind = "team"
	// KindPermissions: the install directory is not writable. Found before
	// anything downloads; Marasi never elevates.
	KindPermissions Kind = "permissions"
	// KindInstall: replacing the installed build failed; it is unchanged.
	KindInstall Kind = "install"
	// KindCancelled: the install's context ended before the swap.
	KindCancelled Kind = "cancelled"
)

// Progress is the step a running install is on and how far the build's
// download has got. Total is 0 when the size is unknown. After the download
// Received and Total keep its size.
type Progress struct {
	Step     Step
	Received int64
	Total    int64
}

// Error is a failed install. The installed build is unchanged.
type Error struct {
	Kind Kind
	Step Step
	// Page is the release page, where the build can be installed by hand.
	Page string
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Install downloads c's build for this platform, verifies it, and replaces
// the executable at target (symlinks resolved) with it. progress, when not
// nil, is called as each step starts and as the build downloads; it runs on
// the calling goroutine.
//
// Cancelling ctx stops the install until the install step starts; after
// that the swap completes. Every failure is an *Error, and leaves target
// and its directory as they were.
func (u *Updater) Install(ctx context.Context, c Candidate, target string, progress func(Progress)) error {
	in := &install{u: u, c: c, progress: progress, step: StepDownload}
	if in.progress == nil {
		in.progress = func(Progress) {}
	}
	err := in.run(ctx, target)
	if err == nil {
		return nil
	}
	var failure *Error
	if !errors.As(err, &failure) {
		failure = &Error{Kind: KindInstall, Step: in.step, Err: err}
	}
	if ctxErr := ctx.Err(); ctxErr != nil && failure.Kind != KindPermissions && failure.Step != StepInstall {
		failure = &Error{Kind: KindCancelled, Step: in.step, Err: fmt.Errorf("the install was cancelled: %w", ctxErr)}
	}
	failure.Page = c.Page
	return failure
}

// install is one run of Install.
type install struct {
	u        *Updater
	c        Candidate
	progress func(Progress)
	step     Step
	received int64
	total    int64
}

func (in *install) enter(step Step) {
	in.step = step
	in.report()
}

func (in *install) report() {
	in.progress(Progress{Step: in.step, Received: in.received, Total: in.total})
}

func fail(kind Kind, step Step, format string, args ...any) *Error {
	return &Error{Kind: kind, Step: step, Err: fmt.Errorf(format, args...)}
}

func (in *install) run(ctx context.Context, target string) error {
	target, err := resolveTarget(target)
	if err != nil {
		return fail(KindInstall, StepInstall, "finding the installed build: %w", err)
	}
	if err := CheckWritable(target); err != nil {
		return &Error{Kind: KindPermissions, Step: StepInstall, Err: err}
	}
	archiveAsset, hasArchive := in.u.archiveAsset(in.c)
	bareAsset, hasBare := in.c.Asset(in.u.AssetName())
	if !hasArchive && !hasBare {
		return fail(KindNoBuild, StepDownload, "no build for this platform (%s)", in.u.AssetName())
	}
	checksumsURL, okSums := in.c.Asset(checksumsAsset)
	signatureURL, okSig := in.c.Asset(signatureAsset)
	if !okSums || !okSig {
		return fail(KindSignature, StepVerify, "the release is not signed: it has no %s and %s", checksumsAsset, signatureAsset)
	}

	if hasArchive {
		in.total = archiveAsset.Size
	} else {
		in.total = bareAsset.Size
	}
	in.enter(StepDownload)
	checksums, err := in.u.downloadSmall(ctx, checksumsURL)
	if err != nil {
		return err
	}
	signature, err := in.u.downloadSmall(ctx, signatureURL)
	if err != nil {
		return err
	}
	// The signature is checked before the build downloads, so a release
	// whose checksums are not ours never has its build fetched.
	if !minisign.Verify(in.u.key, checksums, signature) {
		return fail(KindSignature, StepVerify, "the release's checksums are not signed by the Marasi signing key")
	}
	build, want, isArchive, err := pickBuildAsset(parseChecksums(checksums), archiveAsset, hasArchive, bareAsset, hasBare)
	if err != nil {
		return err
	}
	in.total = build.Size

	dir := filepath.Dir(target)
	temp, err := os.CreateTemp(dir, ".marasi-update-*")
	if err != nil {
		return fail(KindInstall, StepDownload, "making room for the download beside %s: %w", target, err)
	}
	downloadPath := temp.Name()
	keepDownload := false
	defer func() {
		if !keepDownload {
			os.Remove(downloadPath)
		}
	}()
	got, err := in.downloadBuild(ctx, build, temp)
	if closeErr := temp.Close(); err == nil && closeErr != nil {
		err = fail(KindInstall, StepDownload, "writing the download: %w", closeErr)
	}
	if err != nil {
		return err
	}

	in.enter(StepVerify)
	if got != want {
		return fail(KindChecksum, StepVerify, "the downloaded %s does not match its signed checksum", build.Name)
	}

	stagedPath := downloadPath
	keepStaged := &keepDownload
	extractedSwapped := false
	if isArchive {
		extracted, err := os.CreateTemp(dir, ".marasi-update-*")
		if err != nil {
			return fail(KindInstall, StepVerify, "making room to extract %s beside %s: %w", build.Name, target, err)
		}
		extractedPath := extracted.Name()
		defer func() {
			if !extractedSwapped {
				os.Remove(extractedPath)
			}
		}()
		extractErr := extractArchive(downloadPath, build.Name, expectedEntryName(in.u.goos), extracted, in.u.buildSizeLimit())
		closeErr := extracted.Close()
		if extractErr != nil {
			return fail(KindInstall, StepVerify, "extracting %s: %w", build.Name, extractErr)
		}
		if closeErr != nil {
			return fail(KindInstall, StepVerify, "writing the extracted build from %s: %w", build.Name, closeErr)
		}
		// Remove the downloaded archive before the swap so it never lingers
		// beside target once the install succeeds.
		_ = os.Remove(downloadPath)
		stagedPath = extractedPath
		keepStaged = &extractedSwapped
	}

	if in.u.goos == "darwin" {
		if err := in.u.checkTeam(ctx, target, stagedPath); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	in.enter(StepInstall)
	mode := os.FileMode(0o755)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm() | 0o111
	}
	if err := os.Chmod(stagedPath, mode); err != nil {
		return fail(KindInstall, StepInstall, "marking the new build executable: %w", err)
	}
	if err := replace(in.u.goos, stagedPath, target); err != nil {
		return fail(KindInstall, StepInstall, "replacing %s: %w", target, err)
	}
	*keepStaged = true
	return nil
}

// pickBuildAsset prefers the platform archive when the release publishes it
// and the verified signed checksums list it, falling back to the bare
// executable for older releases without an archive.
func pickBuildAsset(sums map[string]string, archive Asset, hasArchive bool, bare Asset, hasBare bool) (asset Asset, want string, isArchive bool, err error) {
	if hasArchive {
		if sum, ok := sums[archive.Name]; ok {
			return archive, sum, true, nil
		}
	}
	if hasBare {
		if sum, ok := sums[bare.Name]; ok {
			return bare, sum, false, nil
		}
	}
	switch {
	case hasArchive && hasBare:
		return Asset{}, "", false, fail(KindChecksum, StepVerify, "the signed checksums do not list %s or %s", archive.Name, bare.Name)
	case hasArchive:
		return Asset{}, "", false, fail(KindChecksum, StepVerify, "the signed checksums do not list %s", archive.Name)
	default:
		return Asset{}, "", false, fail(KindChecksum, StepVerify, "the signed checksums do not list %s", bare.Name)
	}
}

// downloadSmall fetches a small release asset such as checksums.txt.
func (u *Updater) downloadSmall(ctx context.Context, asset Asset) ([]byte, error) {
	body, _, err := u.get(ctx, asset)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxChecksumsSize+1))
	if err != nil {
		return nil, fail(KindNetwork, StepDownload, "downloading %s: %w", asset.Name, err)
	}
	if len(data) > maxChecksumsSize {
		return nil, fail(KindSignature, StepVerify, "%s is larger than %d bytes", asset.Name, maxChecksumsSize)
	}
	return data, nil
}

// downloadBuild streams the build into dst, reporting progress, and returns
// its SHA-256 in hex.
func (in *install) downloadBuild(ctx context.Context, asset Asset, dst io.Writer) (string, error) {
	body, size, err := in.u.get(ctx, asset)
	if err != nil {
		return "", err
	}
	defer body.Close()
	if size > 0 {
		in.total = size
	}
	limit := in.u.buildSizeLimit()
	hash := sha256.New()
	counter := &progressWriter{in: in, last: -1}
	n, err := io.Copy(io.MultiWriter(dst, hash, counter), io.LimitReader(body, limit+1))
	if err != nil {
		return "", fail(KindNetwork, StepDownload, "downloading %s: %w", asset.Name, err)
	}
	if n > limit {
		return "", fail(KindChecksum, StepVerify, "%s is larger than %d bytes", asset.Name, limit)
	}
	if in.total <= 0 {
		in.total = n
	}
	in.received = n
	in.report()
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (u *Updater) get(ctx context.Context, asset Asset) (io.ReadCloser, int64, error) {
	body, size, err := u.getURL(ctx, asset.URL)
	if err == nil {
		return body, size, nil
	}
	if asset.BrowserURL == "" || asset.BrowserURL == asset.URL || ctx.Err() != nil {
		return nil, 0, fail(KindNetwork, StepDownload, "downloading %s: %w", asset.Name, err)
	}
	body, size, browserErr := u.getURL(ctx, asset.BrowserURL)
	if browserErr != nil {
		return nil, 0, fail(KindNetwork, StepDownload, "downloading %s: %w (from the GitHub API: %w)", asset.Name, browserErr, err)
	}
	return body, size, nil
}

// getURL requests one asset URL and returns the body of a 200 response.
func (u *Updater) getURL(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "marasi/"+u.installed.Version)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, httpStatusError(resp)
	}
	return resp.Body, resp.ContentLength, nil
}

// progressWriter reports download progress each whole percent, or each MiB
// when the size is unknown.
type progressWriter struct {
	in   *install
	last int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.in.received += int64(len(b))
	mark := p.in.received >> 20
	if p.in.total > 0 {
		mark = p.in.received * 100 / p.in.total
	}
	if mark != p.last {
		p.last = mark
		p.in.report()
	}
	return len(b), nil
}

// parseChecksums reads sha256sum output, "<hex>  <name>" per line ("*name"
// in binary mode), into lowercase hex by asset name.
func parseChecksums(checksums []byte) map[string]string {
	sums := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(checksums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		if decoded, err := hex.DecodeString(sum); err != nil || len(decoded) != sha256.Size {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = sum
	}
	return sums
}

// checkTeam requires the new build to be signed by the installed build's
// Apple team (docs/adr/2026-10-18-updates-are-signed-with-an-embedded-key.md).
func (u *Updater) checkTeam(ctx context.Context, installed, download string) error {
	want, err := u.team.TeamID(ctx, installed)
	if err != nil {
		return fail(KindTeam, StepVerify, "reading the installed build's team: %w", err)
	}
	if want == "" {
		return fail(KindTeam, StepVerify, "the installed build is not signed by an Apple developer team")
	}
	got, err := u.team.TeamID(ctx, download)
	if err != nil {
		return fail(KindTeam, StepVerify, "reading the new build's team: %w", err)
	}
	if got != want {
		return fail(KindTeam, StepVerify, "the new build is not signed by the installed build's Apple team %s", want)
	}
	return nil
}
