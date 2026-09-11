package supervise_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hawserhq/hawser/internal/supervise"
)

func TestStatsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := supervise.Stats{
		Lifecycle: supervise.Lifecycle{
			StartedAt:      time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
			EngineStarts:   3,
			IdleStops:      2,
			LastIdleStopAt: time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second),
		},
		Bridge: supervise.Bridge{
			Connections: 42, BytesToEngine: 1000, BytesToClient: 2000,
			ActiveConns: 1, Transport: "vsock",
		},
	}
	if err := supervise.WriteStats(dir, in); err != nil {
		t.Fatalf("WriteStats: %v", err)
	}

	got, ok, err := supervise.ReadStats(dir)
	if err != nil || !ok {
		t.Fatalf("ReadStats: %v (ok=%v)", err, ok)
	}
	if got.Lifecycle != in.Lifecycle || got.Bridge != in.Bridge {
		t.Errorf("round trip changed the numbers:\n got %+v\nwant %+v", got, in)
	}
	// The writer stamps these, so a reader can judge the reading's age and tell
	// a live supervisor's file from one left behind by a dead process (#166).
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt was not stamped")
	}
	if got.PID != os.Getpid() {
		t.Errorf("PID = %d, want the writer's %d", got.PID, os.Getpid())
	}
}

func TestReadStatsMissingFileIsNotAnError(t *testing.T) {
	// No supervisor has run since the state dir was made: a state, not a fault.
	got, ok, err := supervise.ReadStats(t.TempDir())
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if ok {
		t.Error("ok true with no file")
	}
	if got.UpdatedAt != (time.Time{}) {
		t.Errorf("got %+v, want the zero value", got)
	}
}

func TestReadStatsRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "supervisor-stats.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := supervise.ReadStats(dir); err == nil {
		t.Error("a corrupt stats file was accepted")
	}
}

func TestFreshness(t *testing.T) {
	// A stale reading must be visible as stale: its numbers are history, and
	// showing them as current is the one way this feature could mislead.
	fresh := supervise.Stats{UpdatedAt: time.Now()}
	if !fresh.Fresh() {
		t.Error("a just-written reading is not fresh")
	}
	old := supervise.Stats{UpdatedAt: time.Now().Add(-5 * time.Minute)}
	if old.Fresh() {
		t.Error("a five-minute-old reading is fresh")
	}
	if old.Age() < 4*time.Minute {
		t.Errorf("Age() = %s", old.Age())
	}
	var never supervise.Stats
	if never.Fresh() || never.Age() != 0 {
		t.Error("a zero Stats should be neither fresh nor aged")
	}
}

func TestWriteStatsIsAtomicallyReplaced(t *testing.T) {
	// Two writes in a row must leave one whole file, never a mix.
	dir := t.TempDir()
	for i := 1; i <= 5; i++ {
		if err := supervise.WriteStats(dir, supervise.Stats{
			Bridge: supervise.Bridge{Connections: uint64(i)},
		}); err != nil {
			t.Fatalf("WriteStats: %v", err)
		}
		got, ok, err := supervise.ReadStats(dir)
		if err != nil || !ok {
			t.Fatalf("ReadStats after write %d: %v", i, err)
		}
		if got.Bridge.Connections != uint64(i) {
			t.Fatalf("after write %d, read %d", i, got.Bridge.Connections)
		}
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d files, want just the stats file: %v", len(entries), entries)
	}
}
