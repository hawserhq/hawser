package bundle

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// goodSHA is a well-formed SHA-256 for lock validity in these error tests; the
// bytes need not match any real file.
const goodSHA = "aee4312306d7d613ca3d0c23049c19837707cd4837b266ea057b544ac9605af4"

// makeZip writes a zip with the given entries, for crafting malformed bundles.
func makeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func lockJSON(t *testing.T) string {
	t.Helper()
	b, err := testLock(goodSHA).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateMissingRootfs(t *testing.T) {
	dir := t.TempDir()
	err := Create(filepath.Join(dir, "b.zip"), testLock(goodSHA), filepath.Join(dir, "nope.tar.gz"))
	if err == nil {
		t.Fatal("Create should fail when the rootfs file is missing")
	}
}

func TestOpenMissingLock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.zip")
	makeZip(t, p, map[string]string{"unrelated.txt": "x"})
	if _, err := Open(p); err == nil {
		t.Fatal("Open should fail on a zip with no skrog.lock")
	}
}

func TestOpenBadLock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.zip")
	makeZip(t, p, map[string]string{"skrog.lock": "{ not valid json"})
	if _, err := Open(p); err == nil {
		t.Fatal("Open should fail on an unparseable lock")
	}
}

func TestExtractMissingRootfsEntry(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.zip")
	// A valid lock, but no rootfs entry alongside it.
	makeZip(t, p, map[string]string{"skrog.lock": lockJSON(t)})
	b, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.ExtractRootfs(filepath.Join(dir, "out.tar.gz")); err == nil {
		t.Fatal("ExtractRootfs should fail when the rootfs entry is absent")
	}
}

func TestExtractToUnwritableDest(t *testing.T) {
	dir := t.TempDir()
	rootfsPath, sha := writeFakeRootfs(t, dir)
	bundlePath := filepath.Join(dir, "b.zip")
	if err := Create(bundlePath, testLock(sha), rootfsPath); err != nil {
		t.Fatal(err)
	}
	b, err := Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// A file where a directory is needed: MkdirAll on its "parent" fails.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.ExtractRootfs(filepath.Join(blocker, "sub", "out.tar.gz")); err == nil {
		t.Fatal("ExtractRootfs should fail when the destination dir cannot be created")
	}
}
