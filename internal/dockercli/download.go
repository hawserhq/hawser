package dockercli

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrChecksumMismatch reports a download whose contents do not match the pinned
// digest. Distinct so callers never retry past it: these binaries run on the
// host, so an unverified one is not something to shrug at.
type ErrChecksumMismatch struct {
	URL      string
	Expected string
	Actual   string
}

func (e *ErrChecksumMismatch) Error() string {
	return fmt.Sprintf("checksum mismatch for %s:\n  expected %s\n  actual   %s\n"+
		"refusing to install. The download may be corrupt or tampered with.",
		e.URL, e.Expected, e.Actual)
}

// fetchVerified downloads url to dest and verifies its SHA-256, reusing a cached
// file that already matches. The transfer lands on a temp file renamed into
// place only after verification, so an interrupted download is never mistaken
// for a good cached copy. A file:// URL or bare path is copied from disk (the
// air-gap path) and verified identically.
func (o Options) fetchVerified(ctx context.Context, url, wantSHA, dest string) error {
	if wantSHA == "" {
		return fmt.Errorf("no expected checksum for %s: refusing an unverified download", url)
	}
	wantSHA = strings.ToLower(strings.TrimSpace(wantSHA))

	if sum, err := fileSHA256(dest); err == nil && sum == wantSHA {
		o.logf("cached and verified: %s", dest)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dl-*.partial")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpName) }()

	rc, err := o.open(ctx, url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer rc.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), rc); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return &ErrChecksumMismatch{URL: url, Expected: wantSHA, Actual: got}
	}
	return os.Rename(tmpName, dest)
}

// open returns a reader for an http(s) URL, or for a local file:// URL / path.
func (o Options) open(ctx context.Context, url string) (io.ReadCloser, error) {
	if p, ok := localPath(url); ok {
		return os.Open(p)
	}
	client := o.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return resp.Body, nil
}

// localPath recognizes a filesystem source (file:// URL or bare path) for the
// air-gap install path, mirroring provision's handling.
func localPath(raw string) (string, bool) {
	if after, ok := strings.CutPrefix(raw, "file://"); ok {
		p := after
		if len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		return filepath.FromSlash(p), true
	}
	if len(raw) > 2 && raw[1] == ':' {
		return filepath.FromSlash(raw), true
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) || strings.HasPrefix(raw, ".") {
		return filepath.FromSlash(raw), true
	}
	return "", false
}

// extractZipEntry copies one slash-separated path out of a zip archive to dest.
func extractZipEntry(zipPath, entry, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", zipPath, err)
	}
	defer zr.Close()

	want := path.Clean(entry)
	for _, f := range zr.File {
		if path.Clean(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeFile(dest, rc, 0o755)
	}
	return fmt.Errorf("entry %q not found in %s", entry, zipPath)
}

// copyFile copies src to dest with the given mode.
func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFile(dest, in, mode)
}

// writeFile streams r into dest atomically (temp + rename) with mode.
func writeFile(dest string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".w-*.partial")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, r); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
