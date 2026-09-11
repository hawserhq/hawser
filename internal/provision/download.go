package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Fetcher retrieves the rootfs. The seam exists so tests can serve a tarball
// without network access, and so a future version can add proxy handling in one
// place.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (io.ReadCloser, error)
}

// HTTPFetcher retrieves a rootfs over HTTP(S), or from the local filesystem for
// a file:// URL or a plain path.
//
// Local paths are supported because a development install and the acceptance
// suite both need to install a rootfs built by guest/rootfs/build.sh before any
// release exists — which is exactly what ErrNotPublished tells the user to do.
// Verification is identical either way: a local rootfs is checksummed too.
type HTTPFetcher struct{ Client *http.Client }

// Fetch implements Fetcher.
func (f HTTPFetcher) Fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	if path, ok := localPath(url); ok {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("opening local rootfs %s: %w", path, err)
		}
		return file, nil
	}

	client := f.Client
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
		return nil, fmt.Errorf("fetching %s: HTTP %s", url, resp.Status)
	}
	return resp.Body, nil
}

// localPath recognizes a filesystem source and returns the path to open.
//
// Handles file:// URLs (including Windows' file:///C:/x and file://C:/x spellings)
// and bare paths such as C:\build\rootfs.tar.gz — which is what someone
// naturally passes to --rootfs-url after running the rootfs build.
func localPath(raw string) (string, bool) {
	if after, ok := strings.CutPrefix(raw, "file://"); ok {
		// file:///C:/x -> /C:/x; strip the leading slash before a drive letter.
		p := after
		if len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		return filepath.FromSlash(p), true
	}
	// A Windows drive path is not a URL scheme, despite the colon.
	if len(raw) > 2 && raw[1] == ':' {
		return filepath.FromSlash(raw), true
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) || strings.HasPrefix(raw, ".") {
		return filepath.FromSlash(raw), true
	}
	return "", false
}

// ErrChecksumMismatch reports a rootfs whose contents do not match the expected
// digest. It is a distinct type because callers must never treat it as a
// transient failure to retry past: the rootfs becomes root inside the engine VM,
// so an unverified one is not something to shrug at.
type ErrChecksumMismatch struct {
	URL      string
	Expected string
	Actual   string
}

func (e *ErrChecksumMismatch) Error() string {
	return fmt.Sprintf("rootfs checksum mismatch for %s:\n  expected %s\n  actual   %s\n"+
		"refusing to import. The download may be corrupt or tampered with; "+
		"verify the release asset and its published checksum.",
		e.URL, e.Expected, e.Actual)
}

// FetchRootfs downloads (or copies, for a local/file:// URL) the rootfs to dest
// and verifies its SHA-256. Exposed so `skrog bundle` can pack a verified
// rootfs into an air-gap archive (#75). Idempotent: a cached file at dest that
// already matches is reused rather than refetched.
func (p *Provisioner) FetchRootfs(ctx context.Context, url, wantSHA, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return p.fetchRootfs(ctx, url, wantSHA, dest)
}

// fetchRootfs downloads to dest and verifies its SHA-256 before returning.
//
// The download lands on a temporary file that is only renamed into place after
// verification, so an interrupted or corrupt transfer can never be mistaken for
// a cached good copy on the next run.
func (p *Provisioner) fetchRootfs(ctx context.Context, url, wantSHA, dest string) error {
	if wantSHA == "" {
		return fmt.Errorf("no expected checksum for %s: refusing to import an unverified rootfs", url)
	}
	wantSHA = strings.ToLower(strings.TrimSpace(wantSHA))

	// A verified cached copy is reused; a stale or corrupt one is replaced.
	if sum, err := fileSHA256(dest); err == nil {
		if sum == wantSHA {
			p.logger().Info("rootfs already present and verified", "path", dest)
			return nil
		}
		p.logger().Warn("cached rootfs failed verification, refetching",
			"path", dest, "expected", wantSHA, "actual", sum)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".rootfs-*.partial")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Removed unless the rename below succeeds, so a failed attempt leaves
	// nothing behind that a later run could mistake for a complete download.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	body, err := p.fetcher().Fetch(ctx, url)
	if err != nil {
		return fmt.Errorf("downloading rootfs: %w", err)
	}
	defer body.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), body); err != nil {
		return fmt.Errorf("downloading rootfs: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing rootfs: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA {
		return &ErrChecksumMismatch{URL: url, Expected: wantSHA, Actual: got}
	}

	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("finalizing rootfs download: %w", err)
	}
	p.logger().Info("rootfs downloaded and verified", "path", dest, "sha256", got)
	return nil
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
