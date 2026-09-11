package logging_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/logging"
)

func TestRotatesAtCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.log")
	w, err := logging.NewRotatingWriter(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := strings.Repeat("x", 39) + "\n" // 40 bytes
	for i := 0; i < 10; i++ {              // 400 bytes total
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	// Current file stays under the cap; predecessors exist; count is bounded.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 100 {
		t.Errorf("current log is %d bytes, cap is 100", st.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Error("no rotated predecessor .1")
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Error(".3 exists; keep=2 must bound the trail")
	}
}

func TestOversizedRecordStillLands(t *testing.T) {
	// One record larger than the cap must be written, not dropped: the huge
	// record is usually the panic trace rotation exists to preserve.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.log")
	w, err := logging.NewRotatingWriter(path, 50, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	w.Write([]byte("small\n"))
	big := strings.Repeat("B", 200)
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatalf("oversized write failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), big) {
		t.Error("oversized record was not preserved in the current file")
	}
}

func TestSurvivesReopen(t *testing.T) {
	// The supervisor restarts across logons; appending must continue, and the
	// recorded size must come from the file rather than assumed zero, or the
	// cap drifts upward forever.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.log")

	w1, err := logging.NewRotatingWriter(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	w1.Write([]byte(strings.Repeat("a", 60) + "\n"))
	w1.Close()

	w2, err := logging.NewRotatingWriter(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	w2.Write([]byte(strings.Repeat("b", 60) + "\n")) // must trigger rotation

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Error("rotation did not account for pre-existing size after reopen")
	}
}

func TestManyRotationsKeepBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.log")
	w, err := logging.NewRotatingWriter(path, 50, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 100; i++ {
		fmt.Fprintf(w, "%04d %s\n", i, strings.Repeat("y", 20))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 4 { // s.log + .1 + .2 + .3
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("%d log files %v; keep=3 must bound this at 4", len(entries), names)
	}
}
