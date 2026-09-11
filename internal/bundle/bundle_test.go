package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/wslkit/skrog/internal/lockfile"
)

func writeFakeRootfs(t *testing.T, dir string) (path, sha string) {
	t.Helper()
	content := []byte("this is a pretend rootfs tarball\n")
	sum := sha256.Sum256(content)
	path = filepath.Join(dir, "rootfs.tar.gz")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(sum[:])
}

func testLock(sha string) lockfile.Lock {
	return lockfile.Lock{
		SchemaVersion: lockfile.SchemaVersion,
		EngineVersion: "29.7.2",
		Rootfs:        lockfile.Rootfs{URL: "https://example.com/download/rootfs.tar.gz", SHA256: sha},
		Components:    map[string]string{"dockerd": "29.7.2"},
	}
}

func TestCreateOpenExtractRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rootfsPath, sha := writeFakeRootfs(t, dir)
	lock := testLock(sha)

	bundlePath := filepath.Join(dir, "bundle.zip")
	if err := Create(bundlePath, lock, rootfsPath); err != nil {
		t.Fatal(err)
	}

	b, err := Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if b.Lock().EngineVersion != "29.7.2" || b.Lock().Rootfs.SHA256 != sha {
		t.Fatalf("lock not preserved: %+v", b.Lock())
	}
	if b.RootfsName() != "rootfs.tar.gz" {
		t.Fatalf("rootfs entry name = %q", b.RootfsName())
	}

	out := filepath.Join(dir, "extracted.tar.gz")
	if err := b.ExtractRootfs(out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != sha {
		t.Fatal("extracted rootfs bytes do not match the original")
	}
}

func TestCreateRejectsInvalidLock(t *testing.T) {
	dir := t.TempDir()
	rootfsPath, _ := writeFakeRootfs(t, dir)
	bad := lockfile.Lock{SchemaVersion: lockfile.SchemaVersion} // missing version/url/sha
	if err := Create(filepath.Join(dir, "b.zip"), bad, rootfsPath); err == nil {
		t.Fatal("Create should reject an invalid lock")
	}
}

func TestOpenRejectsNonBundle(t *testing.T) {
	dir := t.TempDir()
	// A zip with no skrog.lock: reuse Create's output but tamper is overkill;
	// a plain non-zip file exercises the open-error path.
	junk := filepath.Join(dir, "not.zip")
	os.WriteFile(junk, []byte("not a zip"), 0o644)
	if _, err := Open(junk); err == nil {
		t.Fatal("Open should reject a non-zip file")
	}
}
