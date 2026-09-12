package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A snapshot whose sidecar write fails must not leave the tarball behind.
//
// Exists() keys on the sidecar, so the archive was invisible to List and
// Delete refused with "no such snapshot" before ever reaching it: a file the
// size of the whole engine with no CLI way to remove it. The usual way to get
// there is the volume filling up during the export, which is a common reason
// to be snapshotting a nearly-full disk in the first place (#246).
func TestSaveDiscardsTheArchiveWhenTheSidecarCannotBeWritten(t *testing.T) {
	w := &fakeWSL{}
	m := newManager(t, w)

	// Make the sidecar unwritable by putting a directory where it goes.
	if err := os.MkdirAll(filepath.Join(m.StateDir, "snapshots", "golden.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.Save(context.Background(), "golden", "")
	if err == nil {
		t.Fatal("Save reported success despite the sidecar write failing")
	}
	tar := m.ArchivePath("golden")
	if _, statErr := os.Stat(tar); statErr == nil {
		t.Errorf("the archive was left behind at %s, invisible to list and undeletable", tar)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("unexpected error checking the archive: %v", statErr)
	}
}

// And an archive orphaned by a version that shipped before the fix must be
// removable, rather than answered with "no such snapshot".
func TestDeleteRemovesAnArchiveWhoseSidecarIsMissing(t *testing.T) {
	m := newManager(t, &fakeWSL{})
	dir := filepath.Join(m.StateDir, "snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "stale.tar")
	if err := os.WriteFile(orphan, []byte("many gigabytes, pretend"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := m.Delete("stale"); err != nil {
		t.Fatalf("Delete refused an orphaned archive: %v", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the orphaned archive is still there: %v", err)
	}

	// A name with neither file is still an error, not a silent success.
	if err := m.Delete("never-existed"); err == nil {
		t.Error("Delete accepted a snapshot that does not exist at all")
	}
}
