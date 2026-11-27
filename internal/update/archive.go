package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxTarTrailer bounds the zero-filled padding read after a tar archive's
// end-of-archive blocks so gzip's CRC32 and length trailer are verified
// without risking unbounded decompression.
const maxTarTrailer = 1 << 20

// expectedEntryName is the single file name allowed inside a platform release
// archive (scripts/archive-cli.sh).
func expectedEntryName(goos string) string {
	if goos == "windows" {
		return "marasi.exe"
	}
	return "marasi"
}

// extractArchive unpacks a verified release archive into dst. It fails closed:
// the archive must contain exactly one regular file named wantName and nothing
// else, bounded by maxSize.
func extractArchive(archivePath, assetName, wantName string, dst io.Writer, maxSize int64) error {
	switch {
	case strings.HasSuffix(assetName, ".zip"):
		return extractZip(archivePath, wantName, dst, maxSize)
	case strings.HasSuffix(assetName, ".tar.gz"):
		return extractTarGz(archivePath, wantName, dst, maxSize)
	default:
		return fmt.Errorf("unsupported archive format for %s", assetName)
	}
}

func extractZip(archivePath, wantName string, dst io.Writer, maxSize int64) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip archive: %w", err)
	}
	defer zr.Close()

	if len(zr.File) == 0 {
		return errors.New("zip archive is empty")
	}
	if len(zr.File) != 1 {
		return fmt.Errorf("zip archive contains %d entries; expected only %s", len(zr.File), wantName)
	}
	entry := zr.File[0]
	if entry.Name != wantName {
		return fmt.Errorf("zip archive contains %q; expected %q", entry.Name, wantName)
	}
	if !entry.Mode().IsRegular() {
		return fmt.Errorf("zip archive entry %q is not a regular file", entry.Name)
	}
	if entry.UncompressedSize64 > uint64(maxSize) {
		return fmt.Errorf("zip archive entry %q is larger than %d bytes", entry.Name, maxSize)
	}

	rc, err := entry.Open()
	if err != nil {
		return fmt.Errorf("reading zip archive entry %q: %w", entry.Name, err)
	}
	n, copyErr := io.Copy(dst, io.LimitReader(rc, maxSize+1))
	closeErr := rc.Close()
	if copyErr != nil {
		return fmt.Errorf("extracting %q from zip archive: %w", entry.Name, copyErr)
	}
	if n > maxSize {
		return fmt.Errorf("zip archive entry %q is larger than %d bytes", entry.Name, maxSize)
	}
	if closeErr != nil {
		return fmt.Errorf("closing %q in zip archive: %w", entry.Name, closeErr)
	}
	return nil
}

func extractTarGz(archivePath, wantName string, dst io.Writer, maxSize int64) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening tar.gz archive: %w", err)
	}
	defer f.Close()

	// Wrap f in a bufio.Reader so gzip.NewReader does not allocate its own
	// hidden lookahead buffer; after the single gzip stream ends we can check
	// br for unexpected trailing data or concatenated streams.
	br := bufio.NewReader(f)
	gz, err := gzip.NewReader(br)
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer gz.Close()
	gz.Multistream(false)

	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if errors.Is(err, io.EOF) {
		return errors.New("tar.gz archive is empty")
	}
	if err != nil {
		return fmt.Errorf("reading tar.gz archive: %w", err)
	}
	if hdr.Name != wantName {
		return fmt.Errorf("tar.gz archive contains %q; expected %q", hdr.Name, wantName)
	}
	if hdr.Typeflag != tar.TypeReg {
		return fmt.Errorf("tar.gz archive entry %q is not a regular file", hdr.Name)
	}
	if hdr.Size < 0 || hdr.Size > maxSize {
		return fmt.Errorf("tar.gz archive entry %q is larger than %d bytes", hdr.Name, maxSize)
	}

	n, err := io.Copy(dst, io.LimitReader(tr, maxSize+1))
	if err != nil {
		return fmt.Errorf("extracting %q from tar.gz archive: %w", hdr.Name, err)
	}
	if n > maxSize {
		return fmt.Errorf("tar.gz archive entry %q is larger than %d bytes", hdr.Name, maxSize)
	}

	extra, err := tr.Next()
	if err == nil {
		return fmt.Errorf("tar.gz archive contains extra entry %q; expected only %s", extra.Name, wantName)
	}
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading tar.gz archive after %q: %w", hdr.Name, err)
	}

	if err := drainTarPadding(gz); err != nil {
		return fmt.Errorf("verifying tar.gz trailer: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("closing gzip stream: %w", err)
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("checking tar.gz trailing bytes: %w", err)
		}
		return errors.New("tar.gz archive has unexpected trailing data after gzip stream")
	}
	return nil
}

func drainTarPadding(r io.Reader) error {
	var buf [4096]byte
	var total int64
	for {
		n, err := r.Read(buf[:])
		for _, b := range buf[:n] {
			if b != 0 {
				return errors.New("unexpected non-zero data after tar end-of-archive")
			}
		}
		total += int64(n)
		if total > maxTarTrailer {
			return fmt.Errorf("tar trailer exceeds %d bytes", maxTarTrailer)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
