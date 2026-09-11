package supervise

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestartRequestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if RestartRequested(dir) {
		t.Fatal("a fresh state dir already has a restart pending")
	}
	if err := RequestRestart(dir); err != nil {
		t.Fatalf("RequestRestart: %v", err)
	}
	if !RestartRequested(dir) {
		t.Error("the request was not visible after being written")
	}
	if err := ClearRestart(dir); err != nil {
		t.Fatalf("ClearRestart: %v", err)
	}
	if RestartRequested(dir) {
		t.Error("the request survived being cleared")
	}
}

func TestClearRestartIsIdempotent(t *testing.T) {
	// The supervisor clears at startup whether or not anything is pending, so
	// "nothing to clear" must not be an error.
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := ClearRestart(dir); err != nil {
			t.Fatalf("ClearRestart on an empty dir: %v", err)
		}
	}
}

func TestRequestRestartCreatesTheStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	if err := RequestRestart(dir); err != nil {
		t.Fatalf("RequestRestart: %v", err)
	}
	if !RestartRequested(dir) {
		t.Error("request not found in a state dir that had to be created")
	}
}

func TestRequestRestartLeavesNoTempFile(t *testing.T) {
	// The write is atomic via rename; a leftover .tmp would be litter in the
	// state dir a user browses.
	dir := t.TempDir()
	if err := RequestRestart(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("left behind %s", e.Name())
		}
	}
}
