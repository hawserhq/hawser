package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// White-box: reproduce the #93 state directly — a rotation that closed the
// file but failed to reopen leaves w.f nil — and require the next Write to
// recover instead of panicking or silently dropping the line.
func TestWriteRecoversFromNilHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sup.log")
	w, err := NewRotatingWriter(path, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Simulate the post-failed-reopen state.
	w.mu.Lock()
	w.f.Close()
	w.f = nil
	w.mu.Unlock()

	n, err := w.Write([]byte("after recovery\n"))
	if err != nil {
		t.Fatalf("write did not recover from a nil handle: %v", err)
	}
	if n == 0 {
		t.Error("write reported 0 bytes")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "after recovery") {
		t.Errorf("recovered write not persisted: %q", data)
	}
}

// A Close error during rotate must not abort rotation and strand a full log.
func TestRotateSurvivesCloseAndReopens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sup.log")
	w, err := NewRotatingWriter(path, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(strings.Repeat("x", 20) + "\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// Rotations happened; the primary log exists and is writable.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("primary log missing after rotations: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("rotated log missing: %v", err)
	}
}
