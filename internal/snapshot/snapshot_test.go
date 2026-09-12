package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/wsl"
)

// fakeWSL implements wsl.WSL; Export writes a stand-in tarball so the checksum
// and metadata paths are real.
type fakeWSL struct {
	content                            []byte
	imported, unregistered, terminated bool
	importErr                          error
	unregisterErr                      error
	distros                            []string
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
func (f *fakeWSL) Unregister(context.Context, string) error {
	f.unregistered = true
	return f.unregisterErr
}
func (f *fakeWSL) Terminate(context.Context, string) error { f.terminated = true; return nil }
func (f *fakeWSL) List(context.Context) ([]wsl.Distro, error) {
	var out []wsl.Distro
	for _, n := range f.distros {
		out = append(out, wsl.Distro{Name: n})
	}
	return out, nil
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

// A restore whose import fails must say where the data is and how to get it
// back. Past the unregister the archive is the only copy, and the caller used
// to print "importing the snapshot: <wsl error>" over a machine with no engine
// and no instruction (#235).
func TestRestoreReportsAnOrphanedEngineWithTheRecoveryCommand(t *testing.T) {
	w := &fakeWSL{importErr: errors.New("wsl: not enough space")}
	m := newManager(t, w)
	if _, err := m.Save(context.Background(), "before-upgrade", ""); err != nil {
		t.Fatalf("Save: %v", err)
	}

	err := m.Restore(context.Background(), "before-upgrade")
	var orph *ErrOrphaned
	if !errors.As(err, &orph) {
		t.Fatalf("Restore error is %T (%v), want *ErrOrphaned", err, err)
	}
	msg := err.Error()
	for _, want := range []string{
		"Your data is intact in",
		m.ArchivePath("before-upgrade"),
		"wsl --import skrog-engine",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the recovery message does not mention %q:\n%s", want, msg)
		}
	}
	// The cause must survive for callers that inspect it.
	if !strings.Contains(msg, "not enough space") {
		t.Errorf("the underlying cause was lost:\n%s", msg)
	}
}

// The state a failed restore leaves behind must be retryable.
//
// There is no distro to unregister the second time round, and `wsl
// --unregister` on a missing name is an error — which used to abort the retry
// before the import that would have recovered the data.
func TestRestoreRetriesAfterTheDistroIsAlreadyGone(t *testing.T) {
	w := &fakeWSL{
		unregisterErr: errors.New("There is no distribution with the supplied name."),
		distros:       nil, // and the list agrees: it really is gone
	}
	m := newManager(t, w)
	if _, err := m.Save(context.Background(), "golden", ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := m.Restore(context.Background(), "golden"); err != nil {
		t.Fatalf("Restore did not recover from a missing distro: %v", err)
	}
	if !w.imported {
		t.Error("the retry never reached the import")
	}
}

// But an unregister that fails while the distro is still there is a real
// failure, and must not be walked past into an import that would then fail
// for a second, more confusing reason.
func TestRestoreStillFailsWhenUnregisterFailsAndTheDistroRemains(t *testing.T) {
	w := &fakeWSL{
		unregisterErr: errors.New("access denied"),
		distros:       []string{"skrog-engine"},
	}
	m := newManager(t, w)
	if _, err := m.Save(context.Background(), "golden", ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	err := m.Restore(context.Background(), "golden")
	if err == nil {
		t.Fatal("Restore succeeded despite a failed unregister")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("the unregister failure was not reported: %v", err)
	}
	if w.imported {
		t.Error("the import ran even though the old distro is still registered")
	}
}
