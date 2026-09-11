package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wslkit/skrog/internal/wsl"
)

// fakeWSL implements wsl.WSL; Export writes a stand-in tarball so the checksum
// and metadata paths are real.
type fakeWSL struct {
	content                            []byte
	imported, unregistered, terminated bool
	importErr                          error
}

func (f *fakeWSL) Status(context.Context) (wsl.Status, error) { return wsl.Status{}, nil }
func (f *fakeWSL) Export(_ context.Context, _, path string) error {
	c := f.content
	if c == nil {
		c = []byte("fake distro filesystem tarball")
	}
	return os.WriteFile(path, c, 0o644)
}
func (f *fakeWSL) Import(_ context.Context, _, _, _ string) error {
	f.imported = true
	return f.importErr
}
func (f *fakeWSL) Unregister(context.Context, string) error { f.unregistered = true; return nil }
func (f *fakeWSL) Terminate(context.Context, string) error  { f.terminated = true; return nil }
func (f *fakeWSL) List(context.Context) ([]wsl.Distro, error) {
	return nil, nil
}
func (f *fakeWSL) Exec(context.Context, string, string, ...string) (string, error) { return "", nil }
func (f *fakeWSL) Start(context.Context, string, string, ...string) (func(), error) {
	return func() {}, nil
}

func newManager(t *testing.T, w *fakeWSL) *Manager {
	t.Helper()
	return &Manager{WSL: w, StateDir: t.TempDir(), Distro: "skrog-engine", DataDir: t.TempDir()}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"dev", "before-upgrade", "v1.2"} {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", ".", "..", "a/b", `a\b`, ".hidden"} {
		if err := ValidName(n); err == nil {
			t.Errorf("ValidName(%q) should fail", n)
		}
	}
}

func TestSaveRecordsChecksumAndMeta(t *testing.T) {
	m := newManager(t, &fakeWSL{})
	meta, err := m.Save(context.Background(), "dev", "29.7.2")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "dev" || meta.EngineVersion != "29.7.2" || meta.SizeBytes == 0 || len(meta.SHA256) != 64 {
		t.Fatalf("meta = %+v", meta)
	}
	if _, err := os.Stat(m.tar("dev")); err != nil {
		t.Errorf("tar not written: %v", err)
	}
	got, err := m.Get("dev")
	if err != nil || got.SHA256 != meta.SHA256 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}

func TestListNewestFirst(t *testing.T) {
	m := newManager(t, &fakeWSL{})
	for _, n := range []string{"a", "b", "c"} {
		if _, err := m.Save(context.Background(), n, "29.7.2"); err != nil {
			t.Fatal(err)
		}
	}
	snaps, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 {
		t.Fatalf("got %d snapshots", len(snaps))
	}
	for i := 1; i < len(snaps); i++ {
		if snaps[i].Created.After(snaps[i-1].Created) {
			t.Error("List not sorted newest-first")
		}
	}
}

func TestRestoreVerifiesThenReplaces(t *testing.T) {
	w := &fakeWSL{}
	m := newManager(t, w)
	if _, err := m.Save(context.Background(), "dev", "29.7.2"); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	if !w.terminated || !w.unregistered || !w.imported {
		t.Fatalf("restore should terminate+unregister+import: %+v", w)
	}
}

func TestRestoreRefusesCorruptArchive(t *testing.T) {
	w := &fakeWSL{}
	m := newManager(t, w)
	if _, err := m.Save(context.Background(), "dev", "29.7.2"); err != nil {
		t.Fatal(err)
	}
	// Tamper with the archive after its checksum was recorded.
	if err := os.WriteFile(m.tar("dev"), []byte("corrupted contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(context.Background(), "dev"); err == nil {
		t.Fatal("restore should refuse a checksum mismatch")
	}
	if w.unregistered {
		t.Error("a corrupt archive must not unregister the live engine")
	}
}

func TestRestoreMissing(t *testing.T) {
	m := newManager(t, &fakeWSL{})
	if err := m.Restore(context.Background(), "ghost"); err == nil {
		t.Fatal("restoring a missing snapshot should error")
	}
}

func TestDelete(t *testing.T) {
	m := newManager(t, &fakeWSL{})
	m.Save(context.Background(), "dev", "29.7.2")
	if err := m.Delete("dev"); err != nil {
		t.Fatal(err)
	}
	if m.Exists("dev") {
		t.Error("snapshot still exists after delete")
	}
	if _, err := os.Stat(filepath.Join(m.dir(), "dev.tar")); !os.IsNotExist(err) {
		t.Error("tar not removed")
	}
	if err := m.Delete("dev"); err == nil {
		t.Error("deleting a missing snapshot should error")
	}
}
