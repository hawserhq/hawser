// Package bundle is the air-gap install format (#75): a single .zip holding a
// verified engine rootfs and a skrog.lock, so an isolated-network machine
// installs entirely from the file with zero network calls. It builds on the
// lockfile (the pin + checksum) and the existing checksum-mandatory install
// path (which already reads a local rootfs and verifies it).
package bundle

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/wslkit/skrog/internal/lockfile"
)

// lockEntry is the fixed name of the lock inside a bundle.
const lockEntry = "skrog.lock"

// rootfsEntryName is where the rootfs lives inside the bundle: the basename of
// the lock's rootfs URL, so the same filename the release uses is preserved.
func rootfsEntryName(l lockfile.Lock) string {
	return path.Base(l.Rootfs.URL)
}

// Create writes a .zip bundle to dest containing the rootfs at rootfsPath and
// the lock. The lock is written first so `Open` can read it without scanning
// the (large) rootfs entry.
func Create(dest string, l lockfile.Lock, rootfsPath string) error {
	if err := l.Validate(); err != nil {
		return err
	}
	lockBytes, err := l.Marshal()
	if err != nil {
		return err
	}

	rootfs, err := os.Open(rootfsPath)
	if err != nil {
		return fmt.Errorf("opening rootfs %s: %w", rootfsPath, err)
	}
	defer rootfs.Close()

	// Write to a temp file and rename, so an interrupted build never leaves a
	// half-written bundle that looks complete. One deferred cleanup covers every
	// error path: close the file and drop the temp unless we committed.
	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		out.Close()
		if !committed {
			os.Remove(tmp)
		}
	}()

	zw := zip.NewWriter(out)
	lw, err := zw.Create(lockEntry)
	if err != nil {
		return err
	}
	if _, err := lw.Write(lockBytes); err != nil {
		return err
	}
	rw, err := zw.Create(rootfsEntryName(l))
	if err != nil {
		return err
	}
	if _, err := io.Copy(rw, rootfs); err != nil {
		return fmt.Errorf("writing rootfs into bundle: %w", err)
	}
	if err := zw.Close(); err != nil {
		return err
	}
	// Close before the rename: Windows will not rename an open file.
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	committed = true
	return nil
}

// Bundle is an opened air-gap bundle.
type Bundle struct {
	zr   *zip.ReadCloser
	lock lockfile.Lock
}

// Open reads and validates a bundle's lock. The rootfs is not read until
// ExtractRootfs, so opening is cheap.
func Open(path string) (*Bundle, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening bundle %s: %w", path, err)
	}
	f, err := entry(zr, lockEntry)
	if err != nil {
		zr.Close()
		return nil, fmt.Errorf("bundle is missing %s: %w", lockEntry, err)
	}
	rc, err := f.Open()
	if err != nil {
		zr.Close()
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		zr.Close()
		return nil, err
	}
	lock, err := lockfile.Parse(b)
	if err != nil {
		zr.Close()
		return nil, err
	}
	return &Bundle{zr: zr, lock: lock}, nil
}

// Lock returns the bundle's engine pin.
func (b *Bundle) Lock() lockfile.Lock { return b.lock }

// ExtractRootfs writes the bundled rootfs to dest atomically. It does not verify
// the checksum — the install path does that when it reads dest — so extraction
// and verification stay in one place.
func (b *Bundle) ExtractRootfs(dest string) error {
	f, err := entry(b.zr, rootfsEntryName(b.lock))
	if err != nil {
		return fmt.Errorf("bundle is missing its rootfs %q: %w", rootfsEntryName(b.lock), err)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// RootfsName is the bundled rootfs filename, used to name the extracted file.
func (b *Bundle) RootfsName() string { return rootfsEntryName(b.lock) }

// Close releases the bundle's file handle.
func (b *Bundle) Close() error { return b.zr.Close() }

func entry(zr *zip.ReadCloser, name string) (*zip.File, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f, nil
		}
	}
	return nil, fmt.Errorf("no entry %q", name)
}
